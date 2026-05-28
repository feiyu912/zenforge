package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/feiyu912/zenforge/model"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

func New(config Config) *Client {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     config.APIKey,
		model:      config.Model,
		httpClient: httpClient,
	}
}

func (c *Client) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	events, err := c.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	var response model.Response
	var content strings.Builder
	for event := range events {
		if event.Error != nil {
			return nil, event.Error
		}
		if event.Delta != "" {
			content.WriteString(event.Delta)
		}
		if event.Message != nil {
			response.Message = *event.Message
		}
		if len(event.ToolCalls) > 0 {
			response.Message.ToolCalls = append(response.Message.ToolCalls, event.ToolCalls...)
		}
		if event.Usage.TotalTokens != 0 || event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			response.Usage = event.Usage
		}
	}
	if response.Message.Role == "" {
		response.Message.Role = "assistant"
	}
	if response.Message.Content == "" {
		response.Message.Content = content.String()
	}
	return &response, nil
}

func (c *Client) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	if c.model == "" {
		return nil, fmt.Errorf("openai model is required")
	}
	body, err := json.Marshal(c.chatRequest(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("openai chat completion failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	events := make(chan model.Event, 32)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		if err := readSSE(resp.Body, events); err != nil && !errors.Is(err, io.EOF) {
			events <- model.Event{Type: model.EventError, Error: err}
		}
	}()
	return events, nil
}

func (c *Client) chatRequest(req model.Request) chatRequest {
	out := chatRequest{
		Model:       c.model,
		Stream:      true,
		Messages:    make([]chatMessage, 0, len(req.Messages)),
		Tools:       tools(req.Tools),
		ToolChoice:  toolChoice(req.ToolChoice),
		StreamUsage: map[string]bool{"include_usage": true},
	}
	for _, message := range req.Messages {
		out.Messages = append(out.Messages, chatMessage{
			Role:       message.Role,
			Content:    message.Content,
			Name:       message.Name,
			ToolCallID: message.ToolCallID,
			ToolCalls:  chatToolCalls(message.ToolCalls),
		})
	}
	return out
}

func readSSE(body io.Reader, events chan<- model.Event) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	acc := newAccumulator()
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			message := acc.message()
			events <- model.Event{Type: model.EventDone, Message: &message}
			return nil
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		if chunk.Usage != nil {
			events <- model.Event{Type: model.EventDone, Usage: usage(*chunk.Usage)}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Role != "" {
				acc.role = choice.Delta.Role
			}
			if choice.Delta.Content != "" {
				acc.content.WriteString(choice.Delta.Content)
				events <- model.Event{Type: model.EventDelta, Delta: choice.Delta.Content}
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				acc.addToolCall(toolCall)
			}
			if choice.FinishReason != "" {
				message := acc.message()
				events <- model.Event{Type: model.EventDone, Message: &message}
			}
		}
	}
	return scanner.Err()
}

func tools(specs []model.ToolSpec) []chatTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(specs))
	for _, spec := range specs {
		parameters := spec.Schema
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  parameters,
			},
		})
	}
	return out
}

func toolChoice(choice model.ToolChoice) any {
	switch choice {
	case model.ToolChoiceNone:
		return "none"
	case model.ToolChoiceAuto:
		return "auto"
	default:
		return nil
	}
}

func chatToolCalls(calls []model.ToolCallSpec) []chatMessageToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]chatMessageToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, chatMessageToolCall{
			ID:   call.ID,
			Type: "function",
			Function: chatToolCallFunction{
				Name:      call.Name,
				Arguments: string(call.Arguments),
			},
		})
	}
	return out
}

func usage(value chatUsage) model.Usage {
	return model.Usage{
		PromptTokens:     value.PromptTokens,
		CompletionTokens: value.CompletionTokens,
		TotalTokens:      value.TotalTokens,
	}
}

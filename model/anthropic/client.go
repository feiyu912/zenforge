package anthropic

import (
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

const (
	defaultBaseURL          = "https://api.anthropic.com/v1"
	defaultAnthropicVersion = "2023-06-01"
)

type Client struct {
	baseURL          string
	apiKey           string
	model            string
	maxTokens        int
	anthropicVersion string
	httpClient       *http.Client
}

type Config struct {
	BaseURL          string
	APIKey           string
	Model            string
	MaxTokens        int
	AnthropicVersion string
	HTTPClient       *http.Client
}

func New(config Config) *Client {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	version := config.AnthropicVersion
	if version == "" {
		version = defaultAnthropicVersion
	}
	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:          baseURL,
		apiKey:           config.APIKey,
		model:            config.Model,
		maxTokens:        maxTokens,
		anthropicVersion: version,
		httpClient:       httpClient,
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
		return nil, fmt.Errorf("anthropic model is required")
	}
	providerReq, err := c.messagesRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(providerReq)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Anthropic-Version", c.anthropicVersion)
	if c.apiKey != "" {
		httpReq.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("anthropic messages failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	events := make(chan model.Event, 32)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		if err := parseStream(resp.Body, events); err != nil && !errors.Is(err, io.EOF) {
			events <- model.Event{Type: model.EventError, Error: err}
		}
	}()
	return events, nil
}

func (c *Client) messagesRequest(req model.Request) (messagesRequest, error) {
	system, messages, err := messages(req.Messages)
	if err != nil {
		return messagesRequest{}, err
	}
	providerTools := tools(req.Tools)
	choice := toolChoice(req.ToolChoice)
	if len(providerTools) == 0 || req.ToolChoice == model.ToolChoiceNone {
		providerTools = nil
		choice = nil
	}
	return messagesRequest{
		Model:      c.model,
		MaxTokens:  c.maxTokens,
		System:     system,
		Messages:   messages,
		Tools:      providerTools,
		ToolChoice: choice,
		Stream:     true,
	}, nil
}

func messages(in []model.Message) (string, []message, error) {
	var system []string
	out := make([]message, 0, len(in))
	for index := 0; index < len(in); index++ {
		item := in[index]
		switch item.Role {
		case "system":
			if item.Content != "" {
				system = append(system, item.Content)
			}
		case "assistant":
			converted, err := assistantMessage(item)
			if err != nil {
				return "", nil, err
			}
			if len(converted.Content) > 0 {
				out = append(out, converted)
			}
		case "tool":
			blocks := make([]contentBlock, 0, 2)
			for index < len(in) && in[index].Role == "tool" {
				toolResult := in[index]
				blocks = append(blocks, contentBlock{
					Type:      "tool_result",
					ToolUseID: toolResult.ToolCallID,
					Content:   toolResult.Content,
				})
				index++
			}
			if index < len(in) && in[index].Role == "user" {
				blocks = append(blocks, contentBlock{Type: "text", Text: in[index].Content})
			} else {
				index--
			}
			out = append(out, message{Role: "user", Content: blocks})
		default:
			if item.Content != "" {
				out = append(out, message{Role: "user", Content: []contentBlock{{Type: "text", Text: item.Content}}})
			}
		}
	}
	return strings.Join(system, "\n\n"), out, nil
}

func assistantMessage(item model.Message) (message, error) {
	blocks := make([]contentBlock, 0, 1+len(item.ToolCalls))
	if item.Content != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: item.Content})
	}
	for _, call := range item.ToolCalls {
		input := map[string]any{}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &input); err != nil {
				return message{}, fmt.Errorf("decode anthropic assistant tool call %s: %w", call.ID, err)
			}
		}
		blocks = append(blocks, contentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Name,
			Input: input,
		})
	}
	return message{Role: "assistant", Content: blocks}, nil
}

func tools(specs []model.ToolSpec) []toolDefinition {
	if len(specs) == 0 {
		return nil
	}
	out := make([]toolDefinition, 0, len(specs))
	for _, spec := range specs {
		schema := spec.Schema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, toolDefinition{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: schema,
		})
	}
	return out
}

func toolChoice(choice model.ToolChoice) any {
	switch choice {
	case model.ToolChoiceNone:
		return nil
	case model.ToolChoiceAuto:
		return map[string]string{"type": "auto"}
	case model.ToolChoiceRequired:
		return map[string]string{"type": "any"}
	default:
		return nil
	}
}

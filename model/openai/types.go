package openai

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/model"
)

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []chatTool    `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"`
	Stream      bool          `json:"stream"`
	StreamUsage any           `json:"stream_options,omitempty"`
	// ResponseFormat carries a json_schema response_format when the
	// caller asked for a constrained final response.
	ResponseFormat any `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role string `json:"role"`
	// Content is a string for a plain message and a []contentPart for a
	// multipart one (text plus images). omitting it entirely for an
	// assistant turn that only called tools is what the API expects.
	Content    any                   `json:"content,omitempty"`
	Name       string                `json:"name,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolCalls  []chatMessageToolCall `json:"tool_calls,omitempty"`
}

// contentPart is one piece of a multipart message.
type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
}

type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// chatDelta also carries reasoning. Providers that expose reasoning in a
// chat-completions stream use one of these field names; both are read so a
// reasoning model's text is captured rather than silently dropped.
type chatReasoningFields struct {
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatMessageToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type chatChunk struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
	Error   *apiError    `json:"error,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    any    `json:"code,omitempty"`
}

type chatChoice struct {
	Delta        chatDelta `json:"delta"`
	FinishReason string    `json:"finish_reason"`
}

type chatDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []chatToolDelta `json:"tool_calls,omitempty"`
	chatReasoningFields
}

type chatToolDelta struct {
	Index    int                   `json:"index"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function chatToolCallDeltaFunc `json:"function,omitempty"`
}

type chatToolCallDeltaFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type toolCallAccumulator struct {
	id        string
	name      string
	arguments string
}

type accumulator struct {
	role         string
	content      strings.Builder
	reasoning    strings.Builder
	toolCalls    map[int]*toolCallAccumulator
	finishReason string
}

// reasoningText returns whichever reasoning field the provider populated.
func (d chatDelta) reasoningText() string {
	if d.ReasoningContent != "" {
		return d.ReasoningContent
	}
	return d.Reasoning
}

func newAccumulator() *accumulator {
	return &accumulator{role: "assistant", toolCalls: map[int]*toolCallAccumulator{}}
}

func (a *accumulator) addToolCall(delta chatToolDelta) {
	current := a.toolCalls[delta.Index]
	if current == nil {
		current = &toolCallAccumulator{}
		a.toolCalls[delta.Index] = current
	}
	if delta.ID != "" {
		current.id = delta.ID
	}
	if delta.Function.Name != "" {
		current.name = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		current.arguments += delta.Function.Arguments
	}
}

func (a *accumulator) message() model.Message {
	calls := make([]model.ToolCallSpec, 0, len(a.toolCalls))
	indices := make([]int, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, i := range indices {
		call := a.toolCalls[i]
		if call == nil {
			continue
		}
		calls = append(calls, model.ToolCallSpec{
			ID:        call.id,
			Name:      call.name,
			Arguments: json.RawMessage(call.arguments),
		})
	}
	return model.Message{
		Role:      a.role,
		Content:   a.content.String(),
		ToolCalls: calls,
		Reasoning: a.reasoning.String(),
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/tool"
)

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

type CallResult struct {
	Content           []Content      `json:"content,omitempty"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type Tool struct {
	client     Client
	definition ToolDefinition
}

func Tools(ctx context.Context, client Client) ([]tool.Tool, error) {
	if client == nil {
		return nil, fmt.Errorf("mcp client is required")
	}
	definitions, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, 0, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" {
			return nil, fmt.Errorf("mcp tool name is required")
		}
		out = append(out, &Tool{
			client:     client,
			definition: definition,
		})
	}
	return out, nil
}

func (t *Tool) Name() string {
	return t.definition.Name
}

func (t *Tool) Description() string {
	return t.definition.Description
}

func (t *Tool) Schema() map[string]any {
	if t.definition.InputSchema == nil {
		return map[string]any{"type": "object"}
	}
	return cloneMap(t.definition.InputSchema)
}

func (t *Tool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	result, err := t.client.CallTool(ctx, t.definition.Name, input)
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	output := resultText(result)
	out := tool.Result{
		Output:     output,
		Structured: cloneMap(result.StructuredContent),
		Metadata: map[string]any{
			"mcp": map[string]any{
				"isError": result.IsError,
				"content": result.Content,
			},
		},
	}
	if result.IsError {
		out.Error = output
		out.ExitCode = 1
	}
	return out, nil
}

func resultText(result CallResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, item := range result.Content {
		switch item.Type {
		case "text":
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		default:
			if item.Text != "" {
				parts = append(parts, item.Text)
			} else if item.Data != "" {
				parts = append(parts, item.Data)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if len(result.StructuredContent) == 0 {
		return ""
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return ""
	}
	return string(data)
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

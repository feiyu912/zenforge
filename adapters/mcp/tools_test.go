package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/tool"
)

func TestToolsAdaptsMCPTool(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{
			Name:        "search",
			Description: "Search docs.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
			},
		}},
		result: CallResult{
			Content: []Content{{Type: "text", Text: "found it"}},
			StructuredContent: map[string]any{
				"count": float64(1),
			},
		},
	}
	tools, err := Tools(context.Background(), client)
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(tools))
	}
	selected := tools[0]
	if selected.Name() != "search" || selected.Description() != "Search docs." {
		t.Fatalf("unexpected tool metadata: %s %s", selected.Name(), selected.Description())
	}

	result, err := selected.Call(context.Background(), json.RawMessage(`{"query":"zenforge"}`), toolContext())
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Output != "found it" {
		t.Fatalf("output = %q", result.Output)
	}
	if got := client.calledWith; got != `{"query":"zenforge"}` {
		t.Fatalf("arguments = %s", got)
	}
	if result.Structured["count"] != float64(1) {
		t.Fatalf("structured result = %#v", result.Structured)
	}
}

func TestToolReturnsMCPErrorAsToolError(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{Name: "fail"}},
		result: CallResult{
			Content: []Content{{Type: "text", Text: "remote failed"}},
			IsError: true,
		},
	}
	tools, err := Tools(context.Background(), client)
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	result, err := tools[0].Call(context.Background(), nil, toolContext())
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.ExitCode != 1 || result.Error != "remote failed" {
		t.Fatalf("unexpected error result: %#v", result)
	}
}

func TestToolsRejectsMissingNames(t *testing.T) {
	_, err := Tools(context.Background(), &fakeClient{definitions: []ToolDefinition{{Description: "nope"}}})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected missing name error, got %v", err)
	}
}

type fakeClient struct {
	definitions []ToolDefinition
	result      CallResult
	calledWith  string
}

func (f *fakeClient) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	return f.definitions, ctx.Err()
}

func (f *fakeClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (CallResult, error) {
	f.calledWith = string(arguments)
	return f.result, ctx.Err()
}

func toolContext() tool.Context {
	return tool.Context{}
}

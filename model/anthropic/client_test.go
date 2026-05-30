package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

func TestClientStreamsTextAndSendsMessagesRequest(t *testing.T) {
	var got messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("missing API key header")
		}
		if r.Header.Get("Anthropic-Version") == "" {
			t.Fatalf("missing anthropic version header")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`data: {"type":"message_delta","usage":{"output_tokens":2}}`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "claude-test"})
	response, err := client.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if response.Message.Content != "hello" {
		t.Fatalf("content = %q", response.Message.Content)
	}
	if response.Usage.PromptTokens != 3 || response.Usage.CompletionTokens != 2 || response.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", response.Usage)
	}
	if got.Model != "claude-test" || !got.Stream || got.System != "Be brief." {
		t.Fatalf("unexpected request: %#v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content[0].Text != "hi" {
		t.Fatalf("messages = %#v", got.Messages)
	}
}

func TestClientStreamsToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"search","input":{}}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"zenforge\"}"}}`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "claude-test"})
	response, err := client.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: "user", Content: "search"}},
		Tools: []model.ToolSpec{{
			Name:        "search",
			Description: "Search.",
			Schema:      map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.Message.ToolCalls)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "search" || string(call.Arguments) != `{"query":"zenforge"}` {
		t.Fatalf("unexpected call: %#v", call)
	}
}

func TestClientReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, Model: "claude-test"}).Stream(context.Background(), model.Request{})
	if err == nil || !strings.Contains(err.Error(), "anthropic messages failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

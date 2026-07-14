package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestNewNormalizesMiniMaxAnthropicBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://api.minimax.io/anthropic":        "https://api.minimax.io/anthropic/v1",
		"https://api.minimax.io/anthropic/":       "https://api.minimax.io/anthropic/v1",
		"https://api.minimaxi.com/anthropic":      "https://api.minimaxi.com/anthropic/v1",
		"https://compatible.example/anthropic":    "https://compatible.example/anthropic",
		"https://compatible.example/anthropic/v1": "https://compatible.example/anthropic/v1",
	}
	for input, want := range tests {
		if got := New(Config{BaseURL: input}).baseURL; got != want {
			t.Errorf("base URL for %q = %q, want %q", input, got, want)
		}
	}
}

func TestClientStreamsToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"search","input":{}}}`,
			`data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"query\""}}`,
			`data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":":\"zenforge\"}"}}`,
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

func TestMessagesMergeConsecutiveToolResultsAndFollowingUser(t *testing.T) {
	_, got, err := messages([]model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCallSpec{{ID: "one", Name: "first"}, {ID: "two", Name: "second"}}},
		{Role: "tool", ToolCallID: "one", Content: "first result"},
		{Role: "tool", ToolCallID: "two", Content: "second result"},
		{Role: "user", Content: "continue"},
	})
	if err != nil {
		t.Fatalf("messages returned error: %v", err)
	}
	if len(got) != 2 || got[1].Role != "user" || len(got[1].Content) != 3 {
		t.Fatalf("messages = %#v", got)
	}
	if got[1].Content[0].Type != "tool_result" || got[1].Content[1].ToolUseID != "two" || got[1].Content[2].Text != "continue" {
		t.Fatalf("merged user content = %#v", got[1].Content)
	}
}

func TestClientOmitsToolsForNoneChoice(t *testing.T) {
	var got messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	events, err := New(Config{BaseURL: server.URL, Model: "claude-test"}).Stream(context.Background(), model.Request{
		Messages:   []model.Message{{Role: "user", Content: "answer only"}},
		Tools:      []model.ToolSpec{{Name: "search"}},
		ToolChoice: model.ToolChoiceNone,
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	if got.Tools != nil || got.ToolChoice != nil {
		t.Fatalf("tools=%#v tool_choice=%#v, want both omitted", got.Tools, got.ToolChoice)
	}
}

func TestClientRejectsInvalidAssistantToolArguments(t *testing.T) {
	_, err := New(Config{Model: "claude-test"}).Stream(context.Background(), model.Request{Messages: []model.Message{{
		Role:      "assistant",
		ToolCalls: []model.ToolCallSpec{{ID: "toolu_bad", Name: "search", Arguments: json.RawMessage(`{"query":`)}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "toolu_bad") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientReturnsStreamingProviderError(t *testing.T) {
	httpClient := &http.Client{Transport: anthropicRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"try again\"}}\n\n",
			)),
		}, nil
	})}
	client := New(Config{BaseURL: "https://example.test", Model: "claude-test", HTTPClient: httpClient})
	events, err := client.Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var errorCount int
	var doneCount int
	for event := range events {
		if event.Type == model.EventError {
			errorCount++
			if event.Error == nil || !strings.Contains(event.Error.Error(), "try again") {
				t.Fatalf("error event = %#v", event)
			}
		}
		if event.Type == model.EventDone {
			doneCount++
		}
	}
	if errorCount != 1 || doneCount != 0 {
		t.Fatalf("error events = %d, done events = %d; want 1, 0", errorCount, doneCount)
	}

	_, err = client.Generate(context.Background(), model.Request{})
	if err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseStreamRejectsEOFBeforeMessageStop(t *testing.T) {
	events := make(chan model.Event, 4)
	err := parseStream(strings.NewReader(strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		``,
	}, "\n")), events)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "anthropic provider stream") {
		t.Fatalf("parseStream error = %v, want anthropic provider unexpected EOF", err)
	}
	close(events)

	var delta string
	var doneCount int
	for event := range events {
		delta += event.Delta
		if event.Type == model.EventDone {
			doneCount++
		}
	}
	if delta != "partial" {
		t.Fatalf("delta = %q, want partial", delta)
	}
	if doneCount != 0 {
		t.Fatalf("done event count = %d, want 0", doneCount)
	}
}

func TestParseStreamWrapsUnexpectedReadErrorAfterPartialContent(t *testing.T) {
	events := make(chan model.Event, 4)
	body := &anthropicFailingReader{
		data: []byte(strings.Join([]string{
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			``,
		}, "\n")),
		err: io.ErrUnexpectedEOF,
	}
	err := parseStream(body, events)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "anthropic provider stream") {
		t.Fatalf("parseStream error = %v, want wrapped anthropic provider unexpected EOF", err)
	}
	close(events)

	var delta string
	var doneCount int
	for event := range events {
		delta += event.Delta
		if event.Type == model.EventDone {
			doneCount++
		}
	}
	if delta != "partial" || doneCount != 0 {
		t.Fatalf("delta = %q, done events = %d; want partial, 0", delta, doneCount)
	}
}

func TestClientGenerateRejectsPartialResponseOnUnexpectedEOF(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		``,
	}, "\n")
	httpClient := &http.Client{Transport: anthropicRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	client := New(Config{BaseURL: "https://example.test", Model: "claude-test", HTTPClient: httpClient})

	events, err := client.Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var errorCount int
	var doneCount int
	for event := range events {
		if event.Type == model.EventError {
			errorCount++
			if !errors.Is(event.Error, io.ErrUnexpectedEOF) {
				t.Fatalf("error event = %#v, want unexpected EOF", event)
			}
		}
		if event.Type == model.EventDone {
			doneCount++
		}
	}
	if errorCount != 1 || doneCount != 0 {
		t.Fatalf("error events = %d, done events = %d; want 1, 0", errorCount, doneCount)
	}

	response, err := client.Generate(context.Background(), model.Request{})
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "anthropic provider stream") {
		t.Fatalf("Generate error = %v, want anthropic provider unexpected EOF", err)
	}
}

func TestParseStreamEmitsUsageThenSingleDone(t *testing.T) {
	events := make(chan model.Event, 4)
	err := parseStream(strings.NewReader(strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`,
		`data: {"type":"message_stop"}`,
		`data: not-json`,
		``,
	}, "\n")), events)
	if err != nil {
		t.Fatalf("parseStream returned error: %v", err)
	}
	close(events)

	var types []model.EventType
	var done model.Event
	for event := range events {
		types = append(types, event.Type)
		if event.Type == model.EventDone {
			done = event
		}
	}
	if len(types) != 2 || types[0] != model.EventUsage || types[1] != model.EventDone {
		t.Fatalf("event types = %#v", types)
	}
	if done.Meta["finish_reason"] != "tool_calls" || done.Usage.TotalTokens != 5 {
		t.Fatalf("done event = %#v", done)
	}
}

func TestRequiredToolChoice(t *testing.T) {
	got, ok := toolChoice(model.ToolChoiceRequired).(map[string]string)
	if !ok || got["type"] != "any" {
		t.Fatalf("tool choice = %#v", got)
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

func TestClientReturnsActionableAndRedactedHTTPError(t *testing.T) {
	const secret = "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid ` + secret + `"}`))
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, APIKey: secret, Model: "claude-test"}).Stream(context.Background(), model.Request{})
	if err == nil {
		t.Fatal("Stream returned nil error")
	}
	if !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "/messages") {
		t.Fatalf("error = %q", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked API key: %q", err)
	}
}

type anthropicRoundTripFunc func(*http.Request) (*http.Response, error)

func (f anthropicRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type anthropicFailingReader struct {
	data []byte
	err  error
}

func (r *anthropicFailingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

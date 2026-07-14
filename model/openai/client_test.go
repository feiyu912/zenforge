package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

func TestClientStreamsTextAndSendsChatRequest(t *testing.T) {
	var gotAuth string
	var gotReq chatRequest
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("Decode request returned error: %v", err)
		}
		return sseResponse(
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n" +
				"data: [DONE]\n\n",
		), nil
	})}

	client := New(Config{BaseURL: "https://example.test/v1", APIKey: "test-key", Model: "gpt-test", HTTPClient: httpClient})
	events, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: "user", Content: "hi"}},
		Tools: []model.ToolSpec{{
			Name:        "search",
			Description: "Search",
			Schema:      map[string]any{"type": "object"},
		}},
		ToolChoice: model.ToolChoiceAuto,
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var text string
	var usage model.Usage
	var doneCount int
	for event := range events {
		text += event.Delta
		if event.Type == model.EventDone {
			doneCount++
			if event.Meta["finish_reason"] != "stop" {
				t.Fatalf("finish reason metadata = %#v", event.Meta)
			}
		}
		if event.Usage.TotalTokens != 0 {
			usage = event.Usage
		}
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
	if gotReq.Model != "gpt-test" || !gotReq.Stream {
		t.Fatalf("unexpected request: %#v", gotReq)
	}
	if gotReq.ToolChoice != "auto" || len(gotReq.Tools) != 1 || gotReq.Tools[0].Function.Name != "search" {
		t.Fatalf("unexpected tool request: %#v", gotReq)
	}
	if text != "hello" {
		t.Fatalf("streamed text = %q", text)
	}
	if usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", usage)
	}
	if doneCount != 1 {
		t.Fatalf("done event count = %d, want 1", doneCount)
	}
}

func TestClientAccumulatesStreamingToolCalls(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"{\\\"query\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"function\":{\"arguments\":\"\\\"zen\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
		), nil
	})}

	client := New(Config{BaseURL: "https://example.test/v1", Model: "gpt-test", HTTPClient: httpClient})
	response, err := client.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "search"}}})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", response.Message.ToolCalls)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "search" || string(call.Arguments) != `{"query":"zen"}` {
		t.Fatalf("unexpected tool call: %#v", call)
	}
}

func TestClientOmitsToolChoiceWithoutTools(t *testing.T) {
	var gotReq chatRequest
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("Decode request returned error: %v", err)
		}
		return sseResponse("data: [DONE]\n\n"), nil
	})}

	events, err := New(Config{Model: "gpt-test", HTTPClient: httpClient}).Stream(context.Background(), model.Request{
		ToolChoice: model.ToolChoiceAuto,
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	if gotReq.ToolChoice != nil {
		t.Fatalf("tool_choice = %#v, want omitted", gotReq.ToolChoice)
	}
}

func TestClientReturnsStreamingProviderError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse("data: {\"error\":{\"message\":\"quota exhausted\",\"type\":\"insufficient_quota\"}}\n\n"), nil
	})}

	_, err := New(Config{Model: "gpt-test", HTTPClient: httpClient}).Generate(context.Background(), model.Request{})
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientReturnsActionableAndRedactedHTTPError(t *testing.T) {
	const secret = "test-secret"
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid ` + secret + `"}`)),
		}, nil
	})}

	_, err := New(Config{
		BaseURL:    "https://user:password@example.test/v1",
		APIKey:     secret,
		Model:      "gpt-test",
		HTTPClient: httpClient,
	}).Stream(context.Background(), model.Request{})
	if err == nil {
		t.Fatal("Stream returned nil error")
	}
	if !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "https://example.test/v1/chat/completions") {
		t.Fatalf("error = %q", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "user:password") {
		t.Fatalf("error leaked sensitive configuration: %q", err)
	}
}

func TestClientRejectsMalformedStreamingChunk(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse("data: {}\n\n"), nil
	})}

	_, err := New(Config{Model: "gpt-test", HTTPClient: httpClient}).Generate(context.Background(), model.Request{})
	if err == nil || !strings.Contains(err.Error(), "neither choices nor usage") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequiredToolChoice(t *testing.T) {
	if got := toolChoice(model.ToolChoiceRequired); got != "required" {
		t.Fatalf("tool choice = %#v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

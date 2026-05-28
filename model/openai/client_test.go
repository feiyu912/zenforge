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
	for event := range events {
		text += event.Delta
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
}

func TestClientAccumulatesStreamingToolCalls(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"{\\\"query\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"zen\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
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

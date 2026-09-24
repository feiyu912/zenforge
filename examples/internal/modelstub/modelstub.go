// Package modelstub is a scripted OpenAI-compatible endpoint for the examples'
// tests.
//
// The examples are written the way a deployment is: they call
// provider.FromEnv() and talk to a real HTTP endpoint. That is what makes them
// worth reading, and it is also what would keep them out of CI, which has no
// provider credential. This package closes that gap without changing the
// examples: it serves the same /v1/chat/completions an OpenAI-compatible
// endpoint serves, on a loopback port, from a script the test writes.
//
// It is deliberately not a mock in the unit-test sense. Every example test runs
// the example as a real process, with ZENFORGE_PROVIDER/MODEL/API_KEY/BASE_URL
// pointed here, so the code under test is the adapter, the agent loop, the
// tools, the approval broker and the checkpoint store -- only the model's words
// are scripted. A test can then assert what the model was told, which is how it
// proves a skill was disclosed or a tool result came back.
//
// It lives under examples/internal so every example can import it and nothing
// outside examples/ can.
package modelstub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// ToolCall is one function call a scripted turn asks the agent to make.
type ToolCall struct {
	// ID names the call. Empty means a generated one, which is what a test that
	// does not care about correlation should do.
	ID string
	// Name is the tool the agent must call.
	Name string
	// Arguments is the call's arguments as JSON; nil means "{}".
	Arguments any
}

// Turn is one scripted model reply. A turn either answers with Content, or asks
// for one or more tool calls, or both, exactly as an OpenAI-compatible endpoint
// may.
type Turn struct {
	// Content is the assistant text for this turn.
	Content string
	// ToolCalls are the calls this turn requests, in order.
	ToolCalls []ToolCall
	// FinishReason overrides the reported finish reason. Empty means "stop" for a
	// turn with no tool calls and "tool_calls" for one that has them.
	FinishReason string
}

// Say is a turn that answers with text.
func Say(content string) Turn { return Turn{Content: content} }

// Call is a turn that asks for one tool call and no text.
func Call(name string, arguments any) Turn {
	return Turn{ToolCalls: []ToolCall{{Name: name, Arguments: arguments}}}
}

// Message is one message the endpoint received. Only the fields a test asserts
// on are decoded; anything else the agent sends is ignored rather than refused,
// because this endpoint must not become a second implementation of the wire
// format.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an assistant message that requested calls.
	ToolCalls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	// ToolCallID is set on a tool message answering a call.
	ToolCallID string `json:"tool_call_id"`
}

// Request is one decoded request the endpoint served. It is kept so a test can
// assert what the agent told the model -- that a skill's description was
// disclosed, that a tool's result was sent back, that a resumed run carried its
// earlier steps.
type Request struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []Message `json:"messages"`
	// Tools is the advertised tool list, decoded only to the names, because that
	// is what a test asserts on.
	Tools []struct {
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"function"`
	} `json:"tools"`
}

// Text returns every message's text joined by newlines, for a substring
// assertion that does not want to walk the slice.
func (r Request) Text() string {
	var builder strings.Builder
	for _, message := range r.Messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

// ToolNames returns the names of the tools this request advertised.
func (r Request) ToolNames() []string {
	names := make([]string, 0, len(r.Tools))
	for _, tool := range r.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

// HasTool reports whether the request advertised a tool by name.
func (r Request) HasTool(name string) bool {
	for _, tool := range r.Tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

// Delivered reports whether a tool's result was sent back to the model, which is
// how a test proves the agent actually ran a tool rather than only announcing
// that it would.
func (r Request) Delivered(toolName string) bool {
	requested := map[string]string{}
	for _, message := range r.Messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				requested[call.ID] = call.Function.Name
			}
		}
		if message.Role == "tool" {
			if requested[message.ToolCallID] == toolName {
				return true
			}
		}
	}
	return false
}

// Server is the scripted endpoint. It serves turns in order, one per model
// call, and repeats the last turn once the script is exhausted: a run that asks
// for one more step than the script has must get an answer rather than an
// endpoint failure, or a test would fail for a reason the example is not about.
type Server struct {
	server *httptest.Server

	mu       sync.Mutex
	turns    []Turn
	requests []Request
}

// New starts a scripted endpoint that serves the given turns in order. An empty
// script is a programming error and panics rather than serving an endpoint that
// can never answer.
func New(turns ...Turn) *Server {
	if len(turns) == 0 {
		panic("modelstub.New needs at least one turn")
	}
	stub := &Server{turns: turns}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	return stub
}

// Close stops the endpoint.
func (s *Server) Close() { s.server.Close() }

// URL is the endpoint's base URL, without a trailing slash.
func (s *Server) URL() string { return s.server.URL }

// Env is the environment that points provider.FromEnv() here: the protocol, the
// model name, a credential that is accepted because this endpoint never checks
// one, and the base URL. A test appends it to os.Environ() for the example
// process.
func (s *Server) Env() []string {
	return []string{
		"ZENFORGE_PROVIDER=openai",
		"ZENFORGE_MODEL=scripted-model",
		"ZENFORGE_API_KEY=scripted-key",
		"ZENFORGE_BASE_URL=" + s.URL() + "/v1",
	}
}

// Requests returns every request served so far, in order, as a copy.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// Calls is the number of model calls the run made.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Last is the last request served, and whether there was one. A test that needs
// it asserts on the bool, because every assertion about what the model was told
// depends on the endpoint having been called at all.
func (s *Server) Last() (Request, bool) {
	requests := s.Requests()
	if len(requests) == 0 {
		return Request{}, false
	}
	return requests[len(requests)-1], true
}

// serve answers one chat completion, in the shape the openai adapter asked for:
// a stream of SSE deltas when the request set stream, a single JSON body when it
// did not.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request Request
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "modelstub: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	turn := s.turns[min(len(s.requests), len(s.turns)-1)]
	s.requests = append(s.requests, request)
	s.mu.Unlock()

	if request.Stream {
		writeStream(w, turn)
		return
	}
	writeBody(w, turn)
}

// writeStream serves a turn as SSE deltas. Tool calls are assembled the way the
// adapter expects: one delta opens the call with its index, id and name, the
// next carries the arguments.
func writeStream(w http.ResponseWriter, turn Turn) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emit := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id":      "chatcmpl-stub",
			"object":  "chat.completion.chunk",
			"model":   "scripted-model",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		encoded, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit(map[string]any{"role": "assistant"}, nil)
	if turn.Content != "" {
		emit(map[string]any{"content": turn.Content}, nil)
	}
	for index, call := range turn.ToolCalls {
		emit(map[string]any{"tool_calls": []any{map[string]any{
			"index": index,
			"id":    callID(call, index),
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": argsJSON(call),
			},
		}}}, nil)
	}
	emit(map[string]any{}, finishReason(turn))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// writeBody serves a turn as one non-streaming completion.
func writeBody(w http.ResponseWriter, turn Turn) {
	message := map[string]any{"role": "assistant", "content": turn.Content}
	if len(turn.ToolCalls) > 0 {
		calls := make([]any, 0, len(turn.ToolCalls))
		for index, call := range turn.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   callID(call, index),
				"type": "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": argsJSON(call),
				},
			})
		}
		message["tool_calls"] = calls
	}
	body, _ := json.Marshal(map[string]any{
		"id":     "chatcmpl-stub",
		"object": "chat.completion",
		"model":  "scripted-model",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason(turn),
		}},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func callID(call ToolCall, index int) string {
	if strings.TrimSpace(call.ID) != "" {
		return call.ID
	}
	return fmt.Sprintf("call_%d", index)
}

func argsJSON(call ToolCall) string {
	if call.Arguments == nil {
		return "{}"
	}
	if text, ok := call.Arguments.(string); ok {
		return text
	}
	encoded, err := json.Marshal(call.Arguments)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func finishReason(turn Turn) string {
	if strings.TrimSpace(turn.FinishReason) != "" {
		return turn.FinishReason
	}
	if len(turn.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

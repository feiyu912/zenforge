// Package scripted serves the scripted OpenAI-compatible endpoint every
// benchmark runner talks to.
//
// The benchmark compares frameworks, not models, so all four runners are
// pointed at one endpoint that replays a frozen turn script. That is what makes
// the numbers that remain -- latency, request count, prompt bytes -- framework
// differences rather than model differences.
//
// It is deliberately not a mock in the unit-test sense. Each runner is a real
// subprocess that speaks the OpenAI chat-completions protocol over loopback;
// only the model's words are scripted. The endpoint therefore serves both
// response shapes a real provider serves (SSE stream and a single JSON body),
// records every request body it received, and reports how long it spent
// answering, which is what lets the harness subtract model time from
// wall-clock.
//
// The sibling package examples/internal/modelstub established the response
// shape this endpoint must keep: the openai adapter assembles streamed tool
// calls from deltas, requires a finish_reason before [DONE], and rejects a
// chunk that carries neither choices nor usage. This package follows that shape
// and adds the benchmark's turn selection, request recording, and cost
// accounting.
//
// Turn selection is by conversation state, not by request count, because
// frameworks differ in how many model calls they make. See SelectTurn for the
// exact rule.
package scripted

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// maxRequestBody bounds how much of one request the endpoint will read. The
// benchmark's prompts are small; the cap exists so a runaway runner cannot
// exhaust the harness's memory.
const maxRequestBody = 8 << 20

// ToolCall is one function call a scripted turn asks the agent to make.
type ToolCall struct {
	// ID names the call. Empty means the endpoint generates one for the turn
	// it was issued from, which is the stable id every runner sees. A script
	// leaves it empty unless it needs to correlate a call by hand.
	ID string `json:"id,omitempty"`
	// Name is the tool the agent must call.
	Name string `json:"name"`
	// Arguments is the call's arguments as a JSON object. Nil means "{}".
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Turn is one scripted model reply. A turn either answers with Content, or
// asks for one or more tool calls, or both, exactly as an OpenAI-compatible
// endpoint may.
type Turn struct {
	// Content is the assistant text for this turn.
	Content string `json:"content,omitempty"`
	// ToolCalls are the calls this turn requests, in order.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Script is one task's frozen turn script.
type Script struct {
	// Task is the task id the script belongs to.
	Task string `json:"task"`
	// Query is the exact user message every runner must send for this task.
	// It lives in the frozen script rather than in each runner because prompt
	// bytes are a reported cost metric: a runner that invented its own task
	// sentence could win the cost column by writing a shorter one. A
	// framework's own system prompt and tool schemas stay its own, because
	// those are the legitimate differences the benchmark measures.
	Query string `json:"query"`
	// Turns are served in conversation-state order; see SelectTurn.
	Turns []Turn `json:"turns"`
}

// ParseScript decodes and validates a turn script.
func ParseScript(data []byte) (Script, error) {
	var script Script
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&script); err != nil {
		return Script{}, fmt.Errorf("scripted: decode turn script: %w", err)
	}
	if strings.TrimSpace(script.Task) == "" {
		return Script{}, fmt.Errorf("scripted: turn script has no task id")
	}
	if len(script.Turns) == 0 {
		return Script{}, fmt.Errorf("scripted: turn script %q has no turns", script.Task)
	}
	for index, turn := range script.Turns {
		if strings.TrimSpace(turn.Content) == "" && len(turn.ToolCalls) == 0 {
			return Script{}, fmt.Errorf("scripted: turn %d of %q is empty", index, script.Task)
		}
		for callIndex, call := range turn.ToolCalls {
			if strings.TrimSpace(call.Name) == "" {
				return Script{}, fmt.Errorf("scripted: turn %d call %d of %q has no name", index, callIndex, script.Task)
			}
		}
	}
	return script, nil
}

// IssuedCall is one tool call the endpoint issued, decoded from a recorded
// request. Arguments is kept as the raw JSON string the provider saw, because
// the verifier compares it against the task's frozen command without inventing
// a normalization the frameworks do not share.
type IssuedCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is one message a recorded request carried.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an assistant message that requested calls.
	ToolCalls []IssuedCall `json:"tool_calls,omitempty"`
	// ToolCallID is set on a tool message answering a call.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolDef is one tool the request advertised. The schema is kept raw so the
// harness can measure the exact bytes the endpoint received.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request is one request the endpoint served, captured whole.
type Request struct {
	// Body is the exact request body the runner sent.
	Body []byte
	// Model, Stream, Messages and Tools are the decoded view of Body.
	Model    string
	Stream   bool
	Messages []Message
	Tools    []ToolDef
	// ServedTurn is the index of the script turn this request was answered
	// with. The verifier uses it to place a tool result before the call the
	// script issued next.
	ServedTurn int
	// ToolSchemaBytes is how many bytes of Body the tools array occupied.
	// It is a subset of len(Body): the share of the prompt spent on tool
	// definitions.
	ToolSchemaBytes int
	// ServiceTime is how long the endpoint spent producing this response,
	// which the harness subtracts from wall-clock to report framework
	// overhead.
	ServiceTime time.Duration
}

// LastMessage returns the final message of the request, and whether the
// request carried any. Turn selection looks at it.
func (r Request) LastMessage() (Message, bool) {
	if len(r.Messages) == 0 {
		return Message{}, false
	}
	return r.Messages[len(r.Messages)-1], true
}

// HasTool reports whether the request advertised a tool by name.
func (r Request) HasTool(name string) bool {
	for _, tool := range r.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// Server is the scripted endpoint. It owns the turn script, the conversation
// state that decides which turn to serve, and the record of every request.
type Server struct {
	http  *httptest.Server
	turns []Turn

	mu              sync.Mutex
	requests        []Request
	issued          map[string]int
	lastServed      int
	promptBytes     int64
	toolSchemaBytes int64
	modelTime       time.Duration
	serial          int64
}

// New starts an endpoint that replays the given turns. It returns an error for
// an empty script: an endpoint that can never answer would make every runner
// fail for a reason the runner is not responsible for.
func New(turns []Turn) (*Server, error) {
	if len(turns) == 0 {
		return nil, fmt.Errorf("scripted: at least one turn is required")
	}
	for index, turn := range turns {
		if strings.TrimSpace(turn.Content) == "" && len(turn.ToolCalls) == 0 {
			return nil, fmt.Errorf("scripted: turn %d is empty", index)
		}
	}
	server := &Server{turns: turns, issued: map[string]int{}}
	server.http = httptest.NewServer(http.HandlerFunc(server.serve))
	return server, nil
}

// MustNew is New for callers whose script is a compile-time constant: a bad
// script there is a programming error, not a runtime condition.
func MustNew(turns []Turn) *Server {
	server, err := New(turns)
	if err != nil {
		panic(err)
	}
	return server
}

// NewScript is MustNew over a parsed Script's turns.
func NewScript(script Script) *Server { return MustNew(script.Turns) }

// Close stops the endpoint.
func (s *Server) Close() { s.http.Close() }

// BaseURL is the OpenAI-compatible base URL runners are pointed at, ending in
// /v1 exactly as a real provider's base URL does.
func (s *Server) BaseURL() string { return s.http.URL + "/v1" }

// Requests returns a deep copy of every request served so far, in the order
// they arrived. A copy matters: the caller reads it while runners are still
// talking to the endpoint.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	for index, request := range s.requests {
		out[index] = cloneRequest(request)
	}
	return out
}

// Count is the number of model requests served.
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// PromptBytes is the total request-body size sent upstream. It is the cost
// metric the report uses: bytes are exactly what the endpoint observed, while a
// dollar figure would invent a price for a model that was never called.
func (s *Server) PromptBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptBytes
}

// ToolSchemaBytes is the part of PromptBytes spent on the advertised tool
// definitions.
func (s *Server) ToolSchemaBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolSchemaBytes
}

// ModelServiceTime is the total time the endpoint spent producing responses.
// The harness subtracts it from wall-clock so the reported latency is the
// framework's own overhead rather than the scripted model's service time.
func (s *Server) ModelServiceTime() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelTime
}

// LastServedTurn reports the turn most recently served. It exists for
// diagnostics: a caller inspecting a run can tell how far the script advanced.
func (s *Server) LastServedTurn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastServed
}

// SelectTurn implements the frozen contract's rule:
//
//   - the endpoint issues each turn's tool calls with stable ids and records
//     which turn produced each id;
//   - a request whose last message is a tool result for one of those ids is
//     served the next turn;
//   - any other request is served the turn it served last, so a framework that
//     retries the same question gets the same answer instead of desynchronizing
//     the script;
//   - once the script is exhausted, the last turn is served again.
//
// A framework that asks the same question twice therefore sees the same tool
// call twice and executes the tool twice, which is a real cost of its own loop
// and is left visible rather than papered over.
func (s *Server) SelectTurn(messages []Message) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectTurnLocked(messages)
}

// selectTurnLocked is SelectTurn's body; the caller holds s.mu.
func (s *Server) selectTurnLocked(messages []Message) int {
	index := s.lastServed
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Role == "tool" && strings.TrimSpace(last.ToolCallID) != "" {
			if issuedTurn, ok := s.issued[last.ToolCallID]; ok {
				index = issuedTurn + 1
				if index > len(s.turns)-1 {
					index = len(s.turns) - 1
				}
			}
		}
	}
	s.lastServed = index
	// Re-register the served turn's call ids. Registration is idempotent, so a
	// re-served turn keeps the same ids and a framework that retried cannot
	// see a different call than it saw the first time.
	for callIndex, call := range s.turns[index].ToolCalls {
		s.issued[callID(index, callIndex, call)] = index
	}
	return index
}

// serve answers one chat completion.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, "scripted: "+err.Error(), http.StatusBadRequest)
		return
	}
	wire, err := decodeWire(body)
	if err != nil {
		http.Error(w, "scripted: "+err.Error(), http.StatusBadRequest)
		return
	}

	started := time.Now()

	s.mu.Lock()
	index := s.selectTurnLocked(wire.Messages)
	s.serial++
	request := Request{
		Body:            append([]byte(nil), body...),
		Model:           wire.Model,
		Stream:          wire.Stream,
		Messages:        wire.Messages,
		Tools:           wire.Tools,
		ServedTurn:      index,
		ToolSchemaBytes: len(wire.ToolsRaw),
	}
	slot := len(s.requests)
	s.requests = append(s.requests, request)
	s.promptBytes += int64(len(body))
	s.toolSchemaBytes += int64(len(wire.ToolsRaw))
	s.mu.Unlock()

	turn := s.turns[index]
	if wire.Stream {
		writeStream(w, wire, index, turn, roughTokens(len(body)), roughTokens(turnBytes(turn)))
	} else {
		writeBody(w, wire, index, turn, roughTokens(len(body)), roughTokens(turnBytes(turn)))
	}

	serviceTime := time.Since(started)
	s.mu.Lock()
	s.requests[slot].ServiceTime = serviceTime
	s.modelTime += serviceTime
	s.mu.Unlock()
}

// wireRequest is one decoded chat-completions request.
type wireRequest struct {
	Model    string
	Stream   bool
	Messages []Message
	Tools    []ToolDef
	// ToolsRaw is the exact JSON of the request's tools member, used for the
	// tool-schema byte count.
	ToolsRaw json.RawMessage
	// IncludeUsage mirrors stream_options.include_usage: a streaming client
	// asks for the usage chunk that way, exactly as it does against a real
	// provider.
	IncludeUsage bool
}

func decodeWire(body []byte) (wireRequest, error) {
	var envelope struct {
		Model         string            `json:"model"`
		Stream        bool              `json:"stream"`
		Messages      []json.RawMessage `json:"messages"`
		Tools         json.RawMessage   `json:"tools"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return wireRequest{}, fmt.Errorf("decode request: %w", err)
	}
	wire := wireRequest{
		Model:        envelope.Model,
		Stream:       envelope.Stream,
		ToolsRaw:     envelope.Tools,
		IncludeUsage: envelope.StreamOptions.IncludeUsage,
	}
	for _, raw := range envelope.Messages {
		message, err := decodeMessage(raw)
		if err != nil {
			return wireRequest{}, err
		}
		wire.Messages = append(wire.Messages, message)
	}
	if len(envelope.Tools) > 0 && string(envelope.Tools) != "null" {
		var tools []struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(envelope.Tools, &tools); err != nil {
			return wireRequest{}, fmt.Errorf("decode tools: %w", err)
		}
		for _, tool := range tools {
			wire.Tools = append(wire.Tools, ToolDef{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  append(json.RawMessage(nil), tool.Function.Parameters...),
			})
		}
	}
	return wire, nil
}

func decodeMessage(raw json.RawMessage) (Message, error) {
	var fields struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Message{}, fmt.Errorf("decode message: %w", err)
	}
	message := Message{Role: fields.Role, Content: textContent(fields.Content), ToolCallID: fields.ToolCallID}
	for _, call := range fields.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, IssuedCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return message, nil
}

// textContent decodes a message's content, which is a string in the common
// case and an array of content parts when the request carried images.
func textContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var builder strings.Builder
		for _, part := range parts {
			if part.Type == "text" || part.Type == "" {
				builder.WriteString(part.Text)
			}
		}
		return builder.String()
	}
	return ""
}

// cloneRequest deep-copies one recorded request, so a caller that holds a
// snapshot cannot observe a later mutation.
func cloneRequest(request Request) Request {
	out := request
	out.Body = append([]byte(nil), request.Body...)
	out.Messages = make([]Message, len(request.Messages))
	for index, message := range request.Messages {
		cloned := message
		cloned.ToolCalls = append([]IssuedCall(nil), message.ToolCalls...)
		out.Messages[index] = cloned
	}
	out.Tools = make([]ToolDef, len(request.Tools))
	for index, tool := range request.Tools {
		cloned := tool
		cloned.Parameters = append(json.RawMessage(nil), tool.Parameters...)
		out.Tools[index] = cloned
	}
	return out
}

// callID is the stable id of the callIndex-th call of a turn. It depends only
// on the turn's position, so re-serving a turn re-issues the same ids and a
// framework that retried a question sees the same call, not a new one.
func callID(turnIndex, callIndex int, call ToolCall) string {
	if strings.TrimSpace(call.ID) != "" {
		return call.ID
	}
	return fmt.Sprintf("call_%d_%d", turnIndex, callIndex)
}

// turnBytes is the model-visible size of a turn, used only for the rough usage
// integers below.
func turnBytes(turn Turn) int {
	total := len(turn.Content)
	for _, call := range turn.ToolCalls {
		total += len(call.Name) + len(argsJSON(call))
	}
	return total
}

// roughTokens converts bytes to a rough token count. It is deliberately
// trivial and documented as rough: no tokenizer is being benchmarked, and the
// cost metrics the harness reports are byte counts, not this.
func roughTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}

func argsJSON(call ToolCall) string {
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		return "{}"
	}
	return string(call.Arguments)
}

func finishReason(turn Turn) string {
	if len(turn.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

// writeStream serves a turn as SSE deltas. Tool calls are assembled the way the
// openai adapter expects: one delta opens the call with its index, id and name,
// and carries the arguments.
func writeStream(w http.ResponseWriter, request wireRequest, turnIndex int, turn Turn, promptTokens, completionTokens int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emit := func(payload map[string]any) {
		encoded, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit(chunk(request, map[string]any{"role": "assistant"}, nil))
	if turn.Content != "" {
		emit(chunk(request, map[string]any{"content": turn.Content}, nil))
	}
	for index, call := range turn.ToolCalls {
		emit(chunk(request, map[string]any{"tool_calls": []any{map[string]any{
			"index": index,
			"id":    callID(turnIndex, index, call),
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": argsJSON(call),
			},
		}}}, nil))
	}
	emit(chunk(request, map[string]any{}, finishReason(turn)))
	if request.IncludeUsage {
		// A usage chunk carries no choices, which is exactly how a real
		// provider reports streamed usage when include_usage is set.
		emit(map[string]any{
			"id":      "chatcmpl-scripted",
			"object":  "chat.completion.chunk",
			"model":   modelName(request),
			"choices": []any{},
			"usage":   usage(promptTokens, completionTokens),
		})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// writeBody serves a turn as one non-streaming completion. A non-streaming
// client always gets usage, exactly as a real provider returns it.
func writeBody(w http.ResponseWriter, request wireRequest, turnIndex int, turn Turn, promptTokens, completionTokens int) {
	message := map[string]any{"role": "assistant", "content": turn.Content}
	if len(turn.ToolCalls) > 0 {
		calls := make([]any, 0, len(turn.ToolCalls))
		for index, call := range turn.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   callID(turnIndex, index, call),
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
		"id":     "chatcmpl-scripted",
		"object": "chat.completion",
		"model":  modelName(request),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason(turn),
		}},
		"usage": usage(promptTokens, completionTokens),
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func chunk(request wireRequest, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-scripted",
		"object":  "chat.completion.chunk",
		"model":   modelName(request),
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

func usage(promptTokens, completionTokens int) map[string]any {
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
}

func modelName(request wireRequest) string {
	if strings.TrimSpace(request.Model) != "" {
		return request.Model
	}
	return "scripted-model"
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This file is a hermetic, loopback stand-in for the harness endpoint
// (benchmarks/internal/scripted). It implements the turn-selection rule from
// benchmarks/README.md exactly:
//
//   - a script is a list of turns; each turn issues tool calls with stable ids;
//   - a request whose last message is a tool result for one of those ids is
//     served the next turn;
//   - any other request is served the turn it served last (no advance);
//   - once the script is exhausted, the last turn is served again.
//
// It records every request body, so a test can assert what the model actually
// saw, and it answers both non-streaming (JSON) and streaming (SSE) requests.

type stubToolCall struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type stubTurn struct {
	Content   string
	ToolCalls []stubToolCall
}

type stubMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []stubToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
}

func (m stubMessage) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	return string(m.Content)
}

type recordedRequest struct {
	Messages   []stubMessage
	Stream     bool
	ServedTurn int
}

func (r recordedRequest) allText() string {
	var b strings.Builder
	for _, m := range r.Messages {
		b.WriteString(m.text())
		b.WriteByte('\n')
		for _, tc := range m.ToolCalls {
			b.WriteString(tc.Name)
			b.WriteByte(' ')
			b.WriteString(tc.Arguments)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

type scriptedEndpoint struct {
	mu         sync.Mutex
	turns      []stubTurn
	lastServed int
	idToTurn   map[string]int
	requests   []recordedRequest
	server     *httptest.Server
	baseURL    string
}

func newScriptedEndpoint(t *testing.T, turns []stubTurn) *scriptedEndpoint {
	t.Helper()
	e := &scriptedEndpoint{turns: turns, idToTurn: map[string]int{}}
	e.server = httptest.NewServer(http.HandlerFunc(e.handle))
	t.Cleanup(e.server.Close)
	e.baseURL = e.server.URL + "/v1"
	return e
}

func (e *scriptedEndpoint) recorded() []recordedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]recordedRequest, len(e.requests))
	copy(out, e.requests)
	return out
}

type inboundRequest struct {
	Model    string        `json:"model"`
	Messages []stubMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

func (e *scriptedEndpoint) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}

	var req inboundRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	e.mu.Lock()
	turnIdx := e.lastServed
	if n := len(req.Messages); n > 0 {
		if last := req.Messages[n-1]; last.Role == "tool" {
			if produced, ok := e.idToTurn[last.ToolCallID]; ok {
				turnIdx = produced + 1
			}
		}
	}
	if turnIdx >= len(e.turns) {
		turnIdx = len(e.turns) - 1
	}
	if turnIdx < 0 {
		turnIdx = 0
	}
	e.lastServed = turnIdx
	turn := e.turns[turnIdx]
	for i := range turn.ToolCalls {
		e.idToTurn[turn.ToolCalls[i].ID] = turnIdx
	}
	e.requests = append(e.requests, recordedRequest{Messages: req.Messages, Stream: req.Stream, ServedTurn: turnIdx})
	e.mu.Unlock()

	if req.Stream {
		e.writeSSE(w, turn)
		return
	}
	e.writeJSON(w, turn)
}

func (e *scriptedEndpoint) responseMessage(turn stubTurn) map[string]any {
	msg := map[string]any{"role": "assistant", "content": turn.Content}
	if len(turn.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(turn.ToolCalls))
		for _, tc := range turn.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		msg["tool_calls"] = calls
	} else {
		msg["content"] = turn.Content
	}
	return msg
}

func (e *scriptedEndpoint) writeJSON(w http.ResponseWriter, turn stubTurn) {
	finish := "stop"
	if len(turn.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	resp := map[string]any{
		"id":      "chatcmpl-bench",
		"object":  "chat.completion",
		"created": 0,
		"model":   "bench-model",
		"choices": []map[string]any{{
			"index":         0,
			"message":       e.responseMessage(turn),
			"finish_reason": finish,
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (e *scriptedEndpoint) writeSSE(w http.ResponseWriter, turn stubTurn) {
	finish := "stop"
	if len(turn.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	delta := map[string]any{"role": "assistant", "content": turn.Content}
	if len(turn.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(turn.ToolCalls))
		for i, tc := range turn.ToolCalls {
			calls = append(calls, map[string]any{
				"index": i,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		delta["tool_calls"] = calls
	}

	writeChunk := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	writeChunk(map[string]any{
		"id": "chatcmpl-bench", "object": "chat.completion.chunk", "created": 0, "model": "bench-model",
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	writeChunk(map[string]any{
		"id": "chatcmpl-bench", "object": "chat.completion.chunk", "created": 0, "model": "bench-model",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func stubCall(id, name, args string) stubToolCall {
	return stubToolCall{ID: id, Type: "function", Name: name, Arguments: args}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func e2eConfig(t *testing.T, baseURL, task, phase, approval string, requirePause bool) (Config, string, string) {
	t.Helper()
	ws := t.TempDir()
	state := t.TempDir()
	return Config{
		BaseURL:      baseURL,
		APIKey:       "bench-placeholder",
		Model:        "bench-model",
		Task:         task,
		Workspace:    ws,
		StateDir:     state,
		Phase:        phase,
		Approval:     approval,
		RequirePause: requirePause,
		ResultPath:   filepath.Join(t.TempDir(), "result.json"),
		Query:        frozenQueries[task],
		QueryFromEnv: true,
	}, ws, state
}

// frozenQueries are the task instructions the harness passes in BENCH_QUERY.
// The runner must send them verbatim as the user message.
var frozenQueries = map[string]string{
	TaskEditFile:       "Read input.txt, then write its edited contents to out.txt.",
	TaskApproveCommand: "Append the word approved to approval.txt with a shell command.",
	TaskDurableTask: "Record step 1 and step 2 in steps.txt, create milestone.txt with a shell command, " +
		"then write the final artifact to out.txt.",
}

// TestEndToEndUserMessageIsBenchQueryVerbatim proves the framework's prompt cost
// is the frozen instruction, not wording this runner invented.
func TestEndToEndUserMessageIsBenchQueryVerbatim(t *testing.T) {
	endpoint := newScriptedEndpoint(t, []stubTurn{{Content: "nothing to do"}})
	cfg, _, _ := e2eConfig(t, endpoint.baseURL, TaskEditFile, PhaseRun, ApprovalApprove, false)

	if res := Execute(cfg); res.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want completed", res.Status, res.Detail)
	}

	reqs := endpoint.recorded()
	if len(reqs) == 0 {
		t.Fatal("the endpoint saw no requests")
	}
	var user string
	for _, m := range reqs[0].Messages {
		if m.Role == "user" {
			user = m.text()
		}
	}
	if want := frozenQueries[TaskEditFile]; user != want {
		t.Errorf("user message = %q, want the frozen BENCH_QUERY text %q", user, want)
	}
}

// TestEndToEndEditFile drives the real OpenAI adapter against the loopback
// endpoint and checks the artifact plus the request the write_file turn saw.
func TestEndToEndEditFile(t *testing.T) {
	const inputContent = "the original contents\nsecond line\n"
	const outputContent = "the original contents\nsecond line\nappended\n"

	endpoint := newScriptedEndpoint(t, []stubTurn{
		{ToolCalls: []stubToolCall{stubCall("call-read", "read_file", `{"path":"input.txt"}`)}},
		{ToolCalls: []stubToolCall{stubCall("call-write", "write_file", `{"path":"out.txt","content":`+jsonString(outputContent)+`}`)}},
		{Content: "wrote out.txt"},
	})

	cfg, ws, _ := e2eConfig(t, endpoint.baseURL, TaskEditFile, PhaseRun, ApprovalApprove, false)
	mustWrite(t, filepath.Join(ws, "input.txt"), inputContent)

	res := Execute(cfg)
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want completed", res.Status, res.Detail)
	}

	got, err := os.ReadFile(filepath.Join(ws, "out.txt"))
	if err != nil {
		t.Fatalf("out.txt: %v", err)
	}
	if string(got) != outputContent {
		t.Errorf("out.txt = %q, want %q", string(got), outputContent)
	}

	reqs := endpoint.recorded()
	if len(reqs) < 3 {
		t.Fatalf("endpoint saw %d requests, want at least 3", len(reqs))
	}

	// The write_file turn must have been served to a request whose history
	// already contained the read_file result.
	var sawReadResultBeforeWrite bool
	for _, r := range reqs {
		if r.ServedTurn != 1 {
			continue
		}
		for _, m := range r.Messages {
			if m.Role == "tool" && m.ToolCallID == "call-read" && strings.Contains(m.text(), "the original contents") {
				sawReadResultBeforeWrite = true
			}
		}
	}
	if !sawReadResultBeforeWrite {
		t.Errorf("the read_file result did not reach the model before the write_file turn; requests: %s",
			describeRequests(reqs))
	}
}

func TestEndToEndApproveCommand(t *testing.T) {
	// The harness's revised frozen command: it must put "approved" on stdout, so
	// the tool result the model sees is the command's real output rather than
	// anything this runner appended.
	const command = `echo approved | tee -a approval.txt`
	turns := []stubTurn{
		{ToolCalls: []stubToolCall{stubCall("call-shell", "run_shell", `{"command":`+jsonString(command)+`}`)}},
		{Content: "the command ran"},
	}

	t.Run("approve", func(t *testing.T) {
		endpoint := newScriptedEndpoint(t, turns)
		cfg, ws, _ := e2eConfig(t, endpoint.baseURL, TaskApproveCommand, PhaseRun, ApprovalApprove, false)

		res := Execute(cfg)
		if res.Status != StatusCompleted {
			t.Fatalf("status = %q (%s), want completed", res.Status, res.Detail)
		}

		b, err := os.ReadFile(filepath.Join(ws, "approval.txt"))
		if err != nil {
			t.Fatalf("approved command did not run: %v", err)
		}
		if string(b) != "approved\n" {
			t.Errorf("approval.txt = %q, want %q", string(b), "approved\n")
		}

		reqs := endpoint.recorded()
		if got := toolResultFor(reqs, "call-shell"); got != "approved\n" {
			t.Errorf("the tool result the model saw = %q, want the command's stdout verbatim %q;"+
				" requests: %s", got, "approved\n", describeRequests(reqs))
		}
	})

	t.Run("reject", func(t *testing.T) {
		endpoint := newScriptedEndpoint(t, turns)
		cfg, ws, _ := e2eConfig(t, endpoint.baseURL, TaskApproveCommand, PhaseRun, ApprovalReject, false)

		res := Execute(cfg)
		if res.Status != StatusCompleted {
			t.Fatalf("status = %q (%s), want completed", res.Status, res.Detail)
		}
		if _, err := os.Stat(filepath.Join(ws, "approval.txt")); err == nil {
			t.Fatal("a rejected command ran")
		}
		reqs := endpoint.recorded()
		// A rejection is a tool result too, and it legitimately quotes the
		// command. What must never appear is the command's *output*.
		got := toolResultFor(reqs, "call-shell")
		if got == "approved\n" || strings.Contains(got, "approved\n") {
			t.Errorf("a rejected command's stdout reached the model: %q; requests: %s", got, describeRequests(reqs))
		}
		if !strings.Contains(got, "approval denied") {
			t.Errorf("the model was not told the command was refused: %q", got)
		}
	})
}

// TestEndToEndDurableTask runs the two-phase recovery task across two fresh
// Execute calls, as the harness runs two processes.
func TestEndToEndDurableTask(t *testing.T) {
	const stepOne = "step 1: read the request\n"
	const stepTwo = "step 1: read the request\nstep 2: draft the change\n"

	endpoint := newScriptedEndpoint(t, []stubTurn{
		{ToolCalls: []stubToolCall{stubCall("call-step1", "write_file", `{"path":"steps.txt","content":`+jsonString(stepOne)+`}`)}},
		{ToolCalls: []stubToolCall{stubCall("call-step2", "write_file", `{"path":"steps.txt","content":`+jsonString(stepTwo)+`}`)}},
		{ToolCalls: []stubToolCall{stubCall("call-shell", "run_shell", `{"command":"echo milestone | tee milestone.txt"}`)}},
		{Content: "durable task complete"},
	})

	cfg, ws, state := e2eConfig(t, endpoint.baseURL, TaskDurableTask, PhaseRun, ApprovalApprove, true)

	first := Execute(cfg)
	if first.Status != StatusPaused {
		t.Fatalf("phase run status = %q (%s), want paused", first.Status, first.Detail)
	}
	if first.ExitCode() != ExitPaused {
		t.Fatalf("phase run exit code = %d, want %d", first.ExitCode(), ExitPaused)
	}
	beforePause, err := os.ReadFile(filepath.Join(ws, "steps.txt"))
	if err != nil {
		t.Fatalf("steps.txt written before the pause: %v", err)
	}
	if string(beforePause) != stepTwo {
		t.Fatalf("steps.txt before the pause = %q, want %q", string(beforePause), stepTwo)
	}
	if _, err := os.Stat(filepath.Join(state, resumeStateFile)); err != nil {
		t.Fatalf("no durable resume state: %v", err)
	}

	resumeCfg := cfg
	resumeCfg.Phase = PhaseResume
	second := Execute(resumeCfg)
	if second.Status != StatusCompleted {
		t.Fatalf("phase resume status = %q (%s), want completed", second.Status, second.Detail)
	}

	after, err := os.ReadFile(filepath.Join(ws, "steps.txt"))
	if err != nil {
		t.Fatalf("steps.txt after resume: %v", err)
	}
	if !strings.HasPrefix(string(after), stepTwo) {
		t.Errorf("the resumed artifact lost the steps recorded before the pause: %q", string(after))
	}
	milestone, err := os.ReadFile(filepath.Join(ws, "milestone.txt"))
	if err != nil {
		t.Fatalf("the resumed command did not create milestone.txt: %v", err)
	}
	if string(milestone) != "milestone\n" {
		t.Errorf("milestone.txt = %q, want %q", string(milestone), "milestone\n")
	}
	if got := toolResultFor(endpoint.recorded(), "call-shell"); got != "milestone\n" {
		t.Errorf("the resumed run_shell result = %q, want the command's stdout verbatim", got)
	}
	if !strings.Contains(second.Detail, "durable task complete") {
		t.Errorf("resume detail = %q, want the agent's final answer", second.Detail)
	}
}

// TestRunMainProtocolEndToEnd checks the process-level contract: BENCH_RESULT
// contents and the exit code returned by runMain, including the 75/1 mapping.
func TestRunMainProtocolEndToEnd(t *testing.T) {
	endpoint := newScriptedEndpoint(t, []stubTurn{
		{Content: "nothing to do"},
	})

	ws := t.TempDir()
	state := t.TempDir()
	resultPath := filepath.Join(t.TempDir(), "result.json")

	t.Setenv("BENCH_BASE_URL", endpoint.baseURL)
	t.Setenv("BENCH_API_KEY", "bench-placeholder")
	t.Setenv("BENCH_MODEL", "bench-model")
	t.Setenv("BENCH_TASK", TaskEditFile)
	t.Setenv("BENCH_WORKSPACE", ws)
	t.Setenv("BENCH_STATE_DIR", state)
	t.Setenv("BENCH_PHASE", PhaseRun)
	t.Setenv("BENCH_APPROVAL", ApprovalApprove)
	t.Setenv("BENCH_RESULT", resultPath)
	t.Setenv("BENCH_QUERY", frozenQueries[TaskEditFile])

	if code := runMain(); code != ExitCompleted {
		t.Fatalf("runMain exit = %d, want %d", code, ExitCompleted)
	}

	// The user message the process actually sent must be BENCH_QUERY verbatim.
	reqs := endpoint.recorded()
	if len(reqs) == 0 {
		t.Fatal("the endpoint saw no requests")
	}
	var user string
	for _, m := range reqs[0].Messages {
		if m.Role == "user" {
			user = m.text()
		}
	}
	if user != frozenQueries[TaskEditFile] {
		t.Errorf("user message = %q, want the BENCH_QUERY text %q", user, frozenQueries[TaskEditFile])
	}

	b, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("BENCH_RESULT not written: %v", err)
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("BENCH_RESULT is not valid JSON: %v (%s)", err, b)
	}
	if res.Task != TaskEditFile || res.Phase != PhaseRun || res.Status != StatusCompleted {
		t.Errorf("result = %+v", res)
	}
	if !strings.HasPrefix(res.Framework, "eino ") {
		t.Errorf("framework = %q", res.Framework)
	}

	// A bad BENCH_TASK must still produce a failed result and exit 1.
	t.Setenv("BENCH_TASK", "not-a-task")
	if code := runMain(); code != ExitFailed {
		t.Fatalf("runMain with a bad task exit = %d, want %d", code, ExitFailed)
	}
	b, err = os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("BENCH_RESULT not rewritten: %v", err)
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("BENCH_RESULT is not valid JSON: %v", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("status = %q, want %q", res.Status, StatusFailed)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// toolResultFor returns the content of the first tool message carrying callID
// across the recorded conversation. It is the exact text the model received as
// that tool's result.
func toolResultFor(reqs []recordedRequest, callID string) string {
	for _, r := range reqs {
		for _, m := range r.Messages {
			if m.Role == "tool" && m.ToolCallID == callID {
				return m.text()
			}
		}
	}
	return ""
}

func describeRequests(reqs []recordedRequest) string {
	var b strings.Builder
	for i, r := range reqs {
		fmt.Fprintf(&b, "\n  request %d (served turn %d, stream=%v):", i, r.ServedTurn, r.Stream)
		for j, m := range r.Messages {
			text := m.text()
			if len(text) > 60 {
				text = text[:60] + "..."
			}
			fmt.Fprintf(&b, "\n    [%d] role=%s tool_call_id=%s content=%q tool_calls=%v",
				j, m.Role, m.ToolCallID, text, m.ToolCalls)
		}
	}
	return b.String()
}

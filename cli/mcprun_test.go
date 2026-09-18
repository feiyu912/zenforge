package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
)

// servedRunOptions is the operator's side of a served run: a workspace, a
// model endpoint, and the approval mode under test.
func servedRunOptions(t *testing.T, baseURL string) options {
	t.Helper()
	workspace := t.TempDir()
	opts := defaultOptions()
	opts.workspace = workspace
	opts.shellWorkingDir = workspace
	opts.checkpointDir = t.TempDir()
	opts.apiKey = "test"
	opts.baseURL = baseURL
	opts.approve = "prompt"
	// A served run is a normal run: planning is disabled here for the same
	// reason the CLI tests disable it, so the stub can answer directly.
	opts.planning = "disabled"
	return opts
}

func servedRunStreams() IO {
	return IO{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
}

func TestMCPRunToolIsNotReadOnlyAndRequiresAPrompt(t *testing.T) {
	opts := servedRunOptions(t, "http://127.0.0.1:1")
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	if runTool.Name != mcpRunToolName {
		t.Fatalf("tool name = %q", runTool.Name)
	}
	// The client-side half of the approval path: a conforming MCP client asks
	// its operator before calling a tool that is not declared read-only, so
	// this must never claim to be one.
	if runTool.ReadOnly {
		t.Fatal("the run tool claims to be read-only")
	}
	required, _ := runTool.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "prompt" {
		t.Fatalf("required = %#v", runTool.InputSchema["required"])
	}
	if _, err := runTool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a call without a prompt was accepted")
	}
	if _, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"   "}`)); err == nil {
		t.Fatal("a blank prompt was accepted")
	}
	if _, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":`)); err == nil {
		t.Fatal("malformed arguments were accepted")
	}
}

func TestMCPRunToolReturnsTheFinalAnswerAndRecordsTheRun(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("the answer from the served run"))
	opts := servedRunOptions(t, model.url)
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())

	result, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello"}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("the run reported an error: %#v", result.Content)
	}
	runID, _ := result.StructuredContent["runId"].(string)
	if runID == "" {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	if result.StructuredContent["status"] != "completed" {
		t.Fatalf("status = %#v", result.StructuredContent["status"])
	}
	if !strings.Contains(result.Content[0].Text, "the answer from the served run") {
		t.Fatalf("text = %q", result.Content[0].Text)
	}
	// The run is durable: the same store zenforge_runs reads has it.
	summaries, closeStore, err := listRuns(context.Background(), opts.checkpointType, opts.checkpointDir)
	if err != nil {
		t.Fatalf("listRuns returned error: %v", err)
	}
	defer func() { _ = closeStore() }()
	found := false
	for _, summary := range summaries {
		if summary.RunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("run %s was not recorded: %#v", runID, summaries)
	}
}

func TestServedRunRefusesToolCallsThatNeedAnOperatorAndSaysSo(t *testing.T) {
	model := newOpenAISSEStub(t,
		toolCallChunk("call_shell", "shell", `{"command":"printf served-run-refused","description":"print a marker"}`),
		textChunk("the shell call was refused"),
	)
	opts := servedRunOptions(t, model.url)
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())

	result, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"run the shell tool"}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("a refused tool call failed the whole run: %#v", result.Content)
	}
	refused, _ := result.StructuredContent["refusedToolCalls"].([]string)
	if len(refused) != 1 || refused[0] != "shell" {
		t.Fatalf("refusedToolCalls = %#v", result.StructuredContent["refusedToolCalls"])
	}
	if !strings.Contains(result.Content[0].Text, "--approve always") {
		t.Fatalf("the refusal did not explain how to allow it: %q", result.Content[0].Text)
	}
	// The model saw the refusal and adapted, which is the point of resolving
	// it instead of stopping the run.
	if !strings.Contains(model.body(), "approval_rejected") {
		t.Fatalf("the refusal never reached the model: %s", model.body())
	}
}

func TestServedRunRunsApprovalGatedToolsWhenTheOperatorAllowedThem(t *testing.T) {
	model := newOpenAISSEStub(t,
		toolCallChunk("call_shell", "shell", `{"command":"printf served-run-allowed","description":"print a marker"}`),
		textChunk("the shell call ran"),
	)
	opts := servedRunOptions(t, model.url)
	opts.approve = "always"
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())

	result, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"run the shell tool"}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("the run reported an error: %#v", result.Content)
	}
	if _, ok := result.StructuredContent["refusedToolCalls"]; ok {
		t.Fatalf("an allowed run reported refusals: %#v", result.StructuredContent)
	}
	if !strings.Contains(model.body(), "served-run-allowed") {
		t.Fatalf("the approved tool never ran: %s", model.body())
	}
}

func TestServedRunReportsATimeoutWithTheRunID(t *testing.T) {
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the model call open until the run's deadline cancels it.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer model.Close()
	// Registered after Close, so it runs before it: a cancellation is not
	// guaranteed to surface as the request context being done (a request
	// whose response headers never arrived may leave the handler waiting),
	// and Close waits for handlers. Releasing the hold first keeps the
	// cleanup from depending on which one happened.
	defer close(release)

	opts := servedRunOptions(t, model.URL)
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())

	result, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello"}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a timed-out run was reported as a success: %#v", result.StructuredContent)
	}
	if result.StructuredContent["status"] != "timeout" {
		t.Fatalf("status = %#v; text=%q", result.StructuredContent["status"], result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "was cancelled after") {
		t.Fatalf("text = %q", result.Content[0].Text)
	}
}

// TestMCPServerRunToolAppearsOnlyWithTheOperatorGrant pins the grant itself:
// without --allow-run the tool is not in the catalog, so a caller cannot even
// ask for it, and the read-only shape of the served set is unchanged.
func TestMCPServerRunToolAppearsOnlyWithTheOperatorGrant(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("unused"))
	opts := servedRunOptions(t, model.url)

	// Without the grant the handler is never built, and an attempt to call the
	// tool by name is an unknown tool for the protocol layer.
	tools, err := mcpServerTools(context.Background(), opts.checkpointType, opts.checkpointDir)
	if err != nil {
		t.Fatalf("mcpServerTools returned error: %v", err)
	}
	for _, serverTool := range tools {
		if serverTool.Name == mcpRunToolName {
			t.Fatal("the run tool is exposed without --allow-run")
		}
		if !serverTool.ReadOnly {
			t.Fatalf("tool %q is not read-only", serverTool.Name)
		}
	}

	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	server, err := mcp.NewServer(mcp.ServerConfig{Name: "zenforge", Version: "test", Tools: append(tools, runTool)})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	response, _ := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+mcpRunToolName+`","arguments":{"prompt":"hello"}}}`))
	var call struct {
		Result mcp.CallResult `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &call); err != nil {
		t.Fatalf("the response is not JSON (%v): %s", err, response)
	}
	if call.Error != nil {
		t.Fatalf("an advertised tool call was rejected: %s", response)
	}
	if call.Result.IsError || len(call.Result.Content) == 0 {
		t.Fatalf("result = %#v", call.Result)
	}
	// tools/list must carry the run tool without a read-only hint.
	listResponse, _ := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`))
	var listed struct {
		Result struct {
			Tools []mcp.ToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listResponse, &listed); err != nil {
		t.Fatalf("the tools/list response is not JSON (%v): %s", err, listResponse)
	}
	if len(listed.Result.Tools) != len(tools)+1 {
		t.Fatalf("tools = %#v", listed.Result.Tools)
	}
	last := listed.Result.Tools[len(listed.Result.Tools)-1]
	if last.Name != mcpRunToolName || last.ReadOnly() {
		t.Fatalf("run tool definition = %#v", last)
	}
}

// newHoldableOpenAISSEStub serves one SSE response only after the test closes
// release, and reports when a request has arrived. It exists for tests that
// need a run to be observably in flight.
func newHoldableOpenAISSEStub(t *testing.T, response string) (url string, started chan struct{}, release func()) {
	t.Helper()
	started = make(chan struct{}, 1)
	hold := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, response)
	}))
	t.Cleanup(server.Close)
	// Registered after Close, so a test that fails before it releases the
	// hold still lets the handler return and Close finish.
	t.Cleanup(release)
	return server.URL, started, release
}

// TestServedRunHoldsTheServerSlot pins the one-run-at-a-time rule: `Serve`
// answers one message at a time, but `Handle` is exported and a host may drive
// it concurrently, and two runs on one agent would share state that is not
// built for it. The second caller hears that the server is busy rather than
// being queued behind a run it cannot see.
func TestServedRunHoldsTheServerSlot(t *testing.T) {
	url, started, release := newHoldableOpenAISSEStub(t, textChunk("first"))
	opts := servedRunOptions(t, url)
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())

	first := make(chan error, 1)
	go func() {
		_, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"first"}`))
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first run never reached the model")
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelWait()
	_, busyErr := runTool.Handler(waitCtx, json.RawMessage(`{"prompt":"second"}`))
	if !errors.Is(busyErr, context.DeadlineExceeded) {
		t.Fatalf("second call error = %v, want the waiting caller's deadline", busyErr)
	}
	if !strings.Contains(busyErr.Error(), "another served run is in progress") {
		t.Fatalf("the busy server did not say why the call was refused: %v", busyErr)
	}

	release()
	if err := <-first; err != nil {
		t.Fatalf("the first run failed: %v", err)
	}
}

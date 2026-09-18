package cli

import (
	"bufio"
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
	"github.com/feiyu912/zenforge/approval"
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
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
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
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
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
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
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
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
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
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), 50*time.Millisecond, newServedRunRegistry(context.Background()))
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
	tools, err := mcpServerTools(context.Background(), opts.checkpointType, opts.checkpointDir, newServedRunRegistry(context.Background()))
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

	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	cancelTool := newMCPRunCancelTool(opts.checkpointType, opts.checkpointDir, newServedRunRegistry(context.Background()))
	server, err := mcp.NewServer(mcp.ServerConfig{Name: "zenforge", Version: "test", Tools: append(tools, runTool, cancelTool)})
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
	// The grant is the capability list: it adds starting a run *and* stopping
	// one, both without a read-only hint so the calling client asks its own
	// operator.
	if len(listed.Result.Tools) != len(tools)+2 {
		t.Fatalf("tools = %#v", listed.Result.Tools)
	}
	granted := map[string]bool{}
	for _, tool := range listed.Result.Tools[len(listed.Result.Tools)-2:] {
		granted[tool.Name] = tool.ReadOnly()
	}
	if readOnly, ok := granted[mcpRunToolName]; !ok || readOnly {
		t.Fatalf("run tool definition = %#v", listed.Result.Tools)
	}
	if readOnly, ok := granted[servedRunCancelName]; !ok || readOnly {
		t.Fatalf("cancel tool definition = %#v", listed.Result.Tools)
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
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
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

// runToolNotification is the wire form of a notifications/progress frame, as
// the client would decode it.
type runToolNotification struct {
	Method string `json:"method"`
	Params struct {
		ProgressToken any     `json:"progressToken"`
		Progress      float64 `json:"progress"`
		Message       string  `json:"message"`
	} `json:"params"`
}

// runToolServer builds the protocol layer around one served run tool, so a
// test exercises the path a client's tools/call actually takes: token parsing
// in the adapter, the reporter on the handler's context, and the served run's
// own event loop.
func runToolServer(t *testing.T, opts *options) *mcp.Server {
	t.Helper()
	runTool, err := newMCPRunTool(context.Background(), opts, servedRunStreams(), time.Minute, newServedRunRegistry(context.Background()))
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools:   []mcp.ServerTool{runTool},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

// callRunTool sends one tools/call for the run tool and returns the decoded
// result plus every progress frame the handler emitted.
func callRunTool(t *testing.T, server *mcp.Server, params string) (mcp.CallResult, [][]byte) {
	t.Helper()
	var frames [][]byte
	ctx := mcp.WithNotificationSink(context.Background(), func(frame []byte) error {
		frames = append(frames, append([]byte(nil), frame...))
		return nil
	})
	response, respond := server.Handle(ctx, []byte(
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":`+params+`}`,
	))
	if !respond {
		t.Fatal("the run call was not answered")
	}
	var decoded struct {
		Result mcp.CallResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("the response is not JSON (%v): %s", err, response)
	}
	if decoded.Error != nil {
		t.Fatalf("the run call was rejected: %s", response)
	}
	return decoded.Result, frames
}

// TestMCPRunToolReportsProgressFromRunEvents pins the client's view of a
// streamed served run: one notifications/progress per run event, counted in
// order, with the event type as the message, all before the single result the
// tool returns.
func TestMCPRunToolReportsProgressFromRunEvents(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("the answer from the served run"))
	opts := servedRunOptions(t, model.url)
	defer drainClosers(&opts, servedRunStreams())
	server := runToolServer(t, &opts)

	result, frames := callRunTool(t, server,
		`{"name":"`+mcpRunToolName+`","arguments":{"prompt":"say hello"},"_meta":{"progressToken":"watch-1"}}`)
	if result.IsError {
		t.Fatalf("the run reported an error: %#v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "the answer from the served run") {
		t.Fatalf("the result changed: %#v", result.Content)
	}
	if len(frames) < 2 {
		t.Fatalf("progress notifications = %d, want one per run event: %q", len(frames), frames)
	}
	messages := make([]string, 0, len(frames))
	for index, frame := range frames {
		var notification runToolNotification
		if err := json.Unmarshal(frame, &notification); err != nil {
			t.Fatalf("notification %d is not JSON (%v): %s", index, err, frame)
		}
		if notification.Method != "notifications/progress" {
			t.Fatalf("notification %d method = %q", index, notification.Method)
		}
		if notification.Params.ProgressToken != "watch-1" {
			t.Fatalf("notification %d token = %#v", index, notification.Params.ProgressToken)
		}
		if notification.Params.Progress != float64(index+1) {
			t.Fatalf("notification %d progress = %v, want the running event count %d", index, notification.Params.Progress, index+1)
		}
		if notification.Params.Message == "" {
			t.Fatalf("notification %d carried no event type: %s", index, frame)
		}
		messages = append(messages, notification.Params.Message)
	}
	// The order is the run's own: the first event starts it, the last ends it.
	if messages[0] != "run.started" || messages[len(messages)-1] != "run.done" {
		t.Fatalf("progress messages = %#v", messages)
	}
}

// TestMCPRunToolSendsNoProgressWithoutAToken pins the other half: a caller
// that did not attach a token gets no notifications at all, and the run
// behaves exactly as it did before progress existed.
func TestMCPRunToolSendsNoProgressWithoutAToken(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("the answer from the served run"))
	opts := servedRunOptions(t, model.url)
	defer drainClosers(&opts, servedRunStreams())
	server := runToolServer(t, &opts)

	result, frames := callRunTool(t, server,
		`{"name":"`+mcpRunToolName+`","arguments":{"prompt":"say hello"}}`)
	if result.IsError {
		t.Fatalf("the run reported an error: %#v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "the answer from the served run") {
		t.Fatalf("the result changed: %#v", result.Content)
	}
	if len(frames) != 0 {
		t.Fatalf("a call without a token received %q", frames)
	}
	if result.StructuredContent["status"] != "completed" {
		t.Fatalf("status = %#v", result.StructuredContent["status"])
	}
}

// servedRunServe is one mcp.Server serving a run tool over real pipes, with the
// client side owned by the test. It is the served-run counterpart of
// runToolServer: where that helper drives Handle in process, this one runs
// Serve so a run can put its own request -- the approval elicitation -- on the
// same stream, and the test can answer it or refuse to.
type servedRunServe struct {
	t           *testing.T
	model       *openAISSEStub
	reader      *bufio.Reader
	writer      *bufio.Writer
	clientWrite *io.PipeWriter
	done        chan error
}

// startServedServer runs one server over pipes and returns the client side. The
// server's input is closed on cleanup, so Serve returns even when a test fails
// before it does; the wait turns a broken shutdown into a failure rather than a
// hung test binary.
func startServedServer(t *testing.T, server *mcp.Server) *servedRunServe {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	session := &servedRunServe{
		t:           t,
		reader:      bufio.NewReader(clientRead),
		writer:      bufio.NewWriter(clientWrite),
		clientWrite: clientWrite,
		done:        make(chan error, 1),
	}
	go func() { session.done <- server.Serve(context.Background(), serverRead, serverWrite) }()
	t.Cleanup(func() {
		_ = clientWrite.Close()
		_ = serverWrite.Close()
		select {
		case <-session.done:
		case <-time.After(10 * time.Second):
			t.Errorf("the served run server did not stop")
		}
	})
	return session
}

// startServedRunServe builds the run tool the subcommand would install and
// serves it over pipes, so the approval path under test is the one a remote
// caller reaches. The model stub's responses drive the run.
func startServedRunServe(t *testing.T, responses ...string) *servedRunServe {
	t.Helper()
	return startServedRunServeWithApproval(t, "prompt", responses...)
}

// startServedRunServeWithApproval is startServedRunServe with the operator's
// approval mode chosen by the test, so `never` can be pinned as unchanged.
func startServedRunServeWithApproval(t *testing.T, approve string, responses ...string) *servedRunServe {
	t.Helper()
	model := newOpenAISSEStub(t, responses...)
	opts := servedRunOptions(t, model.url)
	opts.approve = approve
	streams := servedRunStreams()
	runTool, err := newMCPRunTool(context.Background(), &opts, streams, time.Minute, newServedRunRegistry(context.Background()))
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	t.Cleanup(func() { drainClosers(&opts, streams) })
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools:   []mcp.ServerTool{runTool},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	session := startServedServer(t, server)
	session.model = model
	return session
}

// initialize performs the handshake, claiming the elicitation capability only
// when the test wants a client that can be asked.
func (s *servedRunServe) initialize(t *testing.T, advertiseElicitation bool) {
	t.Helper()
	capabilities := ""
	if advertiseElicitation {
		capabilities = `,"capabilities":{"elicitation":{}}`
	}
	response := exchange(t, s.writer, s.reader,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"`+capabilities+`}}`)
	if _, ok := response["result"]; !ok {
		t.Fatalf("initialize failed: %v", response)
	}
}

// initializeSampling is initialize for a client that may be asked to run the
// model call. Elicitation is claimed alongside it so a sampling session is
// still free to approve a tool: the two capabilities are independent, and a
// test should not have to choose between them.
func (s *servedRunServe) initializeSampling(t *testing.T, advertiseSampling bool) {
	t.Helper()
	capabilities := ""
	if advertiseSampling {
		capabilities = `,"capabilities":{"sampling":{},"elicitation":{}}`
	}
	response := exchange(t, s.writer, s.reader,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"`+capabilities+`}}`)
	if _, ok := response["result"]; !ok {
		t.Fatalf("initialize failed: %v", response)
	}
}

// callRun starts one served run whose model will issue an approval-gated shell
// call.
func (s *servedRunServe) callRun(t *testing.T) {
	t.Helper()
	s.write(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+mcpRunToolName+`","arguments":{"prompt":"run the shell tool"}}}`)
}

// elicitation reads the run's approval question off the stream and returns its
// id and params, so a test can inspect the message and answer it.
func (s *servedRunServe) elicitation(t *testing.T) (string, map[string]any) {
	t.Helper()
	request := readFrameWithin(t, s.reader, 10*time.Second)
	if request["method"] != "elicitation/create" {
		t.Fatalf("the frame was not an elicitation: %v", request)
	}
	id, _ := request["id"].(string)
	if id == "" {
		t.Fatalf("the elicitation has no string id: %v", request)
	}
	params, _ := request["params"].(map[string]any)
	return id, params
}

// answer writes the client's answer to one server-initiated request.
func (s *servedRunServe) answer(t *testing.T, id, result string) {
	t.Helper()
	s.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":%s}`, id, result))
}

// answerError writes a JSON-RPC error for one server-initiated request, which
// is how a client that cannot serve the method refuses it.
func (s *servedRunServe) answerError(t *testing.T, id string, code int, message string) {
	t.Helper()
	s.write(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"error":{"code":%d,"message":%q}}`, id, code, message))
}

func (s *servedRunServe) write(t *testing.T, frame string) {
	t.Helper()
	if _, err := s.writer.WriteString(frame + "\n"); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if err := s.writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
}

// result reads the tools/call response and decodes its result.
func (s *servedRunServe) result(t *testing.T) mcp.CallResult {
	t.Helper()
	response := readFrameWithin(t, s.reader, 10*time.Second)
	if response["id"] != float64(7) {
		t.Fatalf("the frame was not the run response: %v", response)
	}
	if errValue, ok := response["error"]; ok {
		t.Fatalf("the run call was rejected: %v", errValue)
	}
	encoded, err := json.Marshal(response["result"])
	if err != nil {
		t.Fatalf("the result could not be re-encoded: %v", err)
	}
	var result mcp.CallResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("the result did not decode: %v (%s)", err, encoded)
	}
	return result
}

// readFrameWithin reads one frame the server wrote, failing instead of blocking
// forever when the server goes quiet.
func readFrameWithin(t *testing.T, reader *bufio.Reader, timeout time.Duration) map[string]any {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- line
	}()
	select {
	case line := <-lines:
		if line == "" {
			t.Fatal("the server closed its stream before answering")
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("the frame is not JSON (%v): %s", err, line)
		}
		return decoded
	case <-time.After(timeout):
		t.Fatal("the server wrote no frame")
		return nil
	}
}

// refusedToolCalls decodes the refused names out of a projected result. The
// value has crossed the wire, so it is []any rather than the []string the
// in-process tests see.
func refusedToolCalls(t *testing.T, result mcp.CallResult) []string {
	t.Helper()
	raw, ok := result.StructuredContent["refusedToolCalls"].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(raw))
	for _, entry := range raw {
		name, _ := entry.(string)
		names = append(names, name)
	}
	return names
}

// servedRunShellCall is the model turn every elicitation test starts from: one
// shell call that needs approval. The command assembles its marker at run time
// ("served-run-%s" plus "ran"), so finding "served-run-ran" in a later model
// request means the shell really ran rather than that its arguments were merely
// replayed.
func servedRunShellCall() string {
	return toolCallChunk("call_shell", "shell", `{"command":"printf 'served-run-%s' ran","description":"print a marker"}`)
}

// TestMCPRunElicitsApprovalFromTheClient pins the approval path's new first
// step: when the client advertised elicitation, the run asks it -- naming the
// tool and the harness's reason -- and the tool call really runs when the
// client approves.
func TestMCPRunElicitsApprovalFromTheClient(t *testing.T) {
	session := startServedRunServe(t, servedRunShellCall(), textChunk("the served run finished"))
	session.initialize(t, true)
	session.callRun(t)

	id, params := session.elicitation(t)
	message, _ := params["message"].(string)
	if !strings.Contains(message, "shell") {
		t.Fatalf("the elicitation did not name the tool: %q", message)
	}
	if !strings.Contains(message, "print a marker") {
		t.Fatalf("the elicitation dropped the harness reason: %q", message)
	}
	schema, _ := params["requestedSchema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	approve, _ := properties["approve"].(map[string]any)
	if approve["type"] != "boolean" {
		t.Fatalf("the requested schema = %#v", params["requestedSchema"])
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "approve" {
		t.Fatalf("the requested schema does not require approve: %#v", schema)
	}

	session.answer(t, id, `{"action":"accept","content":{"approve":true}}`)
	result := session.result(t)
	if result.IsError {
		t.Fatalf("an approved run reported an error: %#v", result.Content)
	}
	if names := refusedToolCalls(t, result); len(names) != 0 {
		t.Fatalf("an approved call was still reported refused: %#v", result.StructuredContent)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "the served run finished") {
		t.Fatalf("text = %#v", result.Content)
	}
	if !strings.Contains(session.model.body(), "served-run-ran") {
		t.Fatalf("the approved tool never ran: %s", session.model.body())
	}
}

// TestMCPRunDeniesWhenTheClientDeclines pins the client's own "no": the call is
// denied, the run says the client declined rather than that the server could
// not ask, and the tool never runs.
func TestMCPRunDeniesWhenTheClientDeclines(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		want   string
	}{
		{"decline", `{"action":"decline"}`, "the MCP client declined the approval request"},
		{"cancel", `{"action":"cancel"}`, "the MCP client declined the approval request"},
		{"approve-false", `{"action":"accept","content":{"approve":false}}`, "the MCP client's operator declined the approval request"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session := startServedRunServe(t, servedRunShellCall(), textChunk("the served run finished"))
			session.initialize(t, true)
			session.callRun(t)
			id, _ := session.elicitation(t)
			session.answer(t, id, testCase.answer)

			result := session.result(t)
			if result.IsError {
				t.Fatalf("a denied tool call failed the whole run: %#v", result.Content)
			}
			names := refusedToolCalls(t, result)
			if len(names) != 1 || names[0] != "shell" {
				t.Fatalf("refusedToolCalls = %#v", result.StructuredContent["refusedToolCalls"])
			}
			if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, testCase.want) {
				t.Fatalf("text = %#v, want %q", result.Content, testCase.want)
			}
			// A client that answered must not be told it could not be asked.
			if strings.Contains(result.Content[0].Text, "cannot ask a human") {
				t.Fatalf("a client decision was reported as a server limitation: %q", result.Content[0].Text)
			}
			if strings.Contains(session.model.body(), "served-run-ran") {
				t.Fatalf("a denied tool call ran: %s", session.model.body())
			}
		})
	}
}

// TestMCPRunTreatsANonBooleanApprovalAsADenial pins the conservative reading of
// an unreadable answer: only the boolean true is consent, so a string or a
// missing field denies the call instead of being treated as truthy.
func TestMCPRunTreatsANonBooleanApprovalAsADenial(t *testing.T) {
	cases := []struct {
		name   string
		answer string
	}{
		{"string", `{"action":"accept","content":{"approve":"true"}}`},
		{"missing", `{"action":"accept","content":{}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session := startServedRunServe(t, servedRunShellCall(), textChunk("the served run finished"))
			session.initialize(t, true)
			session.callRun(t)
			id, _ := session.elicitation(t)
			session.answer(t, id, testCase.answer)

			result := session.result(t)
			if result.IsError {
				t.Fatalf("a denied tool call failed the whole run: %#v", result.Content)
			}
			names := refusedToolCalls(t, result)
			if len(names) != 1 || names[0] != "shell" {
				t.Fatalf("refusedToolCalls = %#v", result.StructuredContent["refusedToolCalls"])
			}
			if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "not a boolean") {
				t.Fatalf("the denial did not explain the unreadable answer: %#v", result.Content)
			}
			if strings.Contains(session.model.body(), "served-run-ran") {
				t.Fatalf("a non-boolean approval was treated as consent: %s", session.model.body())
			}
		})
	}
}

// TestMCPRunRefusesWithoutElicitationUsingTheOldText pins the fallback a client
// that never advertised elicitation gets: exactly the sentence served runs
// produced before elicitation existed, with no elicitation frame on the wire
// (the next frame after the call is its response).
func TestMCPRunRefusesWithoutElicitationUsingTheOldText(t *testing.T) {
	const preElicitationRefusal = "this MCP server serves runs without an operator, so it cannot prompt for approval; start it with --approve always to allow tools that need one"

	session := startServedRunServe(t, servedRunShellCall(), textChunk("the served run finished"))
	session.initialize(t, false)
	session.callRun(t)

	result := session.result(t)
	if result.IsError {
		t.Fatalf("a refused tool call failed the whole run: %#v", result.Content)
	}
	names := refusedToolCalls(t, result)
	if len(names) != 1 || names[0] != "shell" {
		t.Fatalf("refusedToolCalls = %#v", result.StructuredContent["refusedToolCalls"])
	}
	want := "the served run finished\n\n1 tool call(s) were refused because this server cannot ask a human to approve them: shell (" + preElicitationRefusal + ")"
	if len(result.Content) == 0 || result.Content[0].Text != want {
		t.Fatalf("text = %#v, want %q", result.Content, want)
	}
}

// TestMCPRunFallsBackWhenTheElicitationStreamEnds pins that a client which
// disappears mid-question is not waited on forever: the end of the stream wakes
// the elicitation, the refusal resolves the call, and the run still returns its
// result rather than hanging or writing after close.
func TestMCPRunFallsBackWhenTheElicitationStreamEnds(t *testing.T) {
	session := startServedRunServe(t, servedRunShellCall(), textChunk("the served run finished"))
	session.initialize(t, true)
	session.callRun(t)
	session.elicitation(t)
	// The client goes away instead of answering: the input stream ends while
	// the elicitation is still in flight.
	if err := session.clientWrite.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	result := session.result(t)
	if result.IsError {
		t.Fatalf("the fallback refusal failed the whole run: %#v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "cannot ask a human") {
		t.Fatalf("the fallback refusal was not used: %#v", result.Content)
	}
	names := refusedToolCalls(t, result)
	if len(names) != 1 || names[0] != "shell" {
		t.Fatalf("refusedToolCalls = %#v", result.StructuredContent["refusedToolCalls"])
	}
}

// TestMCPRunFallsBackWhenTheClientCannotAnswer pins that an elicitation error --
// here a client that answers with a JSON-RPC error rather than a decision -- is
// not a decision: the run falls back to the refusal it used before elicitation
// existed.
func TestMCPRunFallsBackWhenTheClientCannotAnswer(t *testing.T) {
	session := startServedRunServe(t, servedRunShellCall(), textChunk("the served run finished"))
	session.initialize(t, true)
	session.callRun(t)
	id, _ := session.elicitation(t)
	session.answerError(t, id, -32601, "elicitation is not supported")

	result := session.result(t)
	if result.IsError {
		t.Fatalf("the fallback refusal failed the whole run: %#v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "cannot ask a human") {
		t.Fatalf("the fallback refusal was not used: %#v", result.Content)
	}
	names := refusedToolCalls(t, result)
	if len(names) != 1 || names[0] != "shell" {
		t.Fatalf("refusedToolCalls = %#v", result.StructuredContent["refusedToolCalls"])
	}
}

// TestMCPRunNeverModeDoesNotElicit pins that `--approve never` is unchanged:
// the operator's mode still governs -- a command the mode blocks is blocked by
// the shell policy, not resolved by asking the client -- and the client is not
// consulted even though it advertised that it could answer.
func TestMCPRunNeverModeDoesNotElicit(t *testing.T) {
	session := startServedRunServeWithApproval(t, "never", servedRunShellCall(), textChunk("the served run finished"))
	session.initialize(t, true)
	session.callRun(t)

	// No elicitation is sent, so the run's own response is the next frame; an
	// elicitation arriving first would fail the id check with "srv-1".
	result := session.result(t)
	if result.IsError {
		t.Fatalf("the run reported an error: %#v", result.Content)
	}
	if names := refusedToolCalls(t, result); len(names) != 0 {
		t.Fatalf("never mode resolved a call through the approval path: %#v", result.StructuredContent)
	}
	if !strings.Contains(session.model.body(), "command blocked") {
		t.Fatalf("never mode did not block the command through the policy: %s", session.model.body())
	}
}

// servedRunApprovalRequest is one valid approval request, as a tool would make
// it, for tests that exercise the approval broker without a run behind it.
func servedRunApprovalRequest() approval.Request {
	return approval.Request{
		ID:        "approval_test",
		RunID:     "run_test",
		ToolName:  "shell",
		Operation: "shell.command",
		Title:     "Approve shell command",
		Risk:      approval.RiskHigh,
		Options:   approval.DefaultOptions(),
		CreatedAt: time.Now().UTC(),
	}
}

// TestMCPRunFallsBackWhenTheElicitationTimesOut pins the timeout's fallback: a
// client that is asked and never answers must not hold the run. The recorder's
// own bound is shortened so the test does not wait out the five-minute default;
// firing while the run's context is still alive, it denies the call with the
// same refusal a client that cannot answer gets.
func TestMCPRunFallsBackWhenTheElicitationTimesOut(t *testing.T) {
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []mcp.ServerTool{{
			Name: "probe",
			Handler: func(ctx context.Context, arguments json.RawMessage) (mcp.CallResult, error) {
				return mcp.CallResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	session := startServedServer(t, server)
	session.initialize(t, true)

	const fallback = "the server could not ask"
	recorder := &runApprovalRecorder{reason: fallback, elicit: true, elicitTimeout: 20 * time.Millisecond}
	recorder.beginRun(server)

	type outcome struct {
		decision approval.Decision
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		decision, err := recorder.Request(context.Background(), servedRunApprovalRequest())
		done <- outcome{decision: decision, err: err}
	}()

	// The client is asked and then never answers.
	request := readFrameWithin(t, session.reader, 5*time.Second)
	if request["method"] != "elicitation/create" {
		t.Fatalf("the frame was not an elicitation: %v", request)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("the timeout returned an error instead of the refusal: %v", got.err)
		}
		if got.decision.Action != approval.DecisionReject || got.decision.Reason != fallback {
			t.Fatalf("decision = %#v, want a rejection carrying the fallback reason", got.decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the elicitation timeout never fell back to the refusal")
	}
	if names := recorder.refused(); len(names) != 1 || names[0] != "shell" {
		t.Fatalf("refused = %#v", names)
	}
}

// startServedRunServeWithSampling builds a served run tool with the operator's
// --sampling opt-in and serves it over pipes. No model endpoint is configured:
// the point of the flag is that the run's model call is delegated to the
// connected client, so an attempted fall back to a local provider would fail
// with a connection error rather than quietly succeed.
func startServedRunServeWithSampling(t *testing.T) *servedRunServe {
	t.Helper()
	opts := servedRunOptions(t, "http://127.0.0.1:1")
	opts.sampling = true
	streams := servedRunStreams()
	runTool, err := newMCPRunTool(context.Background(), &opts, streams, time.Minute, newServedRunRegistry(context.Background()))
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	t.Cleanup(func() { drainClosers(&opts, streams) })
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools:   []mcp.ServerTool{runTool},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return startServedServer(t, server)
}

// runTurnSampling returns the sampling request a served run's model call put
// on the wire, so a test can assert what the client was actually asked.
func (s *servedRunServe) runTurnSampling(t *testing.T) (string, map[string]any) {
	t.Helper()
	request := readFrameWithin(t, s.reader, 10*time.Second)
	if request["method"] != "sampling/createMessage" {
		t.Fatalf("the frame was not a sampling request: %v", request)
	}
	id, _ := request["id"].(string)
	if id == "" {
		t.Fatalf("the sampling request has no string id: %v", request)
	}
	params, _ := request["params"].(map[string]any)
	return id, params
}

// TestServedRunUsesTheClientsModelWhenSamplingIsEnabled pins the opt-in end to
// end: with --sampling and a capable client, the run's model call is a
// sampling/createMessage carrying the run's turn, and the client's answer is
// the run's result. The configured endpoint is unreachable, so a run that fell
// back to a local model could not have completed.
func TestServedRunUsesTheClientsModelWhenSamplingIsEnabled(t *testing.T) {
	session := startServedRunServeWithSampling(t)
	session.initializeSampling(t, true)
	session.callRun(t)

	id, params := session.runTurnSampling(t)
	messages, _ := params["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("the sampling request carried no messages: %#v", params)
	}
	found := false
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].(map[string]any)
		if strings.Contains(fmt.Sprint(content["text"]), "run the shell tool") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the sampling request did not carry the run's turn: %#v", messages)
	}
	// The system prompt travels outside the message list, which is the
	// protocol's shape for it.
	if prompt, _ := params["systemPrompt"].(string); prompt == "" {
		t.Fatalf("the sampling request dropped the system prompt: %#v", params)
	}

	session.answer(t, id, `{"role":"assistant","content":{"type":"text","text":"the client model answered"},"model":"client-model"}`)
	result := session.result(t)
	if result.IsError {
		t.Fatalf("the sampled run reported an error: %#v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "the client model answered") {
		t.Fatalf("text = %#v", result.Content)
	}
	if result.StructuredContent["status"] != "completed" {
		t.Fatalf("status = %#v", result.StructuredContent["status"])
	}
}

// TestServedRunFailsWhenTheClientCannotSample pins the refusal the operator
// asked for: --sampling with a client that never advertised the capability is
// a failed run carrying both ways out, not a silent fall back to a local model
// and not a refusal with no reason.
func TestServedRunFailsWhenTheClientCannotSample(t *testing.T) {
	session := startServedRunServeWithSampling(t)
	session.initializeSampling(t, false)
	session.callRun(t)

	// The handler refuses before starting the run, so the next frame is the
	// run call's own response, not a sampling request.
	result := session.result(t)
	if !result.IsError {
		t.Fatalf("a run without a sampling-capable client was reported as a success: %#v", result.StructuredContent)
	}
	if len(result.Content) == 0 {
		t.Fatal("the refusal carried no explanation")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "--sampling") || !strings.Contains(text, "sampling capability") {
		t.Fatalf("the refusal does not name the flag and the missing capability: %q", text)
	}
}

// TestServedRunWithoutSamplingKeepsTheLocalModel pins the other half of the
// opt-in: a client that could sample is still not asked unless the operator
// passed --sampling, and the run is answered by the configured provider
// exactly as before the flag existed.
func TestServedRunWithoutSamplingKeepsTheLocalModel(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("the local model answered"))
	opts := servedRunOptions(t, model.url)
	streams := servedRunStreams()
	runTool, err := newMCPRunTool(context.Background(), &opts, streams, time.Minute, newServedRunRegistry(context.Background()))
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	t.Cleanup(func() { drainClosers(&opts, streams) })
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools:   []mcp.ServerTool{runTool},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	session := startServedServer(t, server)
	session.initializeSampling(t, true)
	session.callRun(t)

	// Any sampling frame would arrive before the run's response and would
	// fail this read's id check, so the local answer is also the proof that no
	// sampling request was sent.
	result := session.result(t)
	if result.IsError {
		t.Fatalf("the local run reported an error: %#v", result.Content)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "the local model answered") {
		t.Fatalf("text = %#v", result.Content)
	}
	if !strings.Contains(model.body(), "run the shell tool") {
		t.Fatalf("the local model never saw the run's turn: %s", model.body())
	}
}

// TestMCPServerSamplingRequiresTheRunGrant pins the flag combination at the
// command boundary: --sampling without --allow-run would start a server on
// which it could never take effect, so it is refused with the reason instead
// of being accepted and ignored.
func TestMCPServerSamplingRequiresTheRunGrant(t *testing.T) {
	err := mcpServerCommand(context.Background(), []string{"--sampling", "--checkpoint-dir", t.TempDir()}, IO{
		Stdin:  strings.NewReader(""),
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("--sampling without --allow-run was accepted")
	}
	if !strings.Contains(err.Error(), "--sampling requires --allow-run") {
		t.Fatalf("error = %v", err)
	}
}

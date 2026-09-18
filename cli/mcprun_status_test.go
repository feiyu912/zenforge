package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
)

// gatedModel answers one call with an SSE body as soon as release is closed,
// so a test can hold a run inside its model call and look at it while it runs.
func gatedModel(t *testing.T, release <-chan struct{}, answer string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, textChunk(answer))
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForStatus(t *testing.T, status mcp.ServerTool, runID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		result, err := status.Handler(context.Background(), json.RawMessage(`{"runId":"`+runID+`"}`))
		if err != nil {
			t.Fatalf("the status handler returned error: %v", err)
		}
		got, _ := result.StructuredContent["status"].(string)
		if got == want {
			return result.StructuredContent
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s stayed %q, want %q (text %q)", runID, got, want, result.Content[0].Text)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDetachedServedRunReportsRunningThenItsAnswer(t *testing.T) {
	release := make(chan struct{})
	model := gatedModel(t, release, "detached answer")
	opts := servedRunOptions(t, model.URL)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	status := newMCPRunStatusTool(context.Background(), opts.checkpointType, opts.checkpointDir, registry)

	// The caller is told which run it now owns, before the run can finish.
	started, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello","detach":true}`))
	if err != nil {
		t.Fatalf("the detached handler returned error: %v", err)
	}
	runID, _ := started.StructuredContent["runId"].(string)
	if runID == "" || started.StructuredContent["status"] != servedRunRunning || started.StructuredContent["detached"] != true {
		t.Fatalf("detached start = %#v; text=%q", started.StructuredContent, started.Content[0].Text)
	}
	if started.IsError {
		t.Fatalf("a started run was reported as an error: %#v", started)
	}
	running := waitForStatus(t, status, runID, servedRunRunning)
	if running["startedAt"] == nil {
		t.Fatalf("a running report has no start time: %#v", running)
	}

	close(release)
	finished := waitForStatus(t, status, runID, servedRunCompleted)
	if finished["output"] != "detached answer" {
		t.Fatalf("finished = %#v", finished)
	}
	if finished["detached"] != true || finished["finishedAt"] == nil {
		t.Fatalf("finished = %#v", finished)
	}
}

// TestDetachedRunOutlivesTheCallThatAskedForIt is the point of detaching: the
// request that started the run is over (and cancelled) while the run keeps
// working, because a detached run is bound to the server, not to the call.
func TestDetachedRunOutlivesTheCallThatAskedForIt(t *testing.T) {
	release := make(chan struct{})
	model := gatedModel(t, release, "survived")
	opts := servedRunOptions(t, model.URL)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	status := newMCPRunStatusTool(context.Background(), opts.checkpointType, opts.checkpointDir, registry)

	callCtx, cancelCall := context.WithCancel(context.Background())
	started, err := runTool.Handler(callCtx, json.RawMessage(`{"prompt":"say hello","detach":true}`))
	if err != nil {
		t.Fatalf("the detached handler returned error: %v", err)
	}
	runID, _ := started.StructuredContent["runId"].(string)
	// The call is finished and its context is cancelled, which is what an MCP
	// server does when a request is answered.
	cancelCall()
	close(release)
	finished := waitForStatus(t, status, runID, servedRunCompleted)
	if finished["output"] != "survived" {
		t.Fatalf("the run did not survive its call: %#v", finished)
	}
}

func TestServedRunStatusReportsDurableRunsAndUnknownIDs(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("recorded answer"))
	opts := servedRunOptions(t, model.url)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	blocking, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello"}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	runID, _ := blocking.StructuredContent["runId"].(string)
	if runID == "" {
		t.Fatalf("the blocking run returned no id: %#v", blocking.StructuredContent)
	}

	// A server that did not run it falls back to the durable record, which is
	// what makes the tool useful for work this process never saw.
	other := newMCPRunStatusTool(context.Background(), opts.checkpointType, opts.checkpointDir, newServedRunRegistry(context.Background()))
	result, err := other.Handler(context.Background(), json.RawMessage(`{"runId":"`+runID+`"}`))
	if err != nil {
		t.Fatalf("the status handler returned error: %v", err)
	}
	if result.StructuredContent["recorded"] != true || result.StructuredContent["runId"] != runID {
		t.Fatalf("durable report = %#v", result.StructuredContent)
	}
	if !strings.Contains(result.Content[0].Text, "did not run it") {
		t.Fatalf("text = %q", result.Content[0].Text)
	}

	missing, err := other.Handler(context.Background(), json.RawMessage(`{"runId":"run_does_not_exist"}`))
	if err != nil {
		t.Fatalf("an unknown id is a fact, not a handler error: %v", err)
	}
	if missing.StructuredContent["status"] != servedRunUnknown || missing.IsError {
		t.Fatalf("unknown report = %#v (error %v)", missing.StructuredContent, missing.IsError)
	}
	if _, err := other.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a missing runId was accepted")
	}
}

func TestServedRunRegistryBoundsItsMemoryAndNeverDropsALiveRun(t *testing.T) {
	registry := newServedRunRegistry(context.Background())
	// One live run, then terminal records past the cap.
	if err := registry.start("run_live", false, func() {}); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	for index := 0; index < maxServedRunRecords+10; index++ {
		runID := fmt.Sprintf("run_%d", index)
		if err := registry.start(runID, false, func() {}); err != nil {
			t.Fatalf("start returned error: %v", err)
		}
		registry.finish(servedRunState{RunID: runID, Status: servedRunCompleted})
	}
	if _, ok := registry.get("run_live"); !ok {
		t.Fatal("a live run was evicted")
	}
	if _, ok := registry.get("run_0"); ok {
		t.Fatal("the oldest terminal record was not evicted")
	}
	if _, ok := registry.get(fmt.Sprintf("run_%d", maxServedRunRecords+9)); !ok {
		t.Fatal("the newest terminal record was evicted")
	}
}

func TestServedRunRegistryShutdownCancelsAndWaits(t *testing.T) {
	registry := newServedRunRegistry(context.Background())
	cancelled := make(chan struct{})
	if err := registry.start("run_a", true, func() { close(cancelled) }); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	// The run settles when it is cancelled, which is what a real run does.
	go func() {
		<-cancelled
		registry.finish(servedRunState{RunID: "run_a", Status: "cancelled"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry.shutdown(ctx)
	select {
	case <-cancelled:
	default:
		t.Fatal("shutdown did not cancel a live run")
	}
	state, ok := registry.get("run_a")
	if !ok || state.Status != "cancelled" {
		t.Fatalf("state after shutdown = %#v", state)
	}
}

func TestServedRunStatusToolIsReadOnlyAndTheRunToolOffersDetach(t *testing.T) {
	opts := servedRunOptions(t, "http://127.0.0.1:1")
	registry := newServedRunRegistry(context.Background())
	status := newMCPRunStatusTool(context.Background(), opts.checkpointType, opts.checkpointDir, registry)
	if status.Name != servedRunStatusName || !status.ReadOnly {
		t.Fatalf("status tool = %#v", status)
	}
	properties, _ := status.InputSchema["properties"].(map[string]any)
	if _, ok := properties["runId"]; !ok {
		t.Fatalf("status schema = %#v", status.InputSchema)
	}
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	runProperties, _ := runTool.InputSchema["properties"].(map[string]any)
	if _, ok := runProperties["detach"]; !ok {
		t.Fatalf("the run tool does not offer detach: %#v", runTool.InputSchema)
	}
	if runTool.ReadOnly {
		t.Fatal("the run tool is advertised read-only")
	}
}

func TestServedRunCancelStopsADetachedRun(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	// The model holds the run inside its call until the run is cancelled; the
	// gate honours the request context, which is what a cancellation reaches.
	model := gatedModel(t, release, "never answered")
	opts := servedRunOptions(t, model.URL)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	status := newMCPRunStatusTool(context.Background(), opts.checkpointType, opts.checkpointDir, registry)
	cancelTool := newMCPRunCancelTool(opts.checkpointType, opts.checkpointDir, registry)

	started, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello","detach":true}`))
	if err != nil {
		t.Fatalf("the detached handler returned error: %v", err)
	}
	runID, _ := started.StructuredContent["runId"].(string)
	waitForStatus(t, status, runID, servedRunRunning)

	stopped, err := cancelTool.Handler(context.Background(), json.RawMessage(`{"runId":"`+runID+`"}`))
	if err != nil {
		t.Fatalf("the cancel handler returned error: %v", err)
	}
	if stopped.StructuredContent["cancelled"] != true {
		t.Fatalf("cancel = %#v", stopped.StructuredContent)
	}
	// The run keeps unwinding after the request, so the status is polled; it
	// must reach the terminal "cancelled", not stay running and not be
	// reported as a failure.
	final := waitForStatus(t, status, runID, "cancelled")
	// A cancelled run carries the explanation, not an answer.
	if final["message"] == nil || final["output"] != nil {
		t.Fatalf("cancelled report = %#v", final)
	}
}

func TestServedRunCancelReportsAnAlreadyFinishedRun(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("done"))
	opts := servedRunOptions(t, model.url)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	cancelTool := newMCPRunCancelTool(opts.checkpointType, opts.checkpointDir, registry)

	finished, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello"}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	runID, _ := finished.StructuredContent["runId"].(string)
	result, err := cancelTool.Handler(context.Background(), json.RawMessage(`{"runId":"`+runID+`"}`))
	if err != nil {
		t.Fatalf("cancelling a finished run returned error: %v", err)
	}
	// The caller's intent — the work is stopped — is satisfied, so this is a
	// fact with a status, not a failure.
	if result.StructuredContent["cancelled"] != false || result.StructuredContent["alreadyFinished"] != true ||
		result.StructuredContent["status"] != servedRunCompleted {
		t.Fatalf("cancel of a finished run = %#v", result.StructuredContent)
	}
	if result.IsError {
		t.Fatalf("a finished run was reported as a cancel failure: %#v", result)
	}
}

func TestServedRunCancelReportsWhatItCannotStop(t *testing.T) {
	model := newOpenAISSEStub(t, textChunk("recorded"))
	opts := servedRunOptions(t, model.url)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	defer drainClosers(&opts, servedRunStreams())
	if _, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello"}`)); err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	// Another process's server: it has the durable record but none of the
	// live state, so it can say what it cannot do.
	other := newMCPRunCancelTool(opts.checkpointType, opts.checkpointDir, newServedRunRegistry(context.Background()))
	summaries, closeStore, err := listRuns(context.Background(), opts.checkpointType, opts.checkpointDir)
	if err != nil || len(summaries) == 0 {
		t.Fatalf("listRuns = %#v, %v", summaries, err)
	}
	defer func() { _ = closeStore() }()
	result, err := other.Handler(context.Background(), json.RawMessage(`{"runId":"`+summaries[0].RunID+`"}`))
	if err != nil {
		t.Fatalf("the cancel handler returned error: %v", err)
	}
	if result.StructuredContent["cancelled"] != false || result.StructuredContent["recorded"] != true {
		t.Fatalf("cancel of another process's run = %#v", result.StructuredContent)
	}
	unknown, err := other.Handler(context.Background(), json.RawMessage(`{"runId":"run_missing"}`))
	if err != nil {
		t.Fatalf("an unknown id should be a fact, not a handler error: %v", err)
	}
	if unknown.StructuredContent["status"] != servedRunUnknown {
		t.Fatalf("unknown cancel = %#v", unknown.StructuredContent)
	}
	if _, err := other.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a missing runId was accepted")
	}
}

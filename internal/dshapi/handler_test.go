package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// stubRun is one agent stream under test control. The mutex keeps emit and
// close from racing when a cancellation and a finish land together.
type stubRun struct {
	mu     sync.Mutex
	ch     chan zenforge.Event
	closed bool
}

func (r *stubRun) emit(event zenforge.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.ch <- event
}

func (r *stubRun) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.ch)
}

// stubAgent is a deterministic stand-in for zenforge.Agent. It persists the
// events it emits, so the run manager, the durable store, and the handler are
// exercised together without a model provider.
type stubAgent struct {
	store eventlog.Store

	mu     sync.Mutex
	runs   map[string]*stubRun
	steers []string
	// tasks records every task the manager started through this agent, in
	// order, so a test can assert what a continuation run was actually handed
	// (its run id, its input, and the messages it carries).
	tasks []zenforge.Task
}

func newStubAgent(store eventlog.Store) *stubAgent {
	return &stubAgent{store: store, runs: make(map[string]*stubRun)}
}

func (a *stubAgent) append(runID string, eventType zenforge.EventType, data map[string]any) {
	_ = a.store.Append(context.Background(), zenforge.NewEvent(eventType, runID, data))
}

func (a *stubAgent) Stream(ctx context.Context, task zenforge.Task) (<-chan zenforge.Event, error) {
	run := &stubRun{ch: make(chan zenforge.Event, 8)}
	a.mu.Lock()
	a.runs[task.RunID] = run
	a.tasks = append(a.tasks, task)
	a.mu.Unlock()
	a.append(task.RunID, zenforge.EventRunStarted, map[string]any{"input": task.Input})
	run.emit(zenforge.NewEvent(zenforge.EventRunStarted, task.RunID, map[string]any{"input": task.Input}))
	go func() {
		<-ctx.Done()
		run.close()
	}()
	return run.ch, nil
}

func (a *stubAgent) Resume(ctx context.Context, runID string) (<-chan zenforge.Event, error) {
	return a.Stream(ctx, zenforge.Task{RunID: runID})
}

// Steer matches harnesshttp's steeringAgent shape so RunManager.Steer can use
// this agent, and records the message for assertions.
func (a *stubAgent) Steer(runID, steerID, message string) (harness.SteerState, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.runs[runID]; !ok {
		return harness.SteerState{}, false
	}
	a.steers = append(a.steers, message)
	return harness.SteerState{ID: steerID, Message: message, CreatedAt: time.Now().UTC()}, true
}

func (a *stubAgent) finish(runID string) {
	a.mu.Lock()
	run := a.runs[runID]
	a.mu.Unlock()
	if run == nil {
		return
	}
	a.append(runID, zenforge.EventRunDone, nil)
	run.emit(zenforge.NewEvent(zenforge.EventRunDone, runID, nil))
	run.close()
}

func (a *stubAgent) steered() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.steers...)
}

type fixture struct {
	handler *Handler
	manager *harnesshttp.RunManager
	agent   *stubAgent
	store   eventlog.Store
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	return newFixtureWithStore(t, cfg, memory.New())
}

// newDurableFixture roots a real jsonl store under t.TempDir(). The default
// event directory is never used: a test that writes there leaks run files into
// the checkout.
func newDurableFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	return newFixtureWithStore(t, cfg, jsonl.New(t.TempDir()))
}

func newFixtureWithStore(t *testing.T, cfg Config, store eventlog.Store) *fixture {
	t.Helper()
	bus := eventlog.NewBus()
	agent := newStubAgent(store)
	manager := harnesshttp.NewRunManager(agent, store, bus, harnesshttp.RunManagerOptions{
		TerminalRetention: -1,
	})
	handler, err := New(manager, store, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return &fixture{handler: handler, manager: manager, agent: agent, store: store}
}

func (f *fixture) post(t *testing.T, endpoint, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, http.MethodPost, endpoint, body, nil)
}

func (f *fixture) request(
	t *testing.T,
	method, endpoint, body string,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, endpoint, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:3080"
	if mutate != nil {
		mutate(req)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %v: %v", value, err)
	}
	return string(encoded)
}

// rpcBody builds a client-request envelope. args is the args object, or "" for
// an empty one.
func rpcBody(t *testing.T, rpcID, method, args string) string {
	t.Helper()
	if args == "" {
		args = "{}"
	}
	return fmt.Sprintf(`{"type":"client-request","rpcId":%s,"method":%s,"payload":{"args":%s}}`,
		mustJSON(t, rpcID), mustJSON(t, method), args)
}

type responseEnvelope struct {
	Type   string `json:"type"`
	RPCID  string `json:"rpcId"`
	Result struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error *responseError  `json:"error"`
	} `json:"result"`
}

type responseError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) responseEnvelope {
	t.Helper()
	var envelope responseEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return envelope
}

func decodeValue(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	envelope := decodeResponse(t, recorder)
	if !envelope.Result.OK {
		t.Fatalf("result not ok: %s", recorder.Body.String())
	}
	if err := json.Unmarshal(envelope.Result.Value, target); err != nil {
		t.Fatalf("decode value %q: %v", string(envelope.Result.Value), err)
	}
}

func assertMethodFailure(t *testing.T, recorder *httptest.ResponseRecorder, code string) responseEnvelope {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("method-level failure status = %d, want 200 so the client parses the result envelope: %s",
			recorder.Code, recorder.Body.String())
	}
	envelope := decodeResponse(t, recorder)
	if envelope.Type != "server-response" {
		t.Fatalf("type = %q, want server-response", envelope.Type)
	}
	if envelope.Result.OK {
		t.Fatalf("result.ok = true, want a failure: %s", recorder.Body.String())
	}
	if envelope.Result.Error == nil {
		t.Fatalf("failure has no error object: %s", recorder.Body.String())
	}
	if envelope.Result.Error.Code != code {
		t.Fatalf("error code = %q, want %q", envelope.Result.Error.Code, code)
	}
	// The shipped client requires details to be a record; a missing field
	// would make it throw instead of returning the error to its caller.
	if envelope.Result.Error.Details == nil {
		t.Fatalf("failure details missing: %s", recorder.Body.String())
	}
	if envelope.Result.Error.Message == "" {
		t.Fatalf("failure message empty: %s", recorder.Body.String())
	}
	return envelope
}

func waitForStatus(t *testing.T, manager *harnesshttp.RunManager, runID string, want harnesshttp.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last harnesshttp.RunInfo
	for time.Now().Before(deadline) {
		info, err := manager.Get(runID)
		if err == nil {
			last = info
			if info.Status == want {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %q status = %q, want %q", runID, last.Status, want)
}

// task returns the task started for a run id, if this agent was handed one.
func (a *stubAgent) task(runID string) (zenforge.Task, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, task := range a.tasks {
		if task.RunID == runID {
			return task, true
		}
	}
	return zenforge.Task{}, false
}

// taskRunIDs lists the run ids this agent was asked to run, in order.
func (a *stubAgent) taskRunIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.tasks))
	for _, task := range a.tasks {
		ids = append(ids, task.RunID)
	}
	return ids
}

// jsonString encodes a Go string as a JSON string, for building request bodies
// that carry an id.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// decodeValueMap returns a successful response's value as raw JSON text, for a
// test that asserts on a page's content rather than its shape.
func decodeValueMap(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	envelope := decodeResponse(t, recorder)
	if !envelope.Result.OK {
		t.Fatalf("result not ok: %s", recorder.Body.String())
	}
	return string(envelope.Result.Value)
}

// createSession creates a pending session and returns its id.
func (f *fixture) createSession(t *testing.T) string {
	t.Helper()
	recorder := f.post(t, "/api/session/create", rpcBody(t, "rpc-create", "session/create", ""))
	var value struct {
		SessionID string `json:"sessionId"`
	}
	decodeValue(t, recorder, &value)
	if value.SessionID == "" {
		t.Fatalf("create returned no sessionId: %s", recorder.Body.String())
	}
	return value.SessionID
}

// startSession creates a session and starts its run with a text prompt.
func (f *fixture) startSession(t *testing.T) string {
	t.Helper()
	sessionID := f.createSession(t)
	recorder := f.post(t, "/api/session/prompt",
		rpcBody(t, "rpc-prompt", "session/prompt",
			fmt.Sprintf(`{"requestId":"req-1","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"hello"}]}`, mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt failed: %s", recorder.Body.String())
	}
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunRunning)
	return sessionID
}

func TestFenceRejectsCrossSite(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "cross-site") {
		t.Fatalf("403 body does not name the failed check: %s", recorder.Body.String())
	}
}

func TestFenceRejectsOriginMismatch(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Origin") {
		t.Fatalf("403 body does not name the failed check: %s", recorder.Body.String())
	}
}

func TestFenceRejectsNullOrigin(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.Header.Set("Origin", "null") })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a null origin: %s", recorder.Code, recorder.Body.String())
	}
}

func TestFenceRejectsNonLoopbackRemoteByDefault(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.RemoteAddr = "10.0.0.9:5000" })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "loopback") {
		t.Fatalf("403 body does not name the failed check: %s", recorder.Body.String())
	}
}

func TestFenceAllowsRemoteWhenConfigured(t *testing.T) {
	f := newFixture(t, Config{AllowRemote: true})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.RemoteAddr = "10.0.0.9:5000" })
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with AllowRemote: %s", recorder.Code, recorder.Body.String())
	}
}

func TestFenceAllowsSameOriginLoopback(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:3080") })
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

func TestFenceAllowsIPv6Loopback(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/session/list",
		rpcBody(t, "rpc-fence", "session/list", ""),
		func(r *http.Request) { r.RemoteAddr = "[::1]:54321" })
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

func TestFenceFencesBeforeRouting(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.request(t, http.MethodPost, "/api/unknown/method",
		rpcBody(t, "rpc-fence", "unknown/method", ""),
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 before the 404: %s", recorder.Code, recorder.Body.String())
	}
}

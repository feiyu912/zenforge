package dshstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/eventlog/jsonl"
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
	select {
	case r.ch <- event:
	default:
	}
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

// stubAgent is a deterministic stand-in for zenforge.Agent. It persists what
// it emits through the fixture's fan-out store, so the run manager, the
// durable log, and the bus are exercised together without a model provider.
type stubAgent struct {
	store eventlog.Store

	mu   sync.Mutex
	runs map[string]*stubRun
}

func newStubAgent(store eventlog.Store) *stubAgent {
	return &stubAgent{store: store, runs: make(map[string]*stubRun)}
}

func (a *stubAgent) Stream(ctx context.Context, task zenforge.Task) (<-chan zenforge.Event, error) {
	run := &stubRun{ch: make(chan zenforge.Event, 64)}
	a.mu.Lock()
	a.runs[task.RunID] = run
	a.mu.Unlock()
	a.emit(task.RunID, zenforge.EventRunStarted, map[string]any{"input": task.Input})
	go func() {
		<-ctx.Done()
		run.close()
	}()
	return run.ch, nil
}

func (a *stubAgent) Resume(ctx context.Context, runID string) (<-chan zenforge.Event, error) {
	return a.Stream(ctx, zenforge.Task{RunID: runID})
}

// Steer matches harnesshttp's steeringAgent shape so RunManager.Steer accepts
// this agent; the transport-layer tests never use it, but the interface stays
// satisfiable.
func (a *stubAgent) Steer(runID, steerID, message string) (harness.SteerState, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.runs[runID]; !ok {
		return harness.SteerState{}, false
	}
	return harness.SteerState{ID: steerID, Message: message, CreatedAt: time.Now().UTC()}, true
}

// emit appends one durable event and hands the same logical event to the run
// manager's drain. The durable copy receives the seq from the fan-out store;
// the in-memory copy only needs its type for status transitions.
func (a *stubAgent) emit(runID string, eventType zenforge.EventType, data map[string]any) {
	event := zenforge.NewEvent(eventType, runID, data)
	_ = a.store.Append(context.Background(), event)
	a.mu.Lock()
	run := a.runs[runID]
	a.mu.Unlock()
	if run != nil {
		run.emit(event)
	}
}

func (a *stubAgent) finish(runID string) {
	a.emit(runID, zenforge.EventRunDone, map[string]any{"output": "done"})
	a.mu.Lock()
	run := a.runs[runID]
	a.mu.Unlock()
	if run != nil {
		run.close()
	}
}

// fixture owns one real durable event log under t.TempDir(), one real run
// manager over it, and one httptest server serving the transport handler. The
// default event/checkpoint directory is never touched: a test that wrote there
// would leak run files into the checkout.
type fixture struct {
	handler *Handler
	manager *harnesshttp.RunManager
	agent   *stubAgent
	broker  *approval.PendingBroker
	store   eventlog.Store
	server  *httptest.Server
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	return newFixtureWithRemote(t, cfg, "")
}

// newFixtureWithRemote builds the same fixture but rewrites every accepted
// connection's RemoteAddr to remoteAddr, which is how the non-loopback fence is
// exercised against a real handshake rather than a synthesized request.
func newFixtureWithRemote(t *testing.T, cfg Config, remoteAddr string) *fixture {
	t.Helper()
	if cfg.Home == "" {
		cfg.Home = "/home/tester"
	}
	if cfg.ApprovalPollInterval == 0 {
		cfg.ApprovalPollInterval = 5 * time.Millisecond
	}
	store := jsonl.New(t.TempDir())
	bus := eventlog.NewBus()
	fanout := eventlog.NewFanoutStore(store, bus)
	agent := newStubAgent(fanout)
	broker := approval.NewPendingBroker(16)
	manager := harnesshttp.NewRunManager(agent, fanout, bus, harnesshttp.RunManagerOptions{
		TerminalRetention: -1,
		Follow:            eventlog.FollowOptions{PollInterval: 2 * time.Millisecond},
	})
	handler, err := New(manager, fanout, broker, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	if remoteAddr != "" {
		server.Listener = rewriteAddrListener{Listener: server.Listener, addr: remoteAddr}
	}
	server.Start()
	t.Cleanup(func() {
		server.Close()
		_ = manager.Close(context.Background())
	})
	return &fixture{
		handler: handler,
		manager: manager,
		agent:   agent,
		broker:  broker,
		store:   fanout,
		server:  server,
	}
}

// wsURL is the mux URL on this fixture's server.
func (f *fixture) wsURL() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + MuxPath
}

// host is the server authority, used to build a matching Origin header.
func (f *fixture) host() string {
	return strings.TrimPrefix(f.server.URL, "http://")
}

// dial opens one WebSocket. A non-nil mutate may set headers; a refused
// handshake returns the *http.Response for the caller to assert on.
func (f *fixture) dial(t *testing.T, mutate func(http.Header)) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if mutate != nil {
		mutate(header)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, response, err := dialer.Dial(f.wsURL(), header)
	return conn, response, err
}

// mustDial opens a WebSocket and fails the test when the handshake is refused.
func (f *fixture) mustDial(t *testing.T) *websocket.Conn {
	t.Helper()
	conn, _, err := f.dial(t, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", f.wsURL(), err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// startRun starts one detached run and waits until the manager reports it
// running, so the durable run.started event is already in the log.
func (f *fixture) startRun(t *testing.T, input string) string {
	t.Helper()
	runID := zenforge.NewRunID()
	if _, err := f.manager.Start(context.Background(), zenforge.Task{RunID: runID, Input: input}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitForStatus(t, f.manager, runID, harnesshttp.RunRunning)
	return runID
}

// postResult posts one client-request envelope to the result route.
func (f *fixture) postResult(t *testing.T, body string) *http.Response {
	t.Helper()
	response, err := http.Post(f.server.URL+EventsResultPath, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post result: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// openStream writes one client open frame.
func openStream(t *testing.T, conn *websocket.Conn, streamID, endpoint, args string) {
	t.Helper()
	if args == "" {
		args = "{}"
	}
	frame := fmt.Sprintf(`{"type":"open","streamId":%q,"endpoint":%q,"payload":{"args":%s}}`,
		streamID, endpoint, args)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write open %s: %v", endpoint, err)
	}
}

// cancelStream writes one client cancel frame.
func cancelStream(t *testing.T, conn *websocket.Conn, streamID string) {
	t.Helper()
	frame := fmt.Sprintf(`{"type":"cancel","streamId":%q}`, streamID)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write cancel %s: %v", streamID, err)
	}
}

// readFrame reads one text frame as a raw key/value object so a test can assert
// the exact key set the client enforces.
func readFrame(t *testing.T, conn *websocket.Conn) map[string]json.RawMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("message type = %d, want text", messageType)
	}
	object, err := decodeJSONObject(data)
	if err != nil {
		t.Fatalf("decode frame %q: %v", data, err)
	}
	return object
}

// readItem reads one item frame for streamID and returns its decoded value.
func readItem(t *testing.T, conn *websocket.Conn, streamID string) map[string]json.RawMessage {
	t.Helper()
	frame := readFrame(t, conn)
	assertKeys(t, frame, "type", "streamId", "value")
	assertField(t, frame, "type", "item")
	assertField(t, frame, "streamId", streamID)
	value, err := decodeJSONObject(frame["value"])
	if err != nil {
		t.Fatalf("item value is not an object: %v", err)
	}
	return value
}

// frameReader reads frames on its own goroutine so a test can wait for one with
// a deadline without using the connection's read deadline: gorilla leaves a
// connection whose read timed out in a failed state, and the streaming tests have
// to keep reading after an idle window.
type frameReader struct {
	frames chan map[string]json.RawMessage
	fail   chan error
}

// startFrameReader takes over a connection's reads. Any read deadline an earlier
// direct read left behind is cleared first.
func startFrameReader(t *testing.T, conn *websocket.Conn) *frameReader {
	t.Helper()
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline: %v", err)
	}
	reader := &frameReader{frames: make(chan map[string]json.RawMessage, 128), fail: make(chan error, 1)}
	go func() {
		defer close(reader.frames)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				reader.fail <- err
				return
			}
			object, err := decodeJSONObject(data)
			if err != nil {
				reader.fail <- err
				return
			}
			reader.frames <- object
		}
	}()
	return reader
}

// next returns the next frame, or ok=false when none arrived within wait. A
// connection that closed is a failure: a stream under test stays open.
func (r *frameReader) next(t *testing.T, wait time.Duration) (map[string]json.RawMessage, bool) {
	t.Helper()
	select {
	case frame, open := <-r.frames:
		if !open {
			select {
			case err := <-r.fail:
				t.Fatalf("stream closed while a frame was expected: %v", err)
			default:
				t.Fatalf("stream closed while a frame was expected")
			}
		}
		return frame, true
	case <-time.After(wait):
		return nil, false
	}
}

// requireNext reads the next frame, failing the test when none arrives.
func (r *frameReader) requireNext(t *testing.T, wait time.Duration) map[string]json.RawMessage {
	t.Helper()
	frame, ok := r.next(t, wait)
	if !ok {
		t.Fatalf("no frame within %s", wait)
	}
	return frame
}

// readStreamEnd reads the next frame and requires it to be a terminal frame,
// returning its discriminator ("end" or "error").
func readStreamEnd(t *testing.T, conn *websocket.Conn, streamID string) (string, map[string]json.RawMessage) {
	t.Helper()
	frame := readFrame(t, conn)
	kind, _ := stringField(t, frame, "type")
	switch kind {
	case "end":
		assertKeys(t, frame, "type", "streamId")
		assertField(t, frame, "streamId", streamID)
		return "end", nil
	case "error":
		assertKeys(t, frame, "type", "streamId", "error")
		assertField(t, frame, "streamId", streamID)
		failure, err := decodeJSONObject(frame["error"])
		if err != nil {
			t.Fatalf("error object is not an object: %v", err)
		}
		assertKeys(t, failure, "code", "message", "details")
		if _, err := decodeJSONObject(failure["details"]); err != nil {
			t.Fatalf("error details is not an object: %v", err)
		}
		return "error", failure
	default:
		t.Fatalf("terminal frame type = %q, want end or error", kind)
		return "", nil
	}
}

// assertKeys requires the object's key set to equal want exactly.
func assertKeys(t *testing.T, object map[string]json.RawMessage, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Fatalf("keys = %v, want %v", got, sorted)
	}
}

// assertField requires one string field to equal want.
func assertField(t *testing.T, object map[string]json.RawMessage, key, want string) {
	t.Helper()
	got, ok := stringField(t, object, key)
	if !ok {
		t.Fatalf("field %q is missing", key)
	}
	if got != want {
		t.Fatalf("field %q = %q, want %q", key, got, want)
	}
}

// stringField reads an optional string field.
func stringField(t *testing.T, object map[string]json.RawMessage, key string) (string, bool) {
	t.Helper()
	raw, ok := object[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("field %q is not a string: %v", key, err)
	}
	return value, true
}

// intField reads a required integer field.
func intField(t *testing.T, object map[string]json.RawMessage, key string) int64 {
	t.Helper()
	raw, ok := object[key]
	if !ok {
		t.Fatalf("field %q is missing", key)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("field %q is not an integer: %v", key, err)
	}
	return value
}

// openEventsStream opens $events and returns the ready frame's clientId.
func openEventsStream(t *testing.T, conn *websocket.Conn, streamID string) string {
	t.Helper()
	openStream(t, conn, streamID, eventStreamEndpoint, "{}")
	ready := readItem(t, conn, streamID)
	assertKeys(t, ready, "type", "clientId", "host")
	assertField(t, ready, "type", "ready")
	clientID, _ := stringField(t, ready, "clientId")
	if clientID == "" {
		t.Fatal("ready frame has an empty clientId")
	}
	return clientID
}

// valueType reads the discriminator of one stream item's value.
func valueType(t *testing.T, value map[string]json.RawMessage) string {
	t.Helper()
	kind, ok := stringField(t, value, "type")
	if !ok {
		t.Fatal("stream item value has no type")
	}
	return kind
}

// recordEventType reads the zenforge event name out of one session/follow
// event item value.
func recordEventType(t *testing.T, value map[string]json.RawMessage) string {
	t.Helper()
	if kind := valueType(t, value); kind != "event" {
		t.Fatalf("follow item type = %q, want event", kind)
	}
	event := decodeValueObject(t, value["event"])
	name, _ := stringField(t, event, "type")
	return name
}

// decodeValueObject decodes one raw JSON value into an object, failing the test
// otherwise.
// decodeValueList decodes one JSON array of raw values, for the cells whose value
// is a list (the pending queue's two message lists).
func decodeValueList(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("decode value list %s: %v", raw, err)
	}
	return values
}

func decodeValueObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	object, err := decodeJSONObject(raw)
	if err != nil {
		t.Fatalf("value is not a JSON object: %v", err)
	}
	return object
}

// responseEnvelope is the unary server-response shape.
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

func decodeResponse(t *testing.T, response *http.Response) responseEnvelope {
	t.Helper()
	var envelope responseEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return envelope
}

// resultBody builds one $events/result client-request envelope.
func resultBody(t *testing.T, clientID, eventID, outcome string) string {
	t.Helper()
	return fmt.Sprintf(
		`{"type":"client-request","rpcId":"rpc-result","method":"$events/result","payload":{"args":{"clientId":%s,"eventId":%s,"outcome":%s}}}`,
		mustJSON(t, clientID), mustJSON(t, eventID), outcome)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %v: %v", value, err)
	}
	return string(encoded)
}

// waitForStatus polls the manager until the run reaches want.
func waitForStatus(t *testing.T, manager *harnesshttp.RunManager, runID string, want harnesshttp.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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

// waitForTerminal polls the manager until the run reaches any terminal status.
func waitForTerminal(t *testing.T, manager *harnesshttp.RunManager, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := manager.Get(runID)
		if err == nil {
			switch info.Status {
			case harnesshttp.RunCompleted, harnesshttp.RunFailed, harnesshttp.RunCancelled:
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %q did not reach a terminal status", runID)
}

// newApprovalRequest is one valid pending approval for runID.
func newApprovalRequest(id, runID string) approval.Request {
	return approval.Request{
		ID:          id,
		RunID:       runID,
		ToolCallID:  "call-1",
		ToolName:    "bash",
		Operation:   "execute",
		Title:       "Run a shell command",
		Description: "the model wants to run a shell command",
		Risk:        approval.RiskHigh,
		Options:     approval.DefaultOptions(),
		CreatedAt:   time.Now().UTC(),
	}
}

// pendingDecision registers one request with the broker in the background and
// returns channels for its decision and error. The caller must drain
// broker.Requests() before asserting the waterfall, which is the point at which
// the broker has the request in its pending table. Cancelling the test releases
// a request that is never answered.
func pendingDecision(t *testing.T, broker *approval.PendingBroker, request approval.Request) (<-chan approval.Decision, <-chan error) {
	t.Helper()
	decisions := make(chan approval.Decision, 1)
	failures := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		decision, err := broker.Request(ctx, request)
		if err != nil {
			failures <- err
			return
		}
		decisions <- decision
	}()
	return decisions, failures
}

// rewriteAddrListener reports a fixed RemoteAddr for every accepted
// connection, so the non-loopback fence can be exercised through a real
// handshake.
type rewriteAddrListener struct {
	net.Listener
	addr string
}

func (l rewriteAddrListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &rewriteAddrConn{Conn: conn, addr: l.addr}, nil
}

type rewriteAddrConn struct {
	net.Conn
	addr string
}

func (c *rewriteAddrConn) RemoteAddr() net.Addr { return fixedAddr(c.addr) }

type fixedAddr string

func (a fixedAddr) Network() string { return "tcp" }
func (a fixedAddr) String() string  { return string(a) }

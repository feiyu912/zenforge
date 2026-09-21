package dshstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	bus := eventlog.NewBus()
	manager := newTestManager(t, memory.New(), bus)
	store := memory.New()
	broker := newTestBroker()

	if _, err := New(nil, store, broker, Config{}); err == nil {
		t.Fatal("New accepted a nil run manager")
	}
	if _, err := New(manager, nil, broker, Config{}); err == nil {
		t.Fatal("New accepted a nil event store")
	}
	if _, err := New(manager, store, nil, Config{}); err == nil {
		t.Fatal("New accepted a nil approval inbox")
	}
	handler, err := New(manager, store, broker, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if handler.cfg.HeartbeatInterval != defaultHeartbeatInterval {
		t.Fatalf("heartbeat default = %v, want %v", handler.cfg.HeartbeatInterval, defaultHeartbeatInterval)
	}
}

func TestMuxHandshakeRefusesForeignOrigin(t *testing.T) {
	f := newFixture(t, Config{})
	conn, response, err := f.dial(t, func(header http.Header) {
		header.Set("Origin", "http://evil.example")
	})
	if err == nil {
		_ = conn.Close()
		t.Fatal("handshake with a foreign Origin succeeded, want refusal")
	}
	if !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("dial error = %v, want ErrBadHandshake", err)
	}
	if response == nil {
		t.Fatal("refused handshake returned no HTTP response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestMuxHandshakeRefusesNonLoopbackRemote(t *testing.T) {
	f := newFixtureWithRemote(t, Config{}, "10.0.0.9:5000")
	conn, response, err := f.dial(t, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("handshake from a non-loopback remote succeeded, want refusal")
	}
	if response == nil {
		t.Fatal("refused handshake returned no HTTP response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestMuxHandshakeAllowsConfiguredRemote(t *testing.T) {
	f := newFixtureWithRemote(t, Config{AllowRemote: true}, "10.0.0.9:5000")
	conn := f.mustDial(t)
	openStream(t, conn, "ctl", "session/control", "")
	readItem(t, conn, "ctl")
}

func TestMuxHandshakeAllowsSameOriginLoopback(t *testing.T) {
	f := newFixture(t, Config{})
	conn, _, err := f.dial(t, func(header http.Header) {
		header.Set("Origin", "http://"+f.host())
	})
	if err != nil {
		t.Fatalf("same-origin handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	openStream(t, conn, "ctl", "session/control", "")
	readItem(t, conn, "ctl")
}

func TestMuxRefusesCrossSiteHeader(t *testing.T) {
	f := newFixture(t, Config{})
	conn, response, err := f.dial(t, func(header http.Header) {
		header.Set("Sec-Fetch-Site", "cross-site")
	})
	if err == nil {
		_ = conn.Close()
		t.Fatal("cross-site handshake succeeded, want refusal")
	}
	if response == nil {
		t.Fatal("refused handshake returned no HTTP response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestEventsResultRefusesForeignOrigin(t *testing.T) {
	f := newFixture(t, Config{})
	request, err := http.NewRequest(http.MethodPost, f.server.URL+EventsResultPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Origin", "http://evil.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestMuxFrameKeysAreExact(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "keys")
	conn := f.mustDial(t)

	// $events: ready is the first item.
	openStream(t, conn, "events", eventStreamEndpoint, "{}")
	ready := readItem(t, conn, "events")
	assertKeys(t, ready, "type", "clientId", "host")
	assertField(t, ready, "type", "ready")
	host := decodeValueObject(t, ready["host"])
	assertKeys(t, host, "home")

	// session/control: baseline is the first item and its value is exact.
	openStream(t, conn, "control", "session/control", "")
	baseline := readItem(t, conn, "control")
	assertKeys(t, baseline, "type", "value")
	assertField(t, baseline, "type", "baseline")
	controlValue := decodeValueObject(t, baseline["value"])
	assertKeys(t, controlValue, "queues", "jobs", "projections")

	// session/follow: snapshot is the first item and every nested object is
	// exact.
	openStream(t, conn, "follow", "session/follow",
		fmt.Sprintf(`{"address":{"kind":"session","sessionId":%s},"assistantStream":true}`, mustJSON(t, runID)))
	snapshot := readItem(t, conn, "follow")
	assertKeys(t, snapshot, "type", "header", "cursor", "records", "hasMore", "projections", "assistantStream")
	assertField(t, snapshot, "type", "snapshot")
	header := decodeValueObject(t, snapshot["header"])
	assertKeys(t, header, "version", "id", "createdAt", "isSeeded")
	projections := decodeValueObject(t, snapshot["projections"])
	assertKeys(t, projections, "asOfSeq", "values")
	assistant := decodeValueObject(t, snapshot["assistantStream"])
	assertKeys(t, assistant, "revision")
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["records"], &records); err != nil {
		t.Fatalf("records is not an array: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("snapshot has no records for a started run")
	}
	for _, record := range records {
		assertKeys(t, record, "type", "event")
		assertField(t, record, "type", "event")
		event := decodeValueObject(t, record["event"])
		assertKeys(t, event, "type", "seq", "time", "data", "surfaceOp")
	}

	// A finished turn leaves the stream open and quiet: the console's client
	// treats a clean end after the snapshot as a carrier failure and reconnects,
	// which is what discards every earlier page "load earlier" fetched (ADR 0114).
	// Every frame the turn does send keeps the exact item key set.
	f.agent.finish(runID)
	reader := startFrameReader(t, conn)
	for {
		frame := reader.requireNext(t, 5*time.Second)
		assertKeys(t, frame, "type", "streamId", "value")
		assertField(t, frame, "type", "item")
		value, err := decodeJSONObject(frame["value"])
		if err != nil {
			t.Fatalf("item value is not an object: %v", err)
		}
		if recordEventType(t, value) == "turn/end" {
			break
		}
	}
	if frame, ok := reader.next(t, 300*time.Millisecond); ok {
		t.Fatalf("frame after the turn ended: %v, want silence", frame)
	}
}

func TestMuxRejectsUnknownEndpoint(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "unknown", "session/nope", "{}")
	kind, failure := readStreamEnd(t, conn, "unknown")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeNotFound)
}

func TestMuxRejectsExtraClientMessageKeys(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	frame := `{"type":"cancel","streamId":"x","extra":1}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertCloseCode(t, conn, websocket.ClosePolicyViolation)
}

func TestMuxRejectsDuplicateStreamID(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "dup", "session/control", "")
	openStream(t, conn, "dup", "session/control", "")
	// The baseline from the first open may arrive before or after the close;
	// read until the close frame and fail if the socket ends any other way.
	assertCloseCodeSkippingItems(t, conn, websocket.ClosePolicyViolation)
}

func TestMuxCancelUnknownStreamKeepsConnectionUsable(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	cancelStream(t, conn, "never-opened")
	openStream(t, conn, "ctl", "session/control", "")
	baseline := readItem(t, conn, "ctl")
	assertField(t, baseline, "type", "baseline")
}

func TestMuxHeartbeatSendsPing(t *testing.T) {
	f := newFixture(t, Config{HeartbeatInterval: 20 * time.Millisecond})
	conn := f.mustDial(t)

	pings := make(chan struct{}, 4)
	conn.SetPingHandler(func(appData string) error {
		select {
		case pings <- struct{}{}:
		default:
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})
	// The client must be reading for control frames to be processed.
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case <-pings:
	case <-time.After(3 * time.Second):
		t.Fatal("no heartbeat ping arrived within 3s")
	}
}

func TestMuxClosesUnresponsiveClient(t *testing.T) {
	f := newFixture(t, Config{HeartbeatInterval: 30 * time.Millisecond})
	conn := f.mustDial(t)
	// Answering no ping is the failure the heartbeat exists to catch.
	conn.SetPingHandler(func(string) error { return nil })

	closed := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage()
		closed <- err
	}()
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("read returned nil after the server should have closed the client")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("server did not terminate a client that never answered a ping")
	}
}

func TestMuxClientDisconnectLeavesNoGoroutineLeak(t *testing.T) {
	f := newFixture(t, Config{HeartbeatInterval: 50 * time.Millisecond})
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for index := 0; index < 10; index++ {
		runID := f.startRun(t, "leak")
		conn, _, err := f.dial(t, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		openStream(t, conn, "follow", "session/follow",
			fmt.Sprintf(`{"address":{"kind":"session","sessionId":%s},"assistantStream":true}`, mustJSON(t, runID)))
		snapshot := readItem(t, conn, "follow")
		assertField(t, snapshot, "type", "snapshot")
		// Abrupt disconnect while the stream is live.
		_ = conn.Close()
		if err := f.manager.Cancel(runID); err != nil {
			t.Fatalf("cancel run: %v", err)
		}
		waitForTerminal(t, f.manager, runID)
	}

	// Bounded settle check: every per-connection goroutine must exit promptly.
	deadline := time.Now().Add(3 * time.Second)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline+15 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after 10 disconnects, baseline = %d", current, baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertCloseCode reads until the server closes the socket and requires the
// given close code.
func assertCloseCode(t *testing.T, conn *websocket.Conn, want int) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) {
			t.Fatalf("read error = %v, want a close error with code %d", err, want)
		}
		if closeErr.Code != want {
			t.Fatalf("close code = %d, want %d", closeErr.Code, want)
		}
		return
	}
}

// assertCloseCodeSkippingItems is assertCloseCode for a socket that may still
// carry a queued item frame before the close.
func assertCloseCodeSkippingItems(t *testing.T, conn *websocket.Conn, want int) {
	t.Helper()
	assertCloseCode(t, conn, want)
}

// newTestManager builds a run manager over the given store without a pending
// fixture. It is used only by the constructor test.
func newTestManager(t *testing.T, store eventlog.Store, bus *eventlog.Bus) *harnesshttp.RunManager {
	t.Helper()
	manager := harnesshttp.NewRunManager(newStubAgent(store), store, bus, harnesshttp.RunManagerOptions{
		TerminalRetention: -1,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

func newTestBroker() *approval.PendingBroker {
	return approval.NewPendingBroker(1)
}

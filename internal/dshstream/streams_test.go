package dshstream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

func TestEventsReadyFrameIsFirst(t *testing.T) {
	f := newFixture(t, Config{Home: "/home/tester"})
	conn := f.mustDial(t)
	openStream(t, conn, "events", eventStreamEndpoint, "{}")

	ready := readItem(t, conn, "events")
	assertKeys(t, ready, "type", "clientId", "host")
	assertField(t, ready, "type", "ready")
	host := decodeValueObject(t, ready["host"])
	assertKeys(t, host, "home")
	assertField(t, host, "home", "/home/tester")
}

func TestEventsRequiresEmptyArgs(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "events", eventStreamEndpoint, `{"unexpected":true}`)
	kind, failure := readStreamEnd(t, conn, "events")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeArgumentsInvalid)
}

func TestEventsDeliversApprovalWaterfall(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "approval")
	request := newApprovalRequest("approval-waterfall", runID)
	pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	openStream(t, conn, "events", eventStreamEndpoint, "{}")
	ready := readItem(t, conn, "events")
	assertField(t, ready, "type", "ready")

	waterfall := readItem(t, conn, "events")
	assertKeys(t, waterfall, "type", "event", "eventId", "agentId", "request")
	assertField(t, waterfall, "type", "waterfall")
	assertField(t, waterfall, "event", "approval/request")
	assertField(t, waterfall, "eventId", request.ID)
	assertField(t, waterfall, "agentId", runID)
	payload := decodeValueObject(t, waterfall["request"])
	assertKeys(t, payload, "toolName", "operation", "risk", "title", "callId", "reason")
	assertField(t, payload, "toolName", "bash")
	assertField(t, payload, "callId", "call-1")
	assertField(t, payload, "reason", request.Description)
}

func TestEventsCancelsAnsweredApproval(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "approval")
	request := newApprovalRequest("approval-cancel", runID)
	decisions, failures := pendingDecision(t, f.broker, request)
	<-f.broker.Requests()

	conn := f.mustDial(t)
	clientID := openEventsStream(t, conn, "events")
	waterfall := readItem(t, conn, "events")
	assertField(t, waterfall, "type", "waterfall")

	response := f.postResult(t, resultBody(t, clientID, request.ID, `{"kind":"result","value":"allowed-once"}`))
	envelope := decodeResponse(t, response)
	if !envelope.Result.OK {
		t.Fatalf("answer failed: %+v", envelope.Result.Error)
	}
	select {
	case decision := <-decisions:
		if decision.Action != approval.DecisionApprove {
			t.Fatalf("decision action = %q, want approve", decision.Action)
		}
	case err := <-failures:
		t.Fatalf("broker request failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("broker did not resolve")
	}

	// The answered request is no longer pending, so the stream withdraws it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value := readItem(t, conn, "events")
		if valueType(t, value) == "cancel" {
			assertKeys(t, value, "type", "eventId")
			assertField(t, value, "eventId", request.ID)
			return
		}
	}
	t.Fatal("no cancellation frame for an answered approval")
}

func TestSessionControlBaseline(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", "")

	value := readItem(t, conn, "control")
	assertKeys(t, value, "type", "value")
	assertField(t, value, "type", "baseline")
	baseline := decodeValueObject(t, value["value"])
	assertKeys(t, baseline, "jobs", "projections")
	if got := string(baseline["jobs"]); got != "{}" {
		t.Fatalf("jobs = %s, want {}", got)
	}
	if got := string(baseline["projections"]); got != "{}" {
		t.Fatalf("projections = %s, want {}", got)
	}
}

func TestSessionControlRejectsArguments(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", `{"unexpected":true}`)
	kind, failure := readStreamEnd(t, conn, "control")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeArgumentsInvalid)
}

func TestSessionFollowSnapshotThenEventsThenEndOnFinish(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "finish")
	f.agent.emit(runID, zenforge.EventStepStarted, map[string]any{"step": 1})

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	cursor := intField(t, snapshot, "cursor")
	if cursor <= 0 {
		t.Fatalf("snapshot cursor = %d, want the durable tail", cursor)
	}
	header := decodeValueObject(t, snapshot["header"])
	assertKeys(t, header, "version", "id", "createdAt", "isSeeded")
	assertField(t, header, "id", runID)
	if version := intField(t, header, "version"); version != 3 {
		t.Fatalf("header version = %d, want 3", version)
	}
	records := decodeRecords(t, snapshot)
	names := make(map[string]bool)
	for _, record := range records {
		names[recordEventType(t, record)] = true
	}
	if !names[string(zenforge.EventRunStarted)] || !names[string(zenforge.EventStepStarted)] {
		t.Fatalf("snapshot event names = %v, want run.started and step.started", names)
	}

	// A live append after the snapshot arrives as an ordinary event frame.
	f.agent.emit(runID, zenforge.EventStepDone, map[string]any{"step": 1})
	live := readItem(t, conn, "follow")
	if got := recordEventType(t, live); got != string(zenforge.EventStepDone) {
		t.Fatalf("live event type = %q, want step.done", got)
	}
	event := decodeValueObject(t, live["event"])
	if seq := intField(t, event, "seq"); seq != cursor+1 {
		t.Fatalf("live seq = %d, want %d", seq, cursor+1)
	}
	assertField(t, event, "surfaceOp", "append")

	f.agent.finish(runID)
	for {
		value := readItem(t, conn, "follow")
		if recordEventType(t, value) == string(zenforge.EventRunDone) {
			break
		}
	}
	kind, _ := readStreamEnd(t, conn, "follow")
	if kind != "end" {
		t.Fatalf("terminal frame = %q, want end", kind)
	}
}

func TestSessionFollowEndsOnCancelledRun(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "cancel")

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")

	if err := f.manager.Cancel(runID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	for {
		value := readItem(t, conn, "follow")
		if recordEventType(t, value) == string(zenforge.EventRunCancelled) {
			break
		}
	}
	kind, _ := readStreamEnd(t, conn, "follow")
	if kind != "end" {
		t.Fatalf("terminal frame = %q, want end", kind)
	}
}

func TestSessionFollowUnknownSession(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, "run-does-not-exist", false))
	kind, failure := readStreamEnd(t, conn, "follow")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeSessionNotFound)
}

func TestSessionFollowRejectsSubagentAddress(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow",
		`{"address":{"kind":"subagent","parentSessionId":"a","childSessionId":"b","mode":"one-shot"}}`)
	kind, failure := readStreamEnd(t, conn, "follow")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeUnimplemented)
}

func TestSessionFollowAssistantStreamBaseline(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "baseline")

	optedIn := f.mustDial(t)
	openStream(t, optedIn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, optedIn, "follow")
	baseline := decodeValueObject(t, snapshot["assistantStream"])
	assertKeys(t, baseline, "revision")
	if revision := intField(t, baseline, "revision"); revision != 0 {
		t.Fatalf("assistantStream revision = %d, want 0", revision)
	}

	notOptedIn := f.mustDial(t)
	openStream(t, notOptedIn, "follow", "session/follow", followArgs(t, runID, false))
	plain := readItem(t, notOptedIn, "follow")
	assertKeys(t, plain, "type", "header", "cursor", "records", "hasMore", "projections")
}

func TestSessionFollowStreamsModelDeltasAsDurableEvents(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "deltas")

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")

	f.agent.emit(runID, zenforge.EventModelDelta, map[string]any{
		"attemptId": "attempt-1", "step": 1, "chunkSeq": 1, "offset": 0, "textDelta": "hello",
	})
	value := readItem(t, conn, "follow")
	// The harness streams model output as durable model.delta events, not as
	// the console's process-local assistant-stream frames, so the frame that
	// carries it is an ordinary event item.
	assertKeys(t, value, "type", "event")
	if got := recordEventType(t, value); got != string(zenforge.EventModelDelta) {
		t.Fatalf("event type = %q, want model.delta", got)
	}
	event := decodeValueObject(t, value["event"])
	data := decodeValueObject(t, event["data"])
	assertField(t, data, "textDelta", "hello")
}

// followArgs builds one session/follow args object.
func followArgs(t *testing.T, runID string, assistantStream bool) string {
	t.Helper()
	if assistantStream {
		return fmt.Sprintf(`{"address":{"kind":"session","sessionId":%s},"assistantStream":true}`, mustJSON(t, runID))
	}
	return fmt.Sprintf(`{"address":{"kind":"session","sessionId":%s}}`, mustJSON(t, runID))
}

// decodeRecords decodes a snapshot's records array. Each record is the same
// {type:"event", event} wrapper a live item carries, so recordEventType reads
// either one.
func decodeRecords(t *testing.T, snapshot map[string]json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["records"], &records); err != nil {
		t.Fatalf("records is not an array: %v", err)
	}
	for _, record := range records {
		assertKeys(t, record, "type", "event")
	}
	return records
}

// TestSessionFollowServesADraftSessionAndItsFirstTurn is the stream half of the
// draft rule: the console opens a session's history as soon as it creates the
// session, before the first prompt has started a run. That session has no run and
// no log, which used to be answered as session/not-found -- the page's "Failed to
// load history" on a brand-new chat. It is served as an empty log (cursor -1), and
// the stream then waits for the first turn's run so the turn arrives over the same
// connection instead of after a reconnect.
func TestSessionFollowServesADraftSessionAndItsFirstTurn(t *testing.T) {
	var mu sync.Mutex
	drafts := map[string]bool{}
	f := newFixture(t, Config{
		DraftSessions: func(sessionID string) bool {
			mu.Lock()
			defer mu.Unlock()
			return drafts[sessionID]
		},
	})
	runID := zenforge.NewRunID()
	mu.Lock()
	drafts[runID] = true
	mu.Unlock()

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, false))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	if cursor := intField(t, snapshot, "cursor"); cursor != -1 {
		t.Fatalf("snapshot cursor = %d, want -1 for a log with no records", cursor)
	}
	if records := decodeRecords(t, snapshot); len(records) != 0 {
		t.Fatalf("snapshot records = %d, want an empty draft history", len(records))
	}

	// session/prompt creates the run. The stream, already open, waits for it and
	// then streams the turn.
	mu.Lock()
	drafts[runID] = false
	mu.Unlock()
	if _, err := f.manager.Start(context.Background(), zenforge.Task{RunID: runID, Input: "hello"}); err != nil {
		t.Fatalf("start the draft's first run: %v", err)
	}
	seen := map[string]bool{}
	for len(seen) == 0 {
		value := readItem(t, conn, "follow")
		seen[recordEventType(t, value)] = true
	}
	if !seen[string(zenforge.EventRunStarted)] {
		t.Fatalf("first frames = %v, want the draft's run.started", seen)
	}
}

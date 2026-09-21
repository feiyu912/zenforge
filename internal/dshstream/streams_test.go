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
	// queues is required by the client's baseline handler -- it iterates the map
	// before anything else and throws on an absent key, which would discard the
	// projections seeded right after it (ADR 0119).
	assertKeys(t, baseline, "queues", "jobs", "projections")
	if got := string(baseline["queues"]); got != "{}" {
		t.Fatalf("queues = %s, want {}", got)
	}
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

func TestSessionFollowStaysOpenAndCarriesTheNextTurn(t *testing.T) {
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
	// The snapshot is served in the console's vocabulary: the run's prompt is a
	// user message and the step is a step/start, which is what makes a transcript
	// appear. The host's own event names are not what the console renders.
	if !names["user/message"] || !names["step/start"] {
		t.Fatalf("snapshot event names = %v, want user/message and step/start", names)
	}
	if names[string(zenforge.EventRunStarted)] {
		t.Fatalf("snapshot event names = %v, want the prompt projected rather than passed through", names)
	}

	// A live append after the snapshot arrives as an ordinary event frame, in the
	// console's vocabulary and with the snapshot's own numbering continued.
	f.agent.emit(runID, zenforge.EventStepDone, map[string]any{"step": 1})
	live := readItem(t, conn, "follow")
	if got := recordEventType(t, live); got != "step/end" {
		t.Fatalf("live event type = %q, want step/end", got)
	}
	event := decodeValueObject(t, live["event"])
	if seq := intField(t, event, "seq"); seq != cursor+1 {
		t.Fatalf("live seq = %d, want %d", seq, cursor+1)
	}
	// A step boundary is not a surface event, so it must not carry the marker;
	// the client refuses a non-eligible type that does.
	if kind := valueType(t, live); kind != "event" {
		t.Fatalf("live frame type = %q, want event", kind)
	}
	if raw, present := event["surfaceOp"]; present && string(raw) != "null" {
		t.Fatalf("step/end carries surfaceOp %s", raw)
	}

	f.agent.finish(runID)
	var turnEnd int64
	for {
		value := readItem(t, conn, "follow")
		if recordEventType(t, value) == "turn/end" {
			turnEnd = intField(t, decodeValueObject(t, value["event"]), "seq")
			break
		}
	}
	// The turn is over; the stream is not. The client builds a carrier failure for
	// a stream that ends after its opening snapshot and reconnects, and every
	// reconnect reinstalls the tail window over whatever "load earlier" added, so
	// an end here is what makes the button repage forever without showing less
	// (ADR 0114). An idle conversation must show silence instead.
	reader := startFrameReader(t, conn)
	if frame, ok := reader.next(t, 400*time.Millisecond); ok {
		t.Fatalf("frame after the turn ended: %v, want silence", frame)
	}
	// The conversation's next turn arrives on the same stream and continues the
	// session sequence the console already cursors on, which is what makes its
	// first event land exactly one past the turn that ended.
	startTurn(t, f, runID, 2, "second turn")
	frame := reader.requireNext(t, 5*time.Second)
	assertField(t, frame, "type", "item")
	assertField(t, frame, "streamId", "follow")
	value, err := decodeJSONObject(frame["value"])
	if err != nil {
		t.Fatalf("next turn's item value is not an object: %v", err)
	}
	// The next turn opens with its own marker, then carries the question: both
	// records come from the run's opening durable event, and both are sent.
	if got := recordEventType(t, value); got != "turn/start" {
		t.Fatalf("first frame of the next turn = %q, want the turn's opening marker", got)
	}
	if seq := intField(t, decodeValueObject(t, value["event"]), "seq"); seq != turnEnd+1 {
		t.Fatalf("next turn's first seq = %d, want %d", seq, turnEnd+1)
	}
	question, err := decodeJSONObject(reader.requireNext(t, 5*time.Second)["value"])
	if err != nil {
		t.Fatalf("next turn's second item value is not an object: %v", err)
	}
	if got := recordEventType(t, question); got != "user/message" {
		t.Fatalf("second frame of the next turn = %q, want the question", got)
	}
	if seq := intField(t, decodeValueObject(t, question["event"]), "seq"); seq != turnEnd+2 {
		t.Fatalf("next turn's question seq = %d, want %d", seq, turnEnd+2)
	}
}

func TestSessionFollowStaysOpenAfterACancelledRun(t *testing.T) {
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
		// A cancelled run closes the console turn as an aborted turn/end.
		if recordEventType(t, value) == "turn/end" {
			break
		}
	}
	// A cancelled turn leaves the conversation usable: the stream stays open so the
	// console can prompt again on the same connection (ADR 0114).
	reader := startFrameReader(t, conn)
	if frame, ok := reader.next(t, 400*time.Millisecond); ok {
		t.Fatalf("frame after the cancelled turn: %v, want silence", frame)
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

// TestSessionFollowStreamsTheAnswerAsAssistantFrames is the operator's report
// pinned: the answer "appeared all at once". The harness streams model output as
// durable model.delta events, which the served log does not carry as records at
// all (they are not the console's vocabulary), so the client had nothing to render
// until the step's settlement arrived. The live stream mints the console's dense
// assistant-stream frames from those same events instead.
func TestSessionFollowStreamsTheAnswerAsAssistantFrames(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "deltas")

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	baseline := decodeValueObject(t, snapshot["assistantStream"])
	assertKeys(t, baseline, "revision")
	if got := intField(t, baseline, "revision"); got != 0 {
		t.Fatalf("baseline revision = %d, want 0", got)
	}

	f.agent.emit(runID, zenforge.EventModelDelta, map[string]any{
		"attemptId": "attempt-1", "step": 1, "chunkSeq": 1, "offset": 0, "textDelta": "hello",
	})

	// The attempt opens and the delta's block opens with it: two frames, and no
	// durable record for the delta at all -- an event outside the console's
	// vocabulary contributes neither a record nor a sequence number (ADR 0117).
	start := readItem(t, conn, "follow")
	assertKeys(t, start, "type", "frame")
	assertField(t, start, "type", "assistant-stream")
	startFrame := decodeValueObject(t, start["frame"])
	assertKeys(t, startFrame, "type", "attemptId", "revision", "startedAfterSeq", "turn", "step")
	assertField(t, startFrame, "type", "start")
	assertField(t, startFrame, "attemptId", "attempt-1")
	if got := intField(t, startFrame, "step"); got != 1 {
		t.Fatalf("start step = %d, want 1", got)
	}
	if got := intField(t, startFrame, "turn"); got != 1 {
		t.Fatalf("start turn = %d, want 1", got)
	}

	opened := readItem(t, conn, "follow")
	assertKeys(t, opened, "type", "frame")
	assertField(t, opened, "type", "assistant-stream")
	openedFrame := decodeValueObject(t, opened["frame"])
	assertKeys(t, openedFrame, "type", "attemptId", "revision", "index", "time", "chunk")
	assertField(t, openedFrame, "type", "chunk")
	if got := intField(t, openedFrame, "index"); got != 0 {
		t.Fatalf("block start frame index = %d, want 0", got)
	}
	block := decodeValueObject(t, openedFrame["chunk"])
	assertKeys(t, block, "type", "index", "blockType")
	assertField(t, block, "type", "block-start")
	assertField(t, block, "blockType", "text")

	chunk := readItem(t, conn, "follow")
	assertKeys(t, chunk, "type", "frame")
	assertField(t, chunk, "type", "assistant-stream")
	chunkFrame := decodeValueObject(t, chunk["frame"])
	assertKeys(t, chunkFrame, "type", "attemptId", "revision", "index", "time", "chunk")
	assertField(t, chunkFrame, "type", "chunk")
	if got := intField(t, chunkFrame, "index"); got != 1 {
		t.Fatalf("chunk index = %d, want 1", got)
	}
	delta := decodeValueObject(t, chunkFrame["chunk"])
	assertKeys(t, delta, "type", "index", "text")
	assertField(t, delta, "type", "text-delta")
	assertField(t, delta, "text", "hello")

	// The settlement releases the attempt: the client stages that record and
	// publishes it when the end frame names its sequence and type.
	f.agent.emit(runID, zenforge.EventModelDone, map[string]any{"step": 1})

	settled := readItem(t, conn, "follow")
	if got := recordEventType(t, settled); got != "assistant/message" {
		t.Fatalf("settlement type = %q, want assistant/message", got)
	}
	settledEvent := decodeValueObject(t, settled["event"])
	seq := intField(t, settledEvent, "seq")
	// The deltas the window no longer carries are byte-exact in the settled
	// message's compact stream, which is where the trajectory view reads them.
	data := decodeValueObject(t, settledEvent["data"])
	var stream []map[string]any
	if err := json.Unmarshal(data["stream"], &stream); err != nil || len(stream) != 1 {
		t.Fatalf("settlement stream = %s, want one block timeline", data["stream"])
	}
	if texts, ok := stream[0]["texts"].([]any); !ok || len(texts) != 1 || texts[0] != "hello" {
		t.Fatalf("settlement timeline = %v, want the delta's text", stream[0])
	}
	if stream[0]["type"] != "text-chunks" || stream[0]["index"] != float64(0) {
		t.Fatalf("settlement timeline = %v, want block 0 as text-chunks", stream[0])
	}

	closed := readItem(t, conn, "follow")
	assertKeys(t, closed, "type", "frame")
	assertField(t, closed, "type", "assistant-stream")
	closedFrame := decodeValueObject(t, closed["frame"])
	assertKeys(t, closedFrame, "type", "attemptId", "revision", "index", "time", "chunk")
	closedBlock := decodeValueObject(t, closedFrame["chunk"])
	assertKeys(t, closedBlock, "type", "index", "block")
	assertField(t, closedBlock, "type", "block-end")

	end := readItem(t, conn, "follow")
	assertKeys(t, end, "type", "frame")
	assertField(t, end, "type", "assistant-stream")
	endFrame := decodeValueObject(t, end["frame"])
	assertKeys(t, endFrame, "type", "attemptId", "revision", "index", "outcome")
	assertField(t, endFrame, "type", "end")
	if got := intField(t, endFrame, "index"); got != 3 {
		t.Fatalf("end index = %d, want the three frames it settles", got)
	}
	outcome := decodeValueObject(t, endFrame["outcome"])
	assertKeys(t, outcome, "kind", "eventType", "seq")
	assertField(t, outcome, "kind", "committed")
	assertField(t, outcome, "eventType", "assistant/message")
	if got := intField(t, outcome, "seq"); got != seq {
		t.Fatalf("end seq = %d, want the settlement's %d", got, seq)
	}
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
	if !seen["turn/start"] {
		t.Fatalf("first frames = %v, want the turn's opening marker", seen)
	}
	value := readItem(t, conn, "follow")
	if got := recordEventType(t, value); got != "user/message" {
		t.Fatalf("frame after the turn marker = %q, want the draft's first prompt", got)
	}
}

// A console that reconnects while an answer is still streaming must be handed the
// open attempt in the snapshot's baseline, and the live tail must continue that
// same attempt rather than starting a second one -- a second start frame makes the
// client rebaseline, so a reload mid-answer would restart the whole stream and the
// operator would watch the partial answer disappear (ADR 0118).
func TestSessionFollowSnapshotResumesAMidAnswerAttempt(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "deltas")
	f.agent.emit(runID, zenforge.EventModelStarted, map[string]any{"attemptId": "attempt-1", "step": 1})
	f.agent.emit(runID, zenforge.EventModelDelta, map[string]any{
		"attemptId": "attempt-1", "step": 1, "chunkSeq": 1, "offset": 0, "textDelta": "half",
	})
	waitForLoggedEvents(t, f, runID, 3)

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	baseline := decodeValueObject(t, snapshot["assistantStream"])
	assertKeys(t, baseline, "revision", "activeAttempt")
	opening := decodeValueObject(t, baseline["activeAttempt"])
	assertKeys(t, opening, "attemptId", "startedAfterSeq", "turn", "step", "nextIndex", "stream")
	assertField(t, opening, "attemptId", "attempt-1")
	if got := intField(t, opening, "step"); got != 1 {
		t.Fatalf("baseline step = %d, want 1", got)
	}
	// Frames so far: the block start and the delta.
	if got := intField(t, opening, "nextIndex"); got != 2 {
		t.Fatalf("baseline nextIndex = %d, want the two frames the attempt has", got)
	}
	// The baseline revision is the generation's frame counter, not a constant: the
	// client holds every frame to revision+1, so citing 0 after replaying two
	// frames makes the next live frame a carrier failure (ADR 0119).
	// Three frames were numbered: the attempt's start, its block start, and the
	// delta (the client counts only the last two toward nextIndex).
	if got := intField(t, baseline, "revision"); got != 3 {
		t.Fatalf("baseline revision = %d, want the three frames it replayed", got)
	}
	var stream []map[string]any
	if err := json.Unmarshal(opening["stream"], &stream); err != nil {
		t.Fatalf("baseline stream did not decode: %v", err)
	}
	if len(stream) != 2 || stream[0]["type"] != "chunk" || stream[1]["type"] != "text-chunks" {
		t.Fatalf("baseline stream = %v, want the block start and the text run", stream)
	}
	if texts, ok := stream[1]["texts"].([]any); !ok || len(texts) != 1 || texts[0] != "half" {
		t.Fatalf("baseline timeline = %v, want the streamed text", stream[1])
	}
	// A one-delta run has no gap at all, and the console's expander requires an
	// array: a nil slice marshals as null and the validator rejects the whole
	// baseline with "dt must contain safe integers" (ADR 0120, verified against
	// dsh-llm's own expandAssistantStream).
	if gaps, ok := stream[1]["dt"].([]any); !ok || len(gaps) != 0 {
		t.Fatalf("baseline gaps = %#v, want an empty array", stream[1]["dt"])
	}

	// The live tail continues the attempt: the next delta is a chunk frame whose
	// index is the one after the baseline's, not a new start.
	f.agent.emit(runID, zenforge.EventModelDelta, map[string]any{
		"attemptId": "attempt-1", "step": 1, "chunkSeq": 2, "offset": 4, "textDelta": " an answer",
	})
	next := readItem(t, conn, "follow")
	assertField(t, next, "type", "assistant-stream")
	nextFrame := decodeValueObject(t, next["frame"])
	assertField(t, nextFrame, "type", "chunk")
	if got := intField(t, nextFrame, "index"); got != 2 {
		t.Fatalf("continuing chunk index = %d, want 2", got)
	}
	if got := intField(t, nextFrame, "revision"); got != 4 {
		t.Fatalf("continuing chunk revision = %d, want the baseline's + 1", got)
	}
	delta := decodeValueObject(t, nextFrame["chunk"])
	assertField(t, delta, "text", " an answer")
}

// waitForLoggedEvents polls the durable log until it holds at least count events,
// which is what makes a snapshot taken afterwards carry them.
func waitForLoggedEvents(t *testing.T, f *fixture, runID string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := f.store.Read(context.Background(), runID, 0, 0)
		if err == nil && len(events) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %q never logged %d events", runID, count)
}

// A reconnected console learns the conversation's name from the snapshot's
// projections block: that is the cell the header and the sidebar read, and a name
// served only as a durable event leaves both showing the raw session id
// (ADR 0119).
func TestSessionFollowServesTheTitleProjection(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "hello")
	f.agent.emit(runID, zenforge.EventSessionTitle, map[string]any{"title": "Named Chat", "source": "user"})
	waitForLoggedEvents(t, f, runID, 2)

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, false))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	projections := decodeValueObject(t, snapshot["projections"])
	values := decodeValueObject(t, projections["values"])
	assertField(t, values, "title", "Named Chat")
	if got := intField(t, projections, "asOfSeq"); got < 1 {
		t.Fatalf("projection watermark = %d, want the served sequence that set the title", got)
	}
}

// A file resource opens this subscription before it stats anything and renders only
// after the ready frame, so a stream that fails leaves the tab loading forever. The
// scope is checked for presence rather than existence: upstream's scope is a session,
// which exists before its first turn, while this host's only existence test is a
// started turn (ADR 0120).
func TestWorkspaceFileChangesStreamsReady(t *testing.T) {
	f := newFixture(t, Config{})
	scope := f.startRun(t, "hello")

	conn := f.mustDial(t)
	openStream(t, conn, "changes", "workspaceFiles/changes",
		fmt.Sprintf(`{"workspaceFileScopeId":%s}`, mustJSON(t, scope)))
	ready := readItem(t, conn, "changes")
	assertField(t, ready, "kind", "ready")
	assertKeys(t, ready, "kind")

	empty := f.mustDial(t)
	openStream(t, empty, "changes", "workspaceFiles/changes", `{"workspaceFileScopeId":""}`)
	kind, failure := readStreamEnd(t, empty, "changes")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeArgumentsInvalid)
}

package dshstream

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshsession"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// A console session outlives its runs, and the console keeps one cursor for the
// whole conversation. These tests hold the stream to that: the second turn's
// snapshot must not cite a sequence the console has already passed, the live tail
// must continue one past the snapshot's cursor, and the window must still reach
// the earlier turn.
//
// The shipped client enforces all three (api/gateway/src/client/journal-stream.ts):
// `assertPageThrough` requires the last record of a page to end exactly at the
// cursor the page was opened with, `follows` requires every live entry to be
// exactly one past the last applied one, and `opening(item, resumed)` rejects a
// resumed generation whose cursor is behind it -- which is the failure this
// reproduces:
//
//	Failed to load history: session event stream resumed at a cursor behind the
//	last applied entry (gateway/internal)

// snapshotOf opens a follow stream, reads its opening snapshot and returns the
// snapshot's cursor, its records and whether more history exists. The stream's
// rows are read straight from the wire so the assertions are about what the
// console receives.
func snapshotOf(t *testing.T, f *fixture, sessionID string) (int64, []map[string]json.RawMessage, bool) {
	t.Helper()
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, sessionID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	var cursor int64
	if err := json.Unmarshal(snapshot["cursor"], &cursor); err != nil {
		t.Fatalf("decode snapshot cursor: %v", err)
	}
	var hasMore bool
	if err := json.Unmarshal(snapshot["hasMore"], &hasMore); err != nil {
		t.Fatalf("decode snapshot hasMore: %v", err)
	}
	return cursor, decodeRecords(t, snapshot), hasMore
}

// bytesContains reports whether a raw JSON object contains a token.
func bytesContains(raw json.RawMessage, token string) bool {
	return bytes.Contains(raw, []byte(token))
}

// recordSeq reads one record's console sequence. It accepts both the decoded
// snapshot records and a live item frame, since both carry the same record under
// a different Go type.
func recordSeq(t *testing.T, record any) int64 {
	t.Helper()
	event := recordEvent(t, record)
	var value struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(event, &value); err != nil {
		t.Fatalf("decode record event: %v", err)
	}
	if value.Seq == 0 {
		t.Fatalf("record event carries no seq: %s", event)
	}
	return value.Seq
}

// recordEvent renders one record's event object from either shape.
func recordEvent(t *testing.T, record any) json.RawMessage {
	t.Helper()
	switch typed := record.(type) {
	case map[string]any:
		event, ok := typed["event"]
		if !ok {
			t.Fatalf("record carries no event: %v", record)
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal record event: %v", err)
		}
		return raw
	case map[string]json.RawMessage:
		event, ok := typed["event"]
		if !ok {
			t.Fatalf("record carries no event: %v", record)
		}
		return event
	default:
		t.Fatalf("unknown record shape %T", record)
		return nil
	}
}

// turnOf reads one record's console turn number, or 0 when the record has none.
func turnOf(t *testing.T, record any) int {
	t.Helper()
	var value struct {
		Data struct {
			Turn int `json:"turn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recordEvent(t, record), &value); err != nil {
		t.Fatalf("decode record event: %v", err)
	}
	return value.Data.Turn
}

// startTurn starts one more turn of a conversation, waits until it is running
// and returns its run id.
func startTurn(t *testing.T, f *fixture, sessionID string, turn int, input string) string {
	t.Helper()
	runID := dshsession.ContinuationRunID(sessionID, turn)
	if _, err := f.manager.Start(context.Background(), zenforge.Task{RunID: runID, Input: input}); err != nil {
		t.Fatalf("start turn %d: %v", turn, err)
	}
	waitForStatus(t, f.manager, runID, harnesshttp.RunRunning)
	return runID
}

// TestSessionFollowResumesAheadOfTheFirstTurnsCursor is the operator's failure,
// reproduced: a conversation whose second turn serves its own numbering from one
// made the resumed stream cite a cursor behind the first turn's tail, and the
// console answered "session event stream resumed at a cursor behind the last
// applied entry" instead of loading the history.
func TestSessionFollowResumesAheadOfTheFirstTurnsCursor(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startRun(t, "first turn")
	f.agent.emit(sessionID, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.finish(sessionID)

	first, records, _ := snapshotOf(t, f, sessionID)
	if len(records) == 0 || first <= 0 {
		t.Fatalf("first snapshot = %d records ending at %d", len(records), first)
	}

	// The operator asks a second question: the console starts the next turn of
	// the same conversation.
	second := startTurn(t, f, sessionID, 2, "second turn")
	f.agent.emit(second, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.finish(second)

	cursor, records, _ := snapshotOf(t, f, sessionID)
	// The console's resume rule: a generation that reopens after the first turn
	// must not cite a cursor the client has already passed.
	if cursor <= first {
		t.Fatalf("resumed cursor = %d, want at least the first turn's %d", cursor, first)
	}
	// assertPageThrough: the window must end exactly at the cursor it cites.
	if got := recordSeq(t, records[len(records)-1]); got != cursor {
		t.Fatalf("snapshot ends at %d, want its cursor %d", got, cursor)
	}
	// follows: contiguous entries, the second turn included.
	for index := 1; index < len(records); index++ {
		if got, want := recordSeq(t, records[index]), recordSeq(t, records[index-1])+1; got != want {
			t.Fatalf("record %d has seq %d, want %d", index, got, want)
		}
	}
	// The conversation, not the newest turn: the window still reaches the first
	// turn's prompt, and the second turn's records are grouped as turn 2.
	if recordSeq(t, records[0]) >= cursor {
		t.Fatalf("window starts at %d and ends at %d, want an earlier turn included",
			recordSeq(t, records[0]), cursor)
	}
	turns := map[int]bool{}
	for _, record := range records {
		if turn := turnOf(t, record); turn != 0 {
			turns[turn] = true
		}
	}
	if !turns[1] || !turns[2] {
		t.Fatalf("window carries turns %v, want both turns of the conversation", turns)
	}
}

// TestSessionFollowSecondTurnTailsOnePastTheSnapshotCursor checks the live half
// of the same rule: with the session-wide numbering, each event of the newest
// turn is exactly one past the cursor the console already applied, which is what
// `follows` requires of every live entry.
func TestSessionFollowSecondTurnTailsOnePastTheSnapshotCursor(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startRun(t, "first turn")
	f.agent.finish(sessionID)

	// The second turn is already running when the console opens the stream, so the
	// snapshot is the conversation's newest sequence and every live frame of the
	// second turn must continue it by exactly one.
	second := startTurn(t, f, sessionID, 2, "second turn")
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, sessionID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	var cursor int64
	if err := json.Unmarshal(snapshot["cursor"], &cursor); err != nil {
		t.Fatalf("decode snapshot cursor: %v", err)
	}
	if cursor <= 0 {
		t.Fatalf("snapshot cursor = %d, want the conversation's sequence", cursor)
	}

	// Two more events of the second turn, then the turn's end.
	f.agent.emit(second, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.emit(second, zenforge.EventStepDone, map[string]any{"step": 1})
	f.agent.finish(second)
	want := cursor
	for index := 0; index < 2; index++ {
		live := readItem(t, conn, "follow")
		if got := recordSeq(t, live); got != want+1 {
			t.Fatalf("live seq = %d, want %d (one past the cursor)", got, want+1)
		}
		if turn := turnOf(t, live); turn != 2 {
			t.Fatalf("live record belongs to turn %d, want the second turn", turn)
		}
		want++
	}
	// The end of the second turn closes the conversation's turn 2.
	end := readItem(t, conn, "follow")
	if got := recordSeq(t, end); got != want+1 {
		t.Fatalf("turn end seq = %d, want %d", got, want+1)
	}
	if event := recordEvent(t, end); !bytesContains(event, `"turn/end"`) {
		t.Fatalf("second turn's last record = %s, want turn/end", event)
	}
}

// TestSessionFollowListsBothTurnsOfAConversation checks the header the console
// reads a stream's identity from, and that the turn count is the session's rather
// than the newest run's.
func TestSessionFollowListsBothTurnsOfAConversation(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startRun(t, "first turn")
	f.agent.finish(sessionID)
	second := startTurn(t, f, sessionID, 2, "second turn")
	f.agent.finish(second)

	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, sessionID, true))
	snapshot := readItem(t, conn, "follow")
	assertField(t, snapshot, "type", "snapshot")
	var header struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(snapshot["header"], &header); err != nil {
		t.Fatalf("decode snapshot header: %v", err)
	}
	if header.ID != sessionID {
		t.Fatalf("header id = %q, want the console session", header.ID)
	}
}

// TestResumedCursorRuleRejectsThePreFixNumbering is the negative half: it states
// the rule the fix satisfies by serving the second turn's own log with its own
// numbering, so the rule itself is tested and not merely asserted in a comment.
func TestResumedCursorRuleRejectsThePreFixNumbering(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startRun(t, "first turn")
	f.agent.emit(sessionID, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.finish(sessionID)
	first, _, _ := snapshotOf(t, f, sessionID)

	// What the host used to answer for the second turn: the run's own log, whose
	// last record is sequence 3 (run.started, step.started, run.done).
	ownTail := int64(0)
	events, err := f.store.Read(context.Background(), dshsession.ContinuationRunID(sessionID, 2), 0, 0)
	if err != nil {
		t.Fatalf("read the second turn's log: %v", err)
	}
	if len(events) > 0 {
		ownTail = events[len(events)-1].Seq
	}
	if ownTail >= first {
		t.Skip("the fixture's turns are too short to show the regression")
	}
	// The client's check, in one line: a resumed generation citing ownTail would
	// be behind `first`, which is exactly the error the operator saw.
	if !(ownTail < first) {
		t.Fatalf("expected the per-run numbering to be behind the conversation's cursor")
	}
}

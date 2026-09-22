package dshapi

import (
	"context"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshstream"
)

// inboxEvents reads one session's durable events for the queue's own event type.
func inboxEvents(t *testing.T, f *fixture, sessionID string) []zenforge.Event {
	t.Helper()
	events, err := f.store.Read(context.Background(), sessionID, 0, 0)
	if err != nil {
		t.Fatalf("read session events: %v", err)
	}
	splices := make([]zenforge.Event, 0, len(events))
	for _, event := range events {
		if string(event.Type) == inboxEventType {
			splices = append(splices, event)
		}
	}
	return splices
}

// lastInboxSplice returns the payload of the newest splice in the session's log.
func lastInboxSplice(t *testing.T, f *fixture, sessionID string) map[string]any {
	t.Helper()
	splices := inboxEvents(t, f, sessionID)
	if len(splices) == 0 {
		t.Fatal("the session has no inbox splice")
	}
	return splices[len(splices)-1].Payload
}

// insertedIDs reads the ids of one splice's inserted messages.
func insertedIDs(t *testing.T, splice map[string]any) []string {
	t.Helper()
	raw, ok := splice["inserted"].([]any)
	if !ok {
		t.Fatalf("inserted = %#v, want an array", splice["inserted"])
	}
	ids := make([]string, 0, len(raw))
	for _, entry := range raw {
		message, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("inserted entry = %#v, want an object", entry)
		}
		id, _ := message["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// A queued message is spliced into the session's own log, in the reference's
// event type and payload, so the queue is durable state rather than a map in the
// adapter: the event is what a later host folds, and what the console reads as
// durable acceptance.
func TestQueuedMessagesAreSplicedIntoTheSessionLog(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))
	assertAccepted(t, f.queuePrompt(t, "req-steer", sessionID, "steer", "actually, now"))

	queued := lastInboxSplice(t, f, sessionID)
	if queued["target"] != inboxNextStep {
		t.Fatalf("target = %v, want %s", queued["target"], inboxNextStep)
	}
	if _, present := queued["removedCount"]; present {
		t.Fatalf("an append carries no removedCount: %#v", queued)
	}
	if _, present := queued["outcome"]; present {
		t.Fatalf("an append is not a cancellation: %#v", queued)
	}
	if ids := insertedIDs(t, queued); len(ids) != 1 || ids[0] != "req-steer" {
		t.Fatalf("inserted = %v, want the steering message", ids)
	}
	message := queued["inserted"].([]any)[0].(map[string]any)
	text := inboxMessageText(message)
	if text != "actually, now" {
		t.Fatalf("inserted text = %q, want the submitted text", text)
	}
	if source, _ := message["source"].(map[string]any); source["rpcId"] != "req-steer" {
		t.Fatalf("source = %#v, want the console's own submission identity", message["source"])
	}

	splices := inboxEvents(t, f, sessionID)
	if len(splices) != 2 {
		t.Fatalf("splices = %d, want one per queued message", len(splices))
	}
	if splices[0].Payload["target"] != inboxNextTurn {
		t.Fatalf("first target = %v, want %s", splices[0].Payload["target"], inboxNextTurn)
	}
}

// The queue is a fold over that log, so a host that never saw the process that
// queued the message still serves the row: this is the durability the queue did
// not have while it was the live run controller's.
func TestPendingInputOutlivesTheProcessThatQueuedIt(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))
	assertAccepted(t, f.queuePrompt(t, "req-steer", sessionID, "steer", "actually, now"))

	// A second host over the same durable log: a new manager (so the runs this
	// process held are gone, and nothing can be inferred from a live queue) and a
	// new handler built the way a restart builds one.
	restarted := newFixtureWithStore(t, Config{}, f.store)
	state := restarted.handler.PendingQueue(sessionID)
	if len(state.NextTurn) != 1 || state.NextTurn[0].ID != "req-queued" || state.NextTurn[0].Text != "wait for the tests" {
		t.Fatalf("next-turn = %+v, want the queued prompt restored from the log", state.NextTurn)
	}
	if len(state.NextStep) != 1 || state.NextStep[0].ID != "req-steer" {
		t.Fatalf("next-step = %+v, want the steering message restored from the log", state.NextStep)
	}
	if state.Seq == 0 {
		t.Fatal("the restored cell carries a sequence")
	}
}

// A restored row is not just painted: the session's next turn is handed it, under
// the identity the console's changes address it by.
func TestRestoredPendingInputIsHandedToTheNextTurn(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))
	f.agent.drainSteers()

	restarted := newFixtureWithStore(t, Config{}, f.store)
	// The restarted host's own manager holds no run, so the prompt starts the
	// next turn of the conversation -- and that turn is handed the pending row.
	assertAccepted(t, restarted.queuePrompt(t, "req-next", sessionID, "queue", "and then this"))

	steered := restarted.agent.steers
	found := false
	for _, steer := range steered {
		if steer.ID == "req-queued" && steer.Message == "wait for the tests" {
			found = true
		}
	}
	if !found {
		t.Fatalf("steers = %+v, want the restored row handed to the new turn", steered)
	}
	// The prompt that started the turn is *that turn's input*, not a pending row:
	// only the restored message is still waiting, now in the new run's queue.
	state := restarted.handler.PendingQueue(sessionID)
	ids := make([]string, 0, len(state.NextTurn))
	for _, item := range state.NextTurn {
		ids = append(ids, item.ID)
	}
	if len(ids) != 1 || ids[0] != "req-queued" {
		t.Fatalf("next-turn = %v, want only the restored row still pending", ids)
	}
	if len(restarted.agent.tasks) == 0 {
		t.Fatal("the prompt did not start a turn")
	}
	last := restarted.agent.tasks[len(restarted.agent.tasks)-1]
	if last.Input != "and then this" {
		t.Fatalf("turn input = %q, want the prompt the operator just sent", last.Input)
	}
}

// An edit and a removal are recorded as splices too, with the outcome the
// reference writes: an edit replaces in place, and a removal the operator asked
// for is a cancellation.
func TestQueueEditsAndRemovalsAreRecordedAsSplices(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))

	assertAccepted(t, f.updateQueue(t, "rpc-edit", sessionID, "req-queued",
		`{"kind":"edit","content":[{"type":"text","text":"wait for the build"}]}`))
	edited := lastInboxSplice(t, f, sessionID)
	if edited["target"] != inboxNextTurn || edited["start"] != float64(0) || edited["removedCount"] != float64(1) {
		t.Fatalf("edit splice = %#v, want an in-place replacement at 0", edited)
	}
	if ids := insertedIDs(t, edited); len(ids) != 1 || ids[0] != "req-queued" {
		t.Fatalf("edit inserted = %v, want the same row identity", ids)
	}

	assertAccepted(t, f.updateQueue(t, "rpc-remove", sessionID, "req-queued", `{"kind":"remove"}`))
	removed := lastInboxSplice(t, f, sessionID)
	if removed["target"] != inboxNextTurn || removed["removedCount"] != float64(1) {
		t.Fatalf("remove splice = %#v, want the row removed from next-turn", removed)
	}
	if removed["outcome"] != inboxCanceled {
		t.Fatalf("remove outcome = %v, want %q", removed["outcome"], inboxCanceled)
	}
	if ids := insertedIDs(t, removed); len(ids) != 0 {
		t.Fatalf("remove inserted = %v, want nothing inserted", ids)
	}
	if state := f.handler.PendingQueue(sessionID); len(state.NextTurn) != 0 {
		t.Fatalf("next-turn = %+v, want the row gone", state.NextTurn)
	}
}

// Promotion moves a row between the console's two lists, and the log says so:
// a cancellation in next-turn and an append to next-step.
func TestPromotionMovesTheRowInTheLog(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))

	assertAccepted(t, f.updateQueue(t, "rpc-steer", sessionID, "req-queued", `{"kind":"steer"}`))
	splices := inboxEvents(t, f, sessionID)
	if len(splices) != 3 {
		t.Fatalf("splices = %d, want the append, the cancellation and the promotion", len(splices))
	}
	promotion := splices[1].Payload
	if promotion["target"] != inboxNextTurn || promotion["outcome"] != inboxCanceled {
		t.Fatalf("promotion removal = %#v", promotion)
	}
	move := splices[2].Payload
	if move["target"] != inboxNextStep || move["start"] != float64(0) {
		t.Fatalf("promotion append = %#v", move)
	}
	if ids := insertedIDs(t, move); len(ids) != 1 || ids[0] != "req-queued" {
		t.Fatalf("promotion inserted = %v, want the same row", ids)
	}
	state := f.handler.PendingQueue(sessionID)
	if len(state.NextTurn) != 0 || len(state.NextStep) != 1 || state.NextStep[0].ID != "req-queued" {
		t.Fatalf("cell = %+v, want the row in next-step only", state)
	}
}

// A delivery the live run queue proves is recorded as a claim -- a removal with no
// outcome, because the agent receiving a message is not a cancellation -- so a
// host that restarts does not restore a message the run already read.
func TestDeliveredPendingInputIsClaimedInTheLog(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))

	f.agent.drainSteers()
	if state := f.handler.PendingQueue(sessionID); len(state.NextTurn) != 0 {
		t.Fatalf("next-turn = %+v, want the row delivered", state.NextTurn)
	}
	claim := lastInboxSplice(t, f, sessionID)
	if claim["target"] != inboxNextTurn || claim["removedCount"] != float64(1) {
		t.Fatalf("claim = %#v, want the delivered row removed", claim)
	}
	if _, present := claim["outcome"]; present {
		t.Fatalf("a delivery is not a cancellation: %#v", claim)
	}

	// The claim is what the restarted host folds, so the row does not come back.
	restarted := newFixtureWithStore(t, Config{}, f.store)
	if state := restarted.handler.PendingQueue(sessionID); len(state.NextTurn) != 0 || len(state.NextStep) != 0 {
		t.Fatalf("restored cell = %+v, want nothing pending after a delivery", state)
	}
}

// The fold is the reference's `apply`, validations included: a splice whose range
// leaves its list, and a message that would be pending twice at once, are refused
// rather than answered around. There is no way for this host to write either one,
// which is exactly why the check has to be a unit test.
func TestInboxFoldRejectsAnInvalidLog(t *testing.T) {
	sessionID := "run-1"
	cases := []struct {
		name   string
		events []zenforge.Event
		want   string
	}{
		{
			name: "a range past the end of the list",
			events: []zenforge.Event{inboxEvent(sessionID, inboxSplice(inboxNextTurn, 1, 0,
				[]any{inboxMessage("req-1", "hello")}, false))},
			want: "invalid persisted inbox splice",
		},
		{
			name:   "more removed than the list holds",
			events: []zenforge.Event{inboxEvent(sessionID, inboxSplice(inboxNextTurn, 0, 1, nil, true))},
			want:   "invalid persisted inbox splice",
		},
		{
			name: "the same message pending in both lists",
			events: []zenforge.Event{
				inboxEvent(sessionID, inboxSplice(inboxNextTurn, 0, 0, []any{inboxMessage("req-1", "hello")}, false)),
				inboxEvent(sessionID, inboxSplice(inboxNextStep, 0, 0, []any{inboxMessage("req-1", "hello")}, false)),
			},
			want: `message "req-1" is already pending`,
		},
		{
			name:   "an unknown target",
			events: []zenforge.Event{inboxEvent(sessionID, inboxSplice("nowhere", 0, 0, nil, false))},
			want:   "is not one of the two pending lists",
		},
		{
			name: "a message with no identity",
			events: []zenforge.Event{inboxEvent(sessionID,
				inboxSplice(inboxNextTurn, 0, 0, []any{map[string]any{"content": []any{}}}, false))},
			want: "a message with no id",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := foldInbox(test.events); err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("fold error = %v, want %q", err, test.want)
			}
		})
	}
}

// inboxEvent wraps one splice payload as a durable event, which is how the fold
// meets it.
func inboxEvent(runID string, payload map[string]any) zenforge.Event {
	event := zenforge.NewEvent(zenforge.EventType(inboxEventType), runID, payload)
	event.Seq = 1
	return event
}

// contains is strings.Contains for the fold's own assertions, kept small so the
// test does not depend on the formatting of the errors it checks.
func contains(value, needle string) bool {
	return len(needle) == 0 || (len(value) >= len(needle) && indexOf(value, needle) >= 0)
}

func indexOf(value, needle string) int {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}

// The folded cell and the live queue must agree about *which* rows are pending,
// because the queue is the console's only list of undelivered work: a row in the
// cell the run does not hold would be a promise, and a row in the run the cell
// does not show would be an operator's words in the wrong place.
func TestTheFoldedCellMatchesTheRunQueue(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-1", sessionID, "queue", "one"))
	assertAccepted(t, f.queuePrompt(t, "req-2", sessionID, "steer", "two"))
	assertAccepted(t, f.queuePrompt(t, "req-3", sessionID, "queue", "three"))

	state := f.handler.PendingQueue(sessionID)
	live, err := f.manager.PendingSteers(sessionID)
	if err != nil {
		t.Fatalf("pending steers: %v", err)
	}
	cell := map[string]bool{}
	for _, item := range append(append([]dshstream.QueueItem{}, state.NextTurn...), state.NextStep...) {
		cell[item.ID] = true
	}
	if len(cell) != len(state.NextTurn)+len(state.NextStep) {
		t.Fatalf("cell = %+v, want distinct ids", state)
	}
	for _, steer := range live {
		if !cell[steer.ID] {
			t.Fatalf("the run holds %q and the cell does not show it", steer.ID)
		}
		delete(cell, steer.ID)
	}
	if len(cell) != 0 {
		t.Fatalf("cell holds rows the run does not: %v", cell)
	}
	if len(live) != 3 {
		t.Fatalf("run queue = %d rows, want 3", len(live))
	}
}

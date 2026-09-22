package dshstream

import (
	"sync"
	"testing"
)

// queueSource is a stub queue source: one session's pending cell, and the
// observer list a test drives the way the RPC handler's store does.
type queueSource struct {
	mu        sync.Mutex
	states    map[string]QueueState
	observers []func(QueueUpdate)
}

func (s *queueSource) Queue(sessionID string) QueueState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[sessionID]
}

func (s *queueSource) Updates(observe func(QueueUpdate)) func() {
	s.mu.Lock()
	s.observers = append(s.observers, observe)
	s.mu.Unlock()
	return func() {}
}

func (s *queueSource) set(sessionID string, state QueueState) {
	s.mu.Lock()
	s.states[sessionID] = state
	s.mu.Unlock()
}

func (s *queueSource) publish(update QueueUpdate) {
	s.mu.Lock()
	s.states[update.SessionID] = update.State
	observers := append([]func(QueueUpdate){}, s.observers...)
	s.mu.Unlock()
	for _, observe := range observers {
		observe(update)
	}
}

func queueStreamFixture(t *testing.T) (*fixture, *queueSource) {
	t.Helper()
	source := &queueSource{states: map[string]QueueState{}}
	f := newFixture(t, Config{Queue: source.Queue, QueueUpdates: source.Updates})
	return f, source
}

// Both console surfaces read the `inbox` cell -- the queue dock lists the
// `next-turn` rows with their actions, the chat view shows `next-step` as
// pending steering -- so the follow snapshot has to carry it, or a console that
// reconnects while messages are queued paints them from its own echoes only and
// the host's rows never appear.
func TestSessionFollowSnapshotCarriesThePendingQueue(t *testing.T) {
	f, source := queueStreamFixture(t)
	runID := f.startRun(t, "hello")
	// The console follows the run it opened, so the cell is keyed by that id.
	source.set(runID, QueueState{
		NextTurn: []QueueItem{{ID: "req-queued", Text: "wait for the tests"}},
		NextStep: []QueueItem{{ID: "req-steer", Text: "actually, now"}},
		Seq:      1_700_000_200_000,
	})
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))

	snapshot := readItem(t, conn, "follow")
	projections := decodeValueObject(t, snapshot["projections"])
	values := decodeValueObject(t, projections["values"])
	cell, ok := values[inboxProjectionKey]
	if !ok {
		t.Fatalf("values = %v, want the inbox cell", values)
	}
	// The block's watermark stays the snapshot cursor: the cell is current as of
	// the page the client just received, while the queue's own sequence orders its
	// live frames (the same rule the goal cell follows).
	if got, want := intField(t, projections, "asOfSeq"), intField(t, snapshot, "cursor"); got != want {
		t.Fatalf("asOfSeq = %d, want the snapshot cursor %d", got, want)
	}
	lists := decodeValueObject(t, cell)
	turn := decodeValueList(t, lists["next-turn"])
	if len(turn) != 1 {
		t.Fatalf("next-turn = %v, want one queued prompt", lists["next-turn"])
	}
	queued := decodeValueObject(t, turn[0])
	// The id is the steer id the prompt path queued the message under, and the
	// source carries the same value as the prompt identity: the console retires
	// its local echo on it and sends it back as the item id when it edits the row.
	assertField(t, queued, "id", "req-queued")
	assertField(t, queued, "role", "user")
	assertField(t, decodeValueObject(t, decodeValueList(t, queued["content"])[0]), "text", "wait for the tests")
	assertField(t, decodeValueObject(t, queued["source"]), "kind", "user")
	assertField(t, decodeValueObject(t, queued["source"]), "rpcId", "req-queued")
	step := decodeValueList(t, decodeValueObject(t, cell)["next-step"])
	if len(step) != 1 {
		t.Fatalf("next-step = %v, want one steering message", lists["next-step"])
	}
	assertField(t, decodeValueObject(t, step[0]), "id", "req-steer")
}

// A session with nothing queued still carries the key, because an absent key is
// how the console learns the capability does not exist: it would then never look
// for a row, and a later queued message would have nothing to appear in.
func TestSessionFollowSnapshotCarriesTheEmptyQueueCell(t *testing.T) {
	f, _ := queueStreamFixture(t)
	runID := f.startRun(t, "hello")
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))

	snapshot := readItem(t, conn, "follow")
	values := decodeValueObject(t, decodeValueObject(t, snapshot["projections"])["values"])
	cell, ok := values[inboxProjectionKey]
	if !ok {
		t.Fatalf("values = %v, want the inbox key present even with an empty queue", values)
	}
	lists := decodeValueObject(t, cell)
	if len(decodeValueList(t, lists["next-turn"])) != 0 || len(decodeValueList(t, lists["next-step"])) != 0 {
		t.Fatalf("cell = %s, want both lists empty", cell)
	}
}

// The control baseline deliberately keeps the queue out of its projection block:
// the block carries one watermark, and the queue's frames are numbered by the
// queue's own counter. It travels with the session's snapshot instead, and later
// values arrive as frames -- which is what this pins.
func TestSessionControlPublishesQueueChangesOutsideTheBaseline(t *testing.T) {
	source := &queueSource{states: map[string]QueueState{}}
	selections := &selectionSource{states: map[string]ModelSelectionState{}}
	f := newFixture(t, Config{
		Queue:                 source.Queue,
		QueueUpdates:          source.Updates,
		ModelSelections:       selections.States,
		ModelSelectionUpdates: selections.Updates,
	})
	const sessionID = "run-shared"
	source.set(sessionID, QueueState{NextTurn: []QueueItem{{ID: "req-1", Text: "queued"}}, Seq: 1_700_000_300_000})
	selections.set(sessionID, ModelSelectionState{
		Projection: ModelSelectionProjection{Next: &ModelSelection{Provider: "acme", Model: "acme-large"}},
		Seq:        4,
	})

	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", "")
	value := readItem(t, conn, "control")
	baseline := decodeValueObject(t, value["value"])
	block := decodeValueObject(t, decodeValueObject(t, baseline["projections"])[sessionID])
	if seq := intField(t, block, "asOfSeq"); seq != 4 {
		t.Fatalf("asOfSeq = %d, want the selection's own sequence 4", seq)
	}
	if _, ok := decodeValueObject(t, block["values"])[inboxProjectionKey]; ok {
		t.Fatal("the control baseline carried the queue cell, which would raise the block watermark")
	}
	// The queues mirror stays empty: the cell is this host's served path, and the
	// newer mirror is a shape nothing in the pinned bundle reads.
	if got := string(baseline["queues"]); got != "{}" {
		t.Fatalf("queues = %s, want {}", got)
	}

	source.publish(QueueUpdate{
		SessionID: sessionID,
		State:     QueueState{NextStep: []QueueItem{{ID: "req-1", Text: "queued"}}, Seq: 1_700_000_400_000},
	})
	frame := readItem(t, conn, "control")
	assertField(t, frame, "type", "projection")
	assertField(t, frame, "sessionId", sessionID)
	assertField(t, frame, "key", inboxProjectionKey)
	if seq := intField(t, frame, "seq"); seq != 1_700_000_400_000 {
		t.Fatalf("frame seq = %d, want the queue's own sequence", seq)
	}
	lists := decodeValueObject(t, frame["value"])
	step := decodeValueList(t, lists["next-step"])
	if len(step) != 1 || len(decodeValueList(t, lists["next-turn"])) != 0 {
		t.Fatalf("frame value = %s, want the steering list only", frame["value"])
	}
}
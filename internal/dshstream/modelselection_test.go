package dshstream

import (
	"sync"
	"testing"
)

// selectionSource is a stub model-selection source: one session's projection,
// and an observer list the test can drive.
type selectionSource struct {
	mu        sync.Mutex
	states    map[string]ModelSelectionState
	observers []func(ModelSelectionUpdate)
}

func (s *selectionSource) States() map[string]ModelSelectionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make(map[string]ModelSelectionState, len(s.states))
	for sessionID, state := range s.states {
		states[sessionID] = state
	}
	return states
}

func (s *selectionSource) Updates(observe func(ModelSelectionUpdate)) func() {
	s.mu.Lock()
	s.observers = append(s.observers, observe)
	s.mu.Unlock()
	return func() {}
}

func (s *selectionSource) set(sessionID string, state ModelSelectionState) {
	s.mu.Lock()
	s.states[sessionID] = state
	s.mu.Unlock()
}

func (s *selectionSource) publish(update ModelSelectionUpdate) {
	s.mu.Lock()
	observers := append([]func(ModelSelectionUpdate){}, s.observers...)
	s.mu.Unlock()
	for _, observe := range observers {
		observe(update)
	}
}

func selectionStreamFixture(t *testing.T, sessionID string) (*fixture, *selectionSource) {
	t.Helper()
	source := &selectionSource{states: map[string]ModelSelectionState{}}
	f := newFixture(t, Config{
		ModelSelections:       source.States,
		ModelSelectionUpdates: source.Updates,
	})
	source.set(sessionID, ModelSelectionState{
		Projection: ModelSelectionProjection{Next: &ModelSelection{Provider: "acme", Model: "acme-large"}},
		Seq:        1,
	})
	return f, source
}

// The model picker renders `projected.next ?? catalog.default`, and the control
// stream is where that projection comes from, so a baseline without it leaves the
// composer showing the host default no matter what was selected.
func TestSessionControlBaselinePublishesModelSelections(t *testing.T) {
	const sessionID = "run-selection"
	f, _ := selectionStreamFixture(t, sessionID)
	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", "")

	value := readItem(t, conn, "control")
	baseline := decodeValueObject(t, value["value"])
	projections := decodeValueObject(t, baseline["projections"])
	entry, ok := projections[sessionID]
	if !ok {
		t.Fatalf("projections = %v, want the session's baseline", projections)
	}
	block := decodeValueObject(t, entry)
	if seq := intField(t, block, "asOfSeq"); seq != 1 {
		t.Fatalf("asOfSeq = %d, want the selection's own sequence 1", seq)
	}
	values := decodeValueObject(t, block["values"])
	projection := decodeValueObject(t, values[modelSelectionProjectionKey])
	assertField(t, decodeValueObject(t, projection["next"]), "provider", "acme")
	if got := string(projection["lastUsed"]); got != "null" {
		t.Fatalf("lastUsed = %s, want null before a run consumed the selection", got)
	}
}

// A selection made while the console is open arrives as a projection frame: the
// session stream has no frame of its own for it.
func TestSessionControlPublishesLaterSelections(t *testing.T) {
	const sessionID = "run-selection"
	f, source := selectionStreamFixture(t, sessionID)
	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", "")
	readItem(t, conn, "control")

	lastUsed := ModelSelection{Provider: "acme", Model: "acme-large"}
	source.publish(ModelSelectionUpdate{
		SessionID:  sessionID,
		Projection: ModelSelectionProjection{LastUsed: &lastUsed, Next: &lastUsed},
		Seq:        2,
	})
	frame := readItem(t, conn, "control")
	assertField(t, frame, "type", "projection")
	assertField(t, frame, "sessionId", sessionID)
	assertField(t, frame, "key", modelSelectionProjectionKey)
	if seq := intField(t, frame, "seq"); seq != 2 {
		t.Fatalf("seq = %d, want 2", seq)
	}
	projection := decodeValueObject(t, frame["value"])
	assertField(t, decodeValueObject(t, projection["lastUsed"]), "model", "acme-large")
}

// The follow snapshot carries the followed session's projection too, so a client
// that only follows a session still renders the selection.
func TestSessionFollowSnapshotCarriesTheModelSelection(t *testing.T) {
	f, source := selectionStreamFixture(t, "run-followed")
	runID := f.startRun(t, "hello")
	// The projection is keyed by the run the console follows.
	source.set(runID, ModelSelectionState{
		Projection: ModelSelectionProjection{Next: &ModelSelection{Provider: "acme", Model: "acme-large"}},
		Seq:        3,
	})
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	projections := decodeValueObject(t, snapshot["projections"])
	values := decodeValueObject(t, projections["values"])
	if _, ok := values[modelSelectionProjectionKey]; !ok {
		t.Fatalf("values = %v, want the model selection", values)
	}
	projection := decodeValueObject(t, values[modelSelectionProjectionKey])
	assertField(t, decodeValueObject(t, projection["next"]), "provider", "acme")
	// The watermark is the snapshot's own cursor: the value is current as of the
	// page the client just received.
	if got, want := intField(t, projections, "asOfSeq"), intField(t, snapshot, "cursor"); got != want {
		t.Fatalf("asOfSeq = %d, want the snapshot cursor %d", got, want)
	}
}

// A session with no selection carries no value: an absent key means the
// capability has no value at this cursor, which is what the client expects.
func TestSessionFollowSnapshotOmitsAnUnselectedProjection(t *testing.T) {
	f, _ := selectionStreamFixture(t, "run-other")
	runID := f.startRun(t, "hello")
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))
	snapshot := readItem(t, conn, "follow")
	projections := decodeValueObject(t, snapshot["projections"])
	values := decodeValueObject(t, projections["values"])
	if len(values) != 0 {
		t.Fatalf("values = %v, want no projection for a session that selected nothing", values)
	}
}

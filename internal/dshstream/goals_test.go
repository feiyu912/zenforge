package dshstream

import (
	"encoding/json"
	"sync"
	"testing"
)

// goalSource is a stub goal source: one session's projection cell, and an
// observer list the test drives the way consoleGoals does.
type goalSource struct {
	mu          sync.Mutex
	projections map[string]*GoalProjection
	observers   []func(GoalUpdate)
}

func (s *goalSource) Projection(sessionID string) *GoalProjection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projections[sessionID]
}

func (s *goalSource) Updates(observe func(GoalUpdate)) func() {
	s.mu.Lock()
	s.observers = append(s.observers, observe)
	s.mu.Unlock()
	return func() {}
}

func (s *goalSource) set(sessionID string, projection *GoalProjection) {
	s.mu.Lock()
	s.projections[sessionID] = projection
	s.mu.Unlock()
}

func (s *goalSource) publish(update GoalUpdate) {
	s.mu.Lock()
	s.projections[update.SessionID] = update.Projection
	observers := append([]func(GoalUpdate){}, s.observers...)
	s.mu.Unlock()
	for _, observe := range observers {
		observe(update)
	}
}

// goalProjection is the projection a paused, revision-three goal serves: every
// field the dock renders, so a dropped field is visible rather than defaulted.
func goalProjection() *GoalProjection {
	return &GoalProjection{
		Goal: GoalSnapshot{
			ID:            "goal-1",
			Revision:      3,
			Objective:     "ship the goal dock",
			Phase:         "paused",
			MaxGoalRounds: 8,
			BlockedReason: &GoalBlockedReason{Code: "blocked-rounds-remaining", Message: "waiting on review"},
		},
		RoundsStarted: 2,
		CreatedAt:     1_700_000_000_000,
		UpdatedAt:     1_700_000_060_000,
	}
}

func goalStreamFixture(t *testing.T) (*fixture, *goalSource) {
	t.Helper()
	source := &goalSource{projections: map[string]*GoalProjection{}}
	f := newFixture(t, Config{Goals: source.Projection, GoalUpdates: source.Updates})
	return f, source
}

// The dock renders `useProjection("goal")`, and a session opened without a goal
// cell would show nothing until the first mutation. The follow snapshot is where
// the cell comes from, because the control baseline's one watermark per block
// cannot order a goal's own sequence against the model-selection cell it shares
// the block with.
func TestSessionFollowSnapshotCarriesTheGoal(t *testing.T) {
	f, source := goalStreamFixture(t)
	runID := f.startRun(t, "hello")
	// The console follows the run it opened, so the cell is keyed by that id.
	source.set(runID, goalProjection())
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))

	snapshot := readItem(t, conn, "follow")
	projections := decodeValueObject(t, snapshot["projections"])
	values := decodeValueObject(t, projections["values"])
	if _, ok := values[goalProjectionKey]; !ok {
		t.Fatalf("values = %v, want the goal cell", values)
	}
	projection := decodeValueObject(t, values[goalProjectionKey])
	goal := decodeValueObject(t, projection["goal"])
	assertField(t, goal, "id", "goal-1")
	assertField(t, goal, "phase", "paused")
	if revision := intField(t, goal, "revision"); revision != 3 {
		t.Fatalf("revision = %d, want 3", revision)
	}
	if rounds := intField(t, projection, "roundsStarted"); rounds != 2 {
		t.Fatalf("roundsStarted = %d, want 2", rounds)
	}
	assertField(t, decodeValueObject(t, goal["blockedReason"]), "message", "waiting on review")
	// The block's watermark stays the snapshot cursor: the cell is current as of
	// the page the client just received, while the goal's own sequence orders its
	// live frames.
	if got, want := intField(t, projections, "asOfSeq"), intField(t, snapshot, "cursor"); got != want {
		t.Fatalf("asOfSeq = %d, want the snapshot cursor %d", got, want)
	}
}

// A session with no goal carries the key with the null arm, which is upstream's
// own `GoalProjection | null` before the first create and after a clear. An
// absent key would tell the client the capability does not exist.
func TestSessionFollowSnapshotCarriesTheNullGoalCell(t *testing.T) {
	f, _ := goalStreamFixture(t)
	runID := f.startRun(t, "hello")
	conn := f.mustDial(t)
	openStream(t, conn, "follow", "session/follow", followArgs(t, runID, true))

	snapshot := readItem(t, conn, "follow")
	values := decodeValueObject(t, decodeValueObject(t, snapshot["projections"])["values"])
	raw, ok := values[goalProjectionKey]
	if !ok {
		t.Fatalf("values = %v, want the goal key present even with no goal", values)
	}
	if got := string(raw); got != "null" {
		t.Fatalf("goal cell = %s, want null", got)
	}
}

// The control baseline carries the model-selection cell and not the goal cell,
// and the sequence it advertises is the selection's own. Folding the goal in
// would raise the block's watermark above the selection store's sequence and
// freeze the model picker against its own later frames.
func TestSessionControlBaselineKeepsTheGoalOutOfTheBlock(t *testing.T) {
	source := &goalSource{projections: map[string]*GoalProjection{}}
	selections := &selectionSource{states: map[string]ModelSelectionState{}}
	f := newFixture(t, Config{
		Goals:                 source.Projection,
		GoalUpdates:           source.Updates,
		ModelSelections:       selections.States,
		ModelSelectionUpdates: selections.Updates,
	})
	const sessionID = "run-shared"
	source.set(sessionID, goalProjection())
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
	values := decodeValueObject(t, block["values"])
	if _, ok := values[goalProjectionKey]; ok {
		t.Fatalf("values = %v, want no goal cell in the control baseline", values)
	}
	if _, ok := values[modelSelectionProjectionKey]; !ok {
		t.Fatalf("values = %v, want the model selection", values)
	}
}

// A mutation committed while the console is open arrives as a projection frame
// carrying the goal's own sequence.
func TestSessionControlPublishesLaterGoalMutations(t *testing.T) {
	const sessionID = "run-goal"
	f, source := goalStreamFixture(t)
	source.set(sessionID, goalProjection())
	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", "")
	readItem(t, conn, "control")

	changed := goalProjection()
	changed.Goal.Phase = "active"
	source.publish(GoalUpdate{SessionID: sessionID, Projection: changed, Seq: 1_700_000_100_000, Activation: "armed"})

	frame := readItem(t, conn, "control")
	assertField(t, frame, "type", "projection")
	assertField(t, frame, "sessionId", sessionID)
	assertField(t, frame, "key", goalProjectionKey)
	if seq := intField(t, frame, "seq"); seq != 1_700_000_100_000 {
		t.Fatalf("seq = %d, want the goal's own sequence", seq)
	}
	goal := decodeValueObject(t, decodeValueObject(t, frame["value"])["goal"])
	assertField(t, goal, "phase", "active")
}

// A cleared goal is the null arm on the wire, not an absent key: the dock hides
// on null and an absent key would read as a host that cannot show goals at all.
func TestSessionControlPublishesAClearedGoalAsNull(t *testing.T) {
	const sessionID = "run-goal"
	f, source := goalStreamFixture(t)
	source.set(sessionID, goalProjection())
	conn := f.mustDial(t)
	openStream(t, conn, "control", "session/control", "")
	readItem(t, conn, "control")

	source.publish(GoalUpdate{SessionID: sessionID, Projection: nil, Seq: 2})

	frame := readItem(t, conn, "control")
	assertField(t, frame, "key", goalProjectionKey)
	if got := string(frame["value"]); got != "null" {
		t.Fatalf("value = %s, want null", got)
	}
}

// The dock subscribes to `goal/activation-changed` and reads `{sessionId, goal}`.
// The emit is the only way it learns the exact revision and activation without
// asking for a fresh read, so the payload is asserted key by key.
func TestEventsDeliversGoalActivationChanged(t *testing.T) {
	const sessionID = "run-goal"
	f, source := goalStreamFixture(t)
	conn := f.mustDial(t)
	openEventsStream(t, conn, "events")

	source.publish(GoalUpdate{SessionID: sessionID, Projection: goalProjection(), Seq: 5, Activation: "disarmed"})

	item := readItem(t, conn, "events")
	assertField(t, item, "type", "emit")
	assertField(t, item, "event", "goal/activation-changed")
	args := decodeValueArray(t, item["args"])
	if len(args) != 1 {
		t.Fatalf("args = %v, want one payload", args)
	}
	payload := decodeValueObject(t, args[0])
	assertKeys(t, payload, "sessionId", "goal")
	assertField(t, payload, "sessionId", sessionID)
	goal := decodeValueObject(t, payload["goal"])
	assertKeys(t, goal, "id", "revision", "activation")
	assertField(t, goal, "id", "goal-1")
	assertField(t, goal, "activation", "disarmed")
	if revision := intField(t, goal, "revision"); revision != 3 {
		t.Fatalf("revision = %d, want 3", revision)
	}
}

// A clear tells the dock the activation is gone by omitting the goal object, the
// shape the client's own hook treats as "no live goal".
func TestEventsDeliversAGoalClearWithoutAGoalObject(t *testing.T) {
	const sessionID = "run-goal"
	f, source := goalStreamFixture(t)
	conn := f.mustDial(t)
	openEventsStream(t, conn, "events")

	source.publish(GoalUpdate{SessionID: sessionID, Projection: nil, Seq: 6})

	item := readItem(t, conn, "events")
	assertField(t, item, "event", "goal/activation-changed")
	args := decodeValueArray(t, item["args"])
	if len(args) != 1 {
		t.Fatalf("args = %v, want one payload", args)
	}
	payload := decodeValueObject(t, args[0])
	assertKeys(t, payload, "sessionId")
	assertField(t, payload, "sessionId", sessionID)
}

// decodeValueArray decodes one raw JSON value into an array of raw values.
func decodeValueArray(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("value is not a JSON array: %v", err)
	}
	return values
}

package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/goals"
	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshstream"
)

// goalsFixture builds the console's goal store over a real file store in a
// temporary directory: the translation this file tests is between the console's
// envelopes and the framework's durable state machine, so the durable half must
// be the real one.
func goalsFixture(t *testing.T) (*consoleGoals, string) {
	t.Helper()
	dir := t.TempDir()
	store := newConsoleGoals(goals.NewFileStore(filepath.Join(dir, "goals")))
	return store, "run-goal-session"
}

// createdGoal creates one goal and returns the ref the console needs for every
// following mutation.
func createdGoal(t *testing.T, store *consoleGoals, sessionID string) dshapi.GoalRef {
	t.Helper()
	created, err := store.Create(context.Background(), sessionID, dshapi.CreateGoalRequest{Objective: "ship the goal dock"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Ref.ID == "" || created.Ref.Revision != 1 {
		t.Fatalf("created = %+v, want a revision-one goal", created)
	}
	return created.Ref
}

// The store is the framework's state machine with the console's vocabulary, so
// the whole lifecycle is walked the way the dock walks it: create, read, edit,
// pause, resume, complete, clear. Each mutation advances exactly one revision,
// which is what the ref compare-and-set depends on.
func TestConsoleGoalsWalkTheLifecycle(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()

	ref := createdGoal(t, store, sessionID)

	view, ok, err := store.Goal(ctx, sessionID)
	if err != nil || !ok {
		t.Fatalf("read after create: ok=%v err=%v", ok, err)
	}
	if view.Phase != string(goals.PhaseActive) || view.Activation != "armed" {
		t.Fatalf("view = %+v, want an active armed goal", view)
	}
	if view.Objective != "ship the goal dock" || view.MaxGoalRounds < 1 {
		t.Fatalf("view = %+v, want the objective and a resolved round cap", view)
	}
	if view.CreatedAt == 0 || view.UpdatedAt == 0 {
		t.Fatalf("view = %+v, want timestamps in epoch milliseconds", view)
	}

	objective := "ship the goal dock, with evidence"
	edited, err := store.Edit(ctx, sessionID, ref, dshapi.EditGoalRequest{Objective: &objective})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Objective != objective || edited.Revision != 2 {
		t.Fatalf("edited = %+v, want the new objective at revision 2", edited)
	}

	paused, err := store.Pause(ctx, sessionID, dshapi.GoalRef{ID: edited.ID, Revision: edited.Revision})
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Phase != string(goals.PhasePaused) || paused.Activation != "disarmed" {
		t.Fatalf("paused = %+v, want a paused disarmed goal", paused)
	}

	resumed, err := store.Resume(ctx, sessionID, dshapi.GoalRef{ID: paused.ID, Revision: paused.Revision})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Phase != string(goals.PhaseActive) || resumed.Activation != "armed" {
		t.Fatalf("resumed = %+v, want an active armed goal", resumed)
	}

	completed, err := store.Complete(ctx, sessionID, dshapi.GoalRef{ID: resumed.ID, Revision: resumed.Revision})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Phase != string(goals.PhaseComplete) || completed.Activation != "disarmed" {
		t.Fatalf("completed = %+v, want a complete disarmed goal", completed)
	}

	cleared, err := store.Clear(ctx, sessionID, dshapi.GoalRef{ID: completed.ID, Revision: completed.Revision})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared != (dshapi.GoalRef{ID: completed.ID, Revision: completed.Revision}) {
		t.Fatalf("cleared = %+v, want the ref it removed", cleared)
	}
	if _, ok, err := store.Goal(ctx, sessionID); err != nil || ok {
		t.Fatalf("read after clear: ok=%v err=%v, want no goal", ok, err)
	}
	if projection := store.Projection(sessionID); projection != nil {
		t.Fatalf("projection after clear = %+v, want the null arm", projection)
	}
}

// The console's own compare-and-set: a mutation that names a revision the store
// has moved past is refused as stale rather than applied to whatever is current.
func TestConsoleGoalsRefuseAStaleRef(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()
	ref := createdGoal(t, store, sessionID)

	objective := "a later objective"
	if _, err := store.Edit(ctx, sessionID, ref, dshapi.EditGoalRequest{Objective: &objective}); err != nil {
		t.Fatalf("edit: %v", err)
	}

	_, err := store.Pause(ctx, sessionID, ref)
	assertGoalCode(t, err, dshapi.GoalCodeStaleRevision)

	// The refused call did not move the goal: the state is still revision two.
	view, _, err := store.Goal(ctx, sessionID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if view.Revision != 2 || view.Phase != string(goals.PhaseActive) {
		t.Fatalf("view = %+v, want the goal untouched by the refused pause", view)
	}
}

// A ref for a goal that is not the current one is missing, not stale, and the
// stale-ref-after-clear case is missing too because the store drops the document
// (ADR 0125 records the deviation from the reference's retained identity).
func TestConsoleGoalsReportMissingRefs(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()

	if _, err := store.Pause(ctx, sessionID, dshapi.GoalRef{ID: "goal-nope", Revision: 1}); err == nil {
		t.Fatal("pause without a goal succeeded")
	} else {
		assertGoalCode(t, err, dshapi.GoalCodeNotFound)
	}

	ref := createdGoal(t, store, sessionID)
	if _, err := store.Pause(ctx, sessionID, dshapi.GoalRef{ID: "goal-other", Revision: 1}); err == nil {
		t.Fatal("pause against another id succeeded")
	} else {
		assertGoalCode(t, err, dshapi.GoalCodeNotFound)
	}

	if _, err := store.Clear(ctx, sessionID, ref); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := store.Pause(ctx, sessionID, ref); err == nil {
		t.Fatal("pause after clear succeeded")
	} else {
		assertGoalCode(t, err, dshapi.GoalCodeNotFound)
	}
}

// A second goal while one is still current is the reference's
// GOAL_ALREADY_EXISTS; completing the current one makes room for the next.
func TestConsoleGoalsRefuseASecondCurrentGoal(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()
	ref := createdGoal(t, store, sessionID)

	if _, err := store.Create(ctx, sessionID, dshapi.CreateGoalRequest{Objective: "another"}); err == nil {
		t.Fatal("create while a goal is current succeeded")
	} else {
		assertGoalCode(t, err, dshapi.GoalCodeAlreadyExists)
	}

	completed, err := store.Complete(ctx, sessionID, ref)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	next, err := store.Create(ctx, sessionID, dshapi.CreateGoalRequest{Objective: "another", MaxGoalRounds: 3})
	if err != nil {
		t.Fatalf("create after complete: %v", err)
	}
	if next.Ref.ID == completed.ID || next.Ref.Revision != 1 {
		t.Fatalf("next = %+v, want a fresh goal at revision one", next.Ref)
	}
	view, _, err := store.Goal(ctx, sessionID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if view.Objective != "another" || view.MaxGoalRounds != 3 {
		t.Fatalf("view = %+v, want the second goal", view)
	}
}

// Lowering the round cap below the rounds already started is the framework's own,
// stricter rule. The console carries the refusal and its message rather than
// inventing a code for it: the reference's edit accepts any positive cap. The
// non-positive cap never reaches here, because the argument layer refuses it with
// the goal domain's own GOAL_INVALID_MAX_ROUNDS first.
func TestConsoleGoalsRefuseACapBelowTheStartedRounds(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()
	ref := createdGoal(t, store, sessionID)

	fileStore := store.store.(*goals.FileStore)
	state, err := fileStore.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Three rounds have run, which the durable state carries even though this host
	// starts no runs in this test.
	state.RoundsStarted = 3
	if err := fileStore.Save(ctx, sessionID, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	tooSmall := 2
	_, err = store.Edit(ctx, sessionID, ref, dshapi.EditGoalRequest{MaxGoalRounds: &tooSmall})
	assertGoalCode(t, err, dshapi.GoalCodeInvalidTransition)
	if err == nil || !strings.Contains(err.Error(), "rounds already started") {
		t.Fatalf("error = %v, want the framework's explanation of the collision", err)
	}

	// A cap at or above the started rounds is accepted.
	roomy := 6
	edited, err := store.Edit(ctx, sessionID, ref, dshapi.EditGoalRequest{MaxGoalRounds: &roomy})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.MaxGoalRounds != 6 || edited.RoundsStarted != 3 {
		t.Fatalf("edited = %+v, want the raised cap and the started rounds", edited)
	}
}

// The two live carriers read this store, so a commit has to reach both: the
// projection cell the dock renders, and the activation the emit names. The
// sequence is the store's own and has to outrank a session cursor, because the
// client discards a frame numbered at or below the watermark it seeded.
func TestConsoleGoalsPublishEveryCommit(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()

	type signal struct {
		projection *dshstream.GoalProjection
		seq        int64
		activation string
	}
	var seen []signal
	unsubscribe := store.Updates(func(update dshstream.GoalUpdate) {
		if update.SessionID != sessionID {
			t.Errorf("update session = %q, want %q", update.SessionID, sessionID)
		}
		seen = append(seen, signal{update.Projection, update.Seq, update.Activation})
	})
	defer unsubscribe()

	ref := createdGoal(t, store, sessionID)
	if len(seen) != 1 {
		t.Fatalf("updates = %d, want one for the create", len(seen))
	}
	if seen[0].projection == nil || seen[0].projection.Goal.Revision != 1 {
		t.Fatalf("created projection = %+v", seen[0].projection)
	}
	if seen[0].projection.Goal.Objective != "ship the goal dock" {
		t.Fatalf("projection = %+v, want the objective the dock renders", seen[0].projection)
	}
	if seen[0].activation != "armed" {
		t.Fatalf("activation = %q, want armed", seen[0].activation)
	}

	paused, err := store.Pause(ctx, sessionID, ref)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := store.Clear(ctx, sessionID, dshapi.GoalRef{ID: paused.ID, Revision: paused.Revision}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("updates = %d, want create, pause and clear", len(seen))
	}
	if seen[1].projection == nil || seen[1].projection.Goal.Phase != string(goals.PhasePaused) {
		t.Fatalf("paused projection = %+v", seen[1].projection)
	}
	if seen[1].activation != "disarmed" {
		t.Fatalf("paused activation = %q, want disarmed", seen[1].activation)
	}
	if seen[2].projection != nil {
		t.Fatalf("cleared projection = %+v, want the null arm", seen[2].projection)
	}

	for index := 1; index < len(seen); index++ {
		if seen[index].seq <= seen[index-1].seq {
			t.Fatalf("sequences = %d then %d, want strictly increasing so the client never discards a later frame",
				seen[index-1].seq, seen[index].seq)
		}
	}
	// A commit's sequence is wall-clock anchored, which is what puts it above any
	// session cursor a client may have seeded the cell with.
	if seen[0].seq < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("seq = %d, want a wall-clock anchored number", seen[0].seq)
	}

	unsubscribe()
	if _, err := store.Edit(ctx, sessionID, dshapi.GoalRef{ID: ref.ID, Revision: ref.Revision}, dshapi.EditGoalRequest{}); err == nil {
		t.Fatal("the store accepted an edit with no fields")
	}
}

// A session's goal outlives the process: the store reads the same document the
// command line and the goal tools write, so a second store over the same
// directory serves the goal the first one committed.
func TestConsoleGoalsReadTheFrameworksOwnState(t *testing.T) {
	store, sessionID := goalsFixture(t)
	ctx := context.Background()
	ref := createdGoal(t, store, sessionID)

	// The framework's own store is the authority on where a goal lives: the same
	// directory read through goals.FileStore sees the console's write.
	path := store.store.(*goals.FileStore)
	state, err := path.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if state.Goal == nil || state.Goal.ID != ref.ID || state.Goal.Objective != "ship the goal dock" {
		t.Fatalf("state = %+v, want the durable goal", state.Goal)
	}
	if _, err := path.Load(ctx, "run-other"); !errors.Is(err, goals.ErrNotFound) {
		t.Fatalf("load of an unset session = %v, want ErrNotFound", err)
	}

	// A goal a CLI run wrote is served by the console store unchanged, which is
	// the point of sharing the directory.
	cliState, err := goals.Create(goals.State{}, goals.CreateOptions{
		ID: "goal-from-cli", Objective: "written by the command line", MaxGoalRounds: 2, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := path.Save(ctx, "run-cli", cliState); err != nil {
		t.Fatalf("save: %v", err)
	}
	view, ok, err := store.Goal(ctx, "run-cli")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if view.ID != "goal-from-cli" || view.MaxGoalRounds != 2 {
		t.Fatalf("view = %+v, want the CLI's goal", view)
	}
}

// assertGoalCode fails unless err is a *dshapi.GoalError carrying code.
func assertGoalCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	var goalErr *dshapi.GoalError
	if !errors.As(err, &goalErr) {
		t.Fatalf("error = %T %v, want a *dshapi.GoalError", err, err)
	}
	if goalErr.Code != code {
		t.Fatalf("error code = %q, want %q (%s)", goalErr.Code, code, goalErr.Message)
	}
}

// A minted identity is the timestamp alone until something actually holds that
// name. Live evidence caught the first version suffixing every goal after the
// first one, because it tested the suffixed candidate and never the base: a
// session's second goal was named `goal-<nanos>-1` although `goal-<nanos>` was
// free.
func TestConsoleGoalIdentifiersSuffixOnlyOnCollision(t *testing.T) {
	now := time.Unix(0, 1_790_044_042_371_542_000)
	base := "goal-1790044042371542000"

	if got := goalIdentifier(now, goals.State{}); got != base {
		t.Fatalf("identifier = %q, want %q", got, base)
	}
	if got := goalIdentifier(now, goals.State{SeenGoalIDs: []string{"goal-something-else"}}); got != base {
		t.Fatalf("identifier = %q, want %q when nothing holds the name", got, base)
	}
	if got := goalIdentifier(now, goals.State{SeenGoalIDs: []string{base}}); got != base+"-1" {
		t.Fatalf("identifier = %q, want the first free suffix", got)
	}
	if got := goalIdentifier(now, goals.State{SeenGoalIDs: []string{base, base + "-1"}}); got != base+"-2" {
		t.Fatalf("identifier = %q, want the second free suffix", got)
	}
}

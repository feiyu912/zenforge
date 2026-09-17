package goals

import (
	"context"
	"testing"
	"time"
)

func fixedClock() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

func TestGoalLifecycleTransitions(t *testing.T) {
	now := fixedClock()
	state, err := Create(State{}, CreateOptions{ID: "goal-1", Objective: "ship the feature", MaxGoalRounds: 3, Now: now})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if state.Goal.Phase != PhaseActive || state.Goal.Revision != 1 || state.RoundsStarted != 0 {
		t.Fatalf("created state = %#v", state)
	}
	if !state.Armed() {
		t.Fatal("a fresh goal must be armed")
	}

	// A second create is refused while the first goal is not complete.
	if _, err := Create(state, CreateOptions{ID: "goal-2", Objective: "another", Now: now}); !IsCode(err, CodeInvalidTransition) {
		t.Fatalf("second create error = %v", err)
	}

	// Rounds are admitted in order and within the budget.
	state, err = AdmitRound(state, 1, now)
	if err != nil || state.RoundsStarted != 1 {
		t.Fatalf("AdmitRound(1) = %#v err=%v", state, err)
	}
	if _, err := AdmitRound(state, 3, now); !IsCode(err, CodeRevisionMismatch) {
		t.Fatalf("out-of-order round error = %v", err)
	}

	// Edit may change the definition but not the phase.
	edited, err := Edit(state, EditOptions{Objective: "ship it well", MaxGoalRounds: 5, Now: now})
	if err != nil {
		t.Fatalf("Edit returned error: %v", err)
	}
	if edited.Goal.Objective != "ship it well" || edited.Goal.MaxGoalRounds != 5 || edited.Goal.Phase != PhaseActive {
		t.Fatalf("edited state = %#v", edited.Goal)
	}
	if edited.Goal.Revision != state.Goal.Revision+1 {
		t.Fatalf("edit revision = %d", edited.Goal.Revision)
	}

	// Pause and resume round-trip, preserving the round counter.
	paused, err := Pause(edited, now)
	if err != nil || paused.Goal.Phase != PhasePaused || paused.Armed() {
		t.Fatalf("paused = %#v err=%v", paused, err)
	}
	resumed, err := Resume(paused, now)
	if err != nil || resumed.Goal.Phase != PhaseActive || resumed.RoundsStarted != 1 {
		t.Fatalf("resumed = %#v err=%v", resumed, err)
	}

	// Blocking requires the same condition across MinBlockedRounds rounds;
	// the intermediate attempts are rejected but record the observation.
	reason := BlockedReason{Code: "missing-credentials", Message: "the API key is not configured"}
	blocked := resumed
	for attempt := 1; attempt <= MinBlockedRounds; attempt++ {
		blocked, err = AdmitRound(blocked, blocked.RoundsStarted+1, now)
		if err != nil {
			t.Fatalf("AdmitRound(%d) returned error: %v", attempt, err)
		}
		blocked, err = Block(blocked, reason, now)
		if attempt < MinBlockedRounds {
			if !IsCode(err, CodeBlockedRounds) {
				t.Fatalf("block attempt %d error = %v", attempt, err)
			}
			if blocked.Goal.Phase != PhaseActive || blocked.BlockedStreak != attempt {
				t.Fatalf("attempt %d state = %#v", attempt, blocked)
			}
			continue
		}
		if err != nil {
			t.Fatalf("final block attempt error = %v", err)
		}
	}
	if blocked.Goal.Phase != PhaseBlocked || blocked.Goal.BlockedReason.Code != "missing-credentials" {
		t.Fatalf("blocked = %#v", blocked)
	}
	// Repeating the same condition within one round does not advance the
	// streak, so three calls in one round cannot satisfy the rule.
	single := resumed
	single, err = AdmitRound(single, single.RoundsStarted+1, now)
	if err != nil {
		t.Fatalf("AdmitRound returned error: %v", err)
	}
	for attempt := 0; attempt < MinBlockedRounds+1; attempt++ {
		single, err = Block(single, reason, now)
		if !IsCode(err, CodeBlockedRounds) {
			t.Fatalf("same-round block attempt %d error = %v", attempt, err)
		}
	}
	if single.BlockedStreak != 1 {
		t.Fatalf("same-round streak = %d", single.BlockedStreak)
	}
	if _, err := Block(blocked, BlockedReason{Code: "other", Message: "another condition"}, now); !IsCode(err, CodeInvalidTransition) {
		t.Fatalf("double block error = %v", err)
	}
	// Resume clears the reason.
	resumedAgain, err := Resume(blocked, now)
	if err != nil || resumedAgain.Goal.Phase != PhaseActive || resumedAgain.Goal.BlockedReason != nil {
		t.Fatalf("resume from blocked = %#v err=%v", resumedAgain, err)
	}

	// Complete from any non-complete phase, then a new goal may be created.
	complete, err := Complete(resumedAgain, now)
	if err != nil || complete.Goal.Phase != PhaseComplete {
		t.Fatalf("complete = %#v err=%v", complete, err)
	}
	if _, err := Complete(complete, now); !IsCode(err, CodeInvalidTransition) {
		t.Fatalf("double complete error = %v", err)
	}
	fresh, err := Create(complete, CreateOptions{ID: "goal-2", Objective: "next", Now: now})
	if err != nil || fresh.Goal.ID != "goal-2" {
		t.Fatalf("create after complete = %#v err=%v", fresh, err)
	}
	// The first goal's id may never be reused.
	done, _ := Complete(fresh, now)
	if _, err := Create(done, CreateOptions{ID: "goal-1", Objective: "again", Now: now}); !IsCode(err, CodeDuplicateGoal) {
		t.Fatalf("duplicate id error = %v", err)
	}
}

func TestGoalResumeRejectsAnExhaustedBudget(t *testing.T) {
	now := fixedClock()
	state, err := Create(State{}, CreateOptions{ID: "goal-1", Objective: "objective", MaxGoalRounds: 1, Now: now})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	state, err = AdmitRound(state, 1, now)
	if err != nil {
		t.Fatalf("AdmitRound returned error: %v", err)
	}
	if state.Armed() {
		t.Fatal("an exhausted goal must not be armed")
	}
	if _, err := Resume(state, now); !IsCode(err, CodeRoundBudget) {
		t.Fatalf("resume error = %v", err)
	}
	if _, err := AdmitRound(state, 2, now); !IsCode(err, CodeRoundBudget) {
		t.Fatalf("extra round error = %v", err)
	}
}

func TestGoalBlockedReasonValidation(t *testing.T) {
	now := fixedClock()
	state, _ := Create(State{}, CreateOptions{ID: "goal-1", Objective: "objective", MaxGoalRounds: MinBlockedRounds, Now: now})
	cases := []BlockedReason{
		{Code: "Not-Kebab", Message: "fine"},
		{Code: "ok", Message: ""},
		{Code: "ok", Message: " padded "},
	}
	for _, reason := range cases {
		if _, err := Block(state, reason, now); !IsCode(err, CodeInvalidDefinition) {
			t.Fatalf("reason %#v error = %v", reason, err)
		}
	}
	// A well-formed reason still has to survive the minimum round count.
	current := state
	for round := 1; round <= MinBlockedRounds; round++ {
		var err error
		current, err = AdmitRound(current, round, now)
		if err != nil {
			t.Fatalf("AdmitRound returned error: %v", err)
		}
		current, err = Block(current, BlockedReason{Code: "ok", Message: "fine"}, now)
		if round < MinBlockedRounds && !IsCode(err, CodeBlockedRounds) {
			t.Fatalf("round %d error = %v", round, err)
		}
		if round == MinBlockedRounds && err != nil {
			t.Fatalf("valid reason rejected at round %d: %v", round, err)
		}
	}
}

func TestGoalFileStoreRoundTripsAndConfinesSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewFileStore(dir)
	now := fixedClock()
	state, err := Create(State{}, CreateOptions{ID: "goal-1", Objective: "persist me", MaxGoalRounds: 4, Now: now})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Save(ctx, "session-1", state); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	loaded, err := store.Load(ctx, "session-1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Goal == nil || loaded.Goal.Objective != "persist me" || loaded.Goal.MaxGoalRounds != 4 {
		t.Fatalf("loaded = %#v", loaded)
	}
	// Mutating the loaded copy must not affect the store.
	loaded.Goal.Objective = "mutated"
	again, err := store.Load(ctx, "session-1")
	if err != nil || again.Goal.Objective != "persist me" {
		t.Fatalf("store aliased its value: %#v err=%v", again, err)
	}
	if _, err := store.Load(ctx, "missing"); !IsNotFound(err) {
		t.Fatalf("missing load error = %v", err)
	}
	if _, err := store.Load(ctx, "../escape"); err == nil {
		t.Fatal("path traversal session id was accepted")
	}
	if err := store.Delete(ctx, "session-1"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, err := store.Load(ctx, "session-1"); !IsNotFound(err) {
		t.Fatalf("deleted load error = %v", err)
	}
}

func TestGoalMemoryStoreValidatesState(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := fixedClock()
	state, _ := Create(State{}, CreateOptions{ID: "goal-1", Objective: "objective", Now: now})
	state.RoundsStarted = 99
	if err := store.Save(ctx, "session", state); err == nil {
		t.Fatal("inconsistent state was stored")
	}
	if err := store.Save(ctx, "", state); err == nil {
		t.Fatal("empty session id was accepted")
	}
}

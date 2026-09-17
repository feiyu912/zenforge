package goals

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func report(status string) Report {
	switch status {
	case StatusComplete:
		return Report{Status: StatusComplete, Summary: "done", Evidence: []string{"tests pass"}}
	case StatusBlocked:
		return Report{Status: StatusBlocked, Summary: "stuck", Evidence: []string{"error log"}, Blocker: "the API key is missing"}
	default:
		return Report{Status: StatusContinue, Summary: "progress", Evidence: []string{"half done"}, NextSteps: []string{"finish it"}}
	}
}

func TestRalphRunsFreshRoundsUntilComplete(t *testing.T) {
	var seen []int
	outcome, err := RunRalph(context.Background(), "objective", DriverOptions{MaxRounds: 5}, func(_ context.Context, round int, _ State, previous Report, _ Report) (Report, error) {
		seen = append(seen, round)
		if round == 1 {
			if previous.Status != "" {
				t.Fatalf("first round received a previous report: %#v", previous)
			}
			return report(StatusContinue), nil
		}
		if previous.Status != StatusContinue {
			t.Fatalf("round %d previous = %#v", round, previous)
		}
		return report(StatusComplete), nil
	})
	if err != nil {
		t.Fatalf("RunRalph returned error: %v", err)
	}
	if outcome.Status != StatusComplete || outcome.Rounds != 2 || len(seen) != 2 {
		t.Fatalf("outcome = %#v rounds=%v", outcome, seen)
	}
}

func TestRalphStopsOnBlockerAndRoundLimit(t *testing.T) {
	outcome, err := RunRalph(context.Background(), "objective", DriverOptions{MaxRounds: 4}, func(context.Context, int, State, Report, Report) (Report, error) {
		return report(StatusBlocked), nil
	})
	if err != nil {
		t.Fatalf("RunRalph returned error: %v", err)
	}
	if outcome.Status != StatusBlocked || outcome.Reason != "the API key is missing" || outcome.Rounds != 1 {
		t.Fatalf("blocked outcome = %#v", outcome)
	}

	outcome, err = RunRalph(context.Background(), "objective", DriverOptions{MaxRounds: 3}, func(context.Context, int, State, Report, Report) (Report, error) {
		return report(StatusContinue), nil
	})
	if err != nil {
		t.Fatalf("RunRalph returned error: %v", err)
	}
	if outcome.Status != StatusContinue || outcome.Rounds != 3 || !strings.Contains(outcome.Reason, "round limit 3") {
		t.Fatalf("limit outcome = %#v", outcome)
	}
}

func TestRalphReportValidation(t *testing.T) {
	cases := []struct {
		name   string
		report Report
	}{
		{"empty summary", Report{Status: StatusContinue, NextSteps: []string{"a"}}},
		{"continue without next steps", Report{Status: StatusContinue, Summary: "s"}},
		{"continue with blocker", Report{Status: StatusContinue, Summary: "s", NextSteps: []string{"a"}, Blocker: "b"}},
		{"complete without evidence", Report{Status: StatusComplete, Summary: "s"}},
		{"complete with next steps", Report{Status: StatusComplete, Summary: "s", Evidence: []string{"e"}, NextSteps: []string{"a"}}},
		{"blocked without blocker", Report{Status: StatusBlocked, Summary: "s", Evidence: []string{"e"}}},
		{"bad status", Report{Status: "later", Summary: "s", Evidence: []string{"e"}}},
		{"unnormalized summary", Report{Status: StatusContinue, Summary: " s ", NextSteps: []string{"a"}}},
		{"unnormalized list entry", Report{Status: StatusContinue, Summary: "s", NextSteps: []string{" a "}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.report.Validate(0); err == nil {
				t.Fatalf("report %#v was accepted", testCase.report)
			}
		})
	}
	if err := report(StatusContinue).Validate(0); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	// The handoff cap is enforced on the serialized report.
	if err := report(StatusContinue).Validate(8); err == nil {
		t.Fatal("oversized report was accepted")
	}
	// A round function that fails stops the loop with its error.
	if _, err := RunRalph(context.Background(), "objective", DriverOptions{}, func(context.Context, int, State, Report, Report) (Report, error) {
		return Report{}, fmt.Errorf("round exploded")
	}); err == nil || !strings.Contains(err.Error(), "round exploded") {
		t.Fatalf("round error = %v", err)
	}
	if _, err := RunRalph(context.Background(), "  ", DriverOptions{}, nil); err == nil {
		t.Fatal("blank objective was accepted")
	}
}

func TestRunGoalDrivesPersistedRounds(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := fixedClock()
	state, err := Create(State{}, CreateOptions{ID: "goal-1", Objective: "objective", MaxGoalRounds: 5, Now: now})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Save(ctx, "session", state); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	// Three blocked reports with the same condition are required before
	// the goal is marked blocked; the first two only continue.
	outcome, err := RunGoal(ctx, store, "session", DriverOptions{}, func(_ context.Context, round int, admitted State, _ Report, _ Report) (Report, error) {
		if admitted.RoundsStarted != round {
			t.Fatalf("round %d admitted state = %#v", round, admitted)
		}
		return report(StatusBlocked), nil
	})
	if err != nil {
		t.Fatalf("RunGoal returned error: %v", err)
	}
	if outcome.Status != StatusBlocked || outcome.Rounds != MinBlockedRounds {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !strings.Contains(outcome.Reason, "API key") {
		t.Fatalf("reason = %q", outcome.Reason)
	}
	stored, err := store.Load(ctx, "session")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if stored.Goal.Phase != PhaseBlocked || stored.Goal.BlockedReason == nil {
		t.Fatalf("stored goal = %#v", stored.Goal)
	}
	if stored.RoundsStarted != MinBlockedRounds {
		t.Fatalf("stored rounds = %d", stored.RoundsStarted)
	}

	// A resume plus a completed round finishes the goal.
	resumed, err := Resume(stored, now)
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	if err := store.Save(ctx, "session", resumed); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	outcome, err = RunGoal(ctx, store, "session", DriverOptions{}, func(context.Context, int, State, Report, Report) (Report, error) {
		return report(StatusComplete), nil
	})
	if err != nil {
		t.Fatalf("RunGoal returned error: %v", err)
	}
	if outcome.Status != StatusComplete || outcome.Goal.Phase != PhaseComplete {
		t.Fatalf("outcome = %#v", outcome)
	}

	// A complete goal is not armed, so the driver does no work.
	calls := 0
	outcome, err = RunGoal(ctx, store, "session", DriverOptions{}, func(context.Context, int, State, Report, Report) (Report, error) {
		calls++
		return report(StatusContinue), nil
	})
	if err != nil || calls != 0 {
		t.Fatalf("complete goal ran %d rounds err=%v", calls, err)
	}
	if outcome.Status != string(PhaseComplete) {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestRunGoalStopsAtTheRoundBudget(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	state, err := Create(State{}, CreateOptions{ID: "goal-1", Objective: "objective", MaxGoalRounds: 2, Now: fixedClock()})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Save(ctx, "session", state); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	outcome, err := RunGoal(ctx, store, "session", DriverOptions{}, func(context.Context, int, State, Report, Report) (Report, error) {
		return report(StatusContinue), nil
	})
	if err != nil {
		t.Fatalf("RunGoal returned error: %v", err)
	}
	if outcome.Status != StatusContinue || outcome.Rounds != 2 || !strings.Contains(outcome.Reason, "budget of 2") {
		t.Fatalf("outcome = %#v", outcome)
	}
}

package dshstream

import (
	"context"
	"testing"

	"github.com/feiyu912/zenforge"
)

// A session whose only run is the first turn resolves to itself, which keeps
// every existing session (and every existing follow test) unchanged.
func TestCurrentRunOfASingleTurnSessionIsTheSession(t *testing.T) {
	f := newFixture(t, Config{})
	runID := f.startRun(t, "hello")
	if got := f.handler.currentRun(context.Background(), runID); got != runID {
		t.Fatalf("currentRun(%q) = %q, want the run itself", runID, got)
	}
}

// A durable turn the manager no longer tracks still counts: a finished
// conversation must still be followable, or history would vanish when a run is
// pruned from the registry.
func TestCurrentRunFollowsADurableOnlyContinuation(t *testing.T) {
	f := newFixture(t, Config{})
	ctx := context.Background()
	first := "run_chain_durable"
	if err := f.store.Append(ctx,
		zenforge.NewEvent(zenforge.EventRunStarted, first, map[string]any{"input": "hello"})); err != nil {
		t.Fatalf("append first turn: %v", err)
	}
	if err := f.store.Append(ctx,
		zenforge.NewEvent(zenforge.EventRunStarted, first+"~2", map[string]any{"input": "again"})); err != nil {
		t.Fatalf("append second turn: %v", err)
	}
	if got := f.handler.currentRun(ctx, first); got != first+"~2" {
		t.Fatalf("currentRun(%q) = %q, want the newest turn", first, got)
	}
}

// An adopted id that merely looks like a continuation keeps its own identity:
// its base does not exist, so there is nothing to resolve to.
func TestCurrentRunLeavesAForeignRunIDAlone(t *testing.T) {
	f := newFixture(t, Config{})
	ctx := context.Background()
	if err := f.store.Append(ctx,
		zenforge.NewEvent(zenforge.EventRunStarted, "run_foreign~2", map[string]any{"input": "x"})); err != nil {
		t.Fatalf("append adopted run: %v", err)
	}
	if got := f.handler.currentRun(ctx, "run_foreign~2"); got != "run_foreign~2" {
		t.Fatalf("currentRun = %q, want the adopted id kept", got)
	}
}

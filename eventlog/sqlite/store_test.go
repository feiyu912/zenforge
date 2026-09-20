package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/eventlog"
)

var _ eventlog.RunLister = (*Store)(nil)

func TestStoreAppendReadAndLatestSeq(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunDone, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_2", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	events, err := store.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("unexpected event count: got %d want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("unexpected seqs: got %d, %d", events[0].Seq, events[1].Seq)
	}

	latest, err := store.LatestSeq(ctx, "run_1")
	if err != nil {
		t.Fatalf("LatestSeq returned error: %v", err)
	}
	if latest != 2 {
		t.Fatalf("unexpected latest seq: got %d want 2", latest)
	}
}

func TestStoreReadSupportsAfterSeqAndLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventModelDelta, "run_1", nil)); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	events, err := store.Read(ctx, "run_1", 1, 1)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unexpected event count: got %d want 1", len(events))
	}
	if events[0].Seq != 2 {
		t.Fatalf("unexpected seq: got %d want 2", events[0].Seq)
	}
}

func TestStoreRejectsOutOfOrderSeq(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil).WithSeq(2)); err == nil {
		t.Fatalf("expected out-of-order seq error")
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := openTestStore(t)
	defer store.Close()

	err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return store
}

func TestRunIDsEnumeratesTheRunsItHolds(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, runID := range []string{"run_b", "run_a"} {
		if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, runID, map[string]any{"input": "hi"})); err != nil {
			t.Fatalf("append %s: %v", runID, err)
		}
	}
	ids, err := store.RunIDs(ctx)
	if err != nil {
		t.Fatalf("RunIDs: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"run_a", "run_b"}) {
		t.Fatalf("RunIDs = %v, want the run ids in order", ids)
	}
}

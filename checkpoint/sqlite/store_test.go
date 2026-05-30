package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
)

func TestStoreSaveLoadDelete(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	cp := testCheckpoint("run_1", 1)

	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	loaded, err := store.Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.RunID != "run_1" || loaded.Seq != 1 {
		t.Fatalf("unexpected checkpoint: %#v", loaded)
	}

	if err := store.Delete(ctx, "run_1"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	_, err = store.Load(ctx, "run_1")
	if !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreLoadMissingReturnsErrNotFound(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	_, err := store.Load(context.Background(), "missing")
	if !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreListSummaries(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	first := testCheckpoint("run_1", 1)
	first.State.Phase = harness.RunPhaseModel
	first.State.Control.Status = harness.RunStatusModelStreaming
	first.State.Step = 1
	first.SavedAt = time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	second := testCheckpoint("run_2", 3)
	second.State.Phase = harness.RunPhaseCompleted
	second.State.Control.Status = harness.RunStatusCompleted
	second.State.Step = 4
	second.SavedAt = time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("Save first returned error: %v", err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("Save second returned error: %v", err)
	}

	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("summary count = %d, want 2: %#v", len(got), got)
	}
	if got[0].RunID != "run_2" || got[0].Seq != 3 || got[0].Phase != "completed" || got[0].Status != "COMPLETED" || got[0].Step != 4 {
		t.Fatalf("unexpected first summary: %#v", got[0])
	}
	if got[1].RunID != "run_1" {
		t.Fatalf("unexpected ordering: %#v", got)
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := openTestStore(t)
	defer store.Close()

	err := store.Save(ctx, testCheckpoint("run_1", 1))
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

func testCheckpoint(runID string, seq int64) checkpoint.Checkpoint {
	now := time.Now().UTC()
	return checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   runID,
		Seq:     seq,
		State: harness.RunState{
			Version:   harness.RunStateVersion,
			RunID:     runID,
			Input:     "hello",
			Phase:     harness.RunPhaseCreated,
			CreatedAt: now,
			UpdatedAt: now,
			Control:   harness.RunControlState{Status: harness.RunStatusIdle},
		},
		SavedAt: now,
	}
}

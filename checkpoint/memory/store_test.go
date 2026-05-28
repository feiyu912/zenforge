package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
)

func TestStoreSaveLoadDelete(t *testing.T) {
	ctx := context.Background()
	store := New()
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
	_, err := New().Load(context.Background(), "missing")
	if !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreClonesStoredCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := New()
	cp := testCheckpoint("run_1", 1)
	cp.State.Meta = map[string]any{"original": "value"}
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	cp.State.Meta["original"] = "changed"
	loaded, err := store.Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.State.Meta["original"] != "value" {
		t.Fatalf("stored checkpoint was mutated: %#v", loaded.State.Meta)
	}
	loaded.State.Meta["original"] = "changed again"

	reloaded, err := store.Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if reloaded.State.Meta["original"] != "value" {
		t.Fatalf("loaded checkpoint was not cloned: %#v", reloaded.State.Meta)
	}
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

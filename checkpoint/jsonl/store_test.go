package jsonl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
)

func TestStoreSaveLoadDelete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
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
	if _, err := os.Stat(filepath.Join(root, "run_1", checkpointsFileName)); err != nil {
		t.Fatalf("expected checkpoints history file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "run_1", latestFileName)); err != nil {
		t.Fatalf("expected latest checkpoint file: %v", err)
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
	_, err := New(t.TempDir()).Load(context.Background(), "missing")
	if !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreLoadUsesLatestWhenHistoryHasCorruptLine(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	cp := testCheckpoint("run_1", 1)
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	history := filepath.Join(root, "run_1", checkpointsFileName)
	file, err := os.OpenFile(history, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile returned error: %v", err)
	}
	if _, err := file.WriteString("{bad json}\n"); err != nil {
		file.Close()
		t.Fatalf("WriteString returned error: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	loaded, err := store.Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Seq != 1 {
		t.Fatalf("unexpected checkpoint: %#v", loaded)
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New(t.TempDir()).Save(ctx, testCheckpoint("run_1", 1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
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

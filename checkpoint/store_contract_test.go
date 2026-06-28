package checkpoint_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
	"github.com/feiyu912/zenforge/harness"
)

func TestStoreSequenceContract(t *testing.T) {
	tests := []struct {
		name string
		open func(*testing.T) checkpoint.Store
	}{
		{name: "memory", open: func(*testing.T) checkpoint.Store { return checkpointmemory.New() }},
		{name: "jsonl", open: func(t *testing.T) checkpoint.Store { return checkpointjsonl.New(t.TempDir()) }},
		{name: "sqlite", open: func(t *testing.T) checkpoint.Store {
			store, err := checkpointsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "checkpoints.db"))
			if err != nil {
				t.Fatalf("Open sqlite: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := test.open(t)
			if err := store.Save(ctx, contractCheckpoint("run_1", 2)); err != nil {
				t.Fatalf("Save seq 2: %v", err)
			}
			for _, seq := range []int64{2, 1} {
				err := store.Save(ctx, contractCheckpoint("run_1", seq))
				if !errors.Is(err, checkpoint.ErrStaleCheckpoint) {
					t.Fatalf("Save seq %d error = %v, want ErrStaleCheckpoint", seq, err)
				}
				loaded, loadErr := store.Load(ctx, "run_1")
				if loadErr != nil {
					t.Fatalf("Load after rejected seq %d: %v", seq, loadErr)
				}
				if loaded.Seq != 2 {
					t.Fatalf("latest seq after rejected seq %d = %d, want 2", seq, loaded.Seq)
				}
			}
			if err := store.Save(ctx, contractCheckpoint("run_1", 3)); err != nil {
				t.Fatalf("Save seq 3: %v", err)
			}
			loaded, err := store.Load(ctx, "run_1")
			if err != nil {
				t.Fatalf("Load seq 3: %v", err)
			}
			if loaded.Seq != 3 {
				t.Fatalf("latest seq = %d, want 3", loaded.Seq)
			}
		})
	}
}

func TestStoreLoadRejectsInvalidRunStateContract(t *testing.T) {
	stores := []struct {
		name string
		open func(*testing.T) checkpoint.Store
	}{
		{name: "memory", open: func(*testing.T) checkpoint.Store { return checkpointmemory.New() }},
		{name: "jsonl", open: func(t *testing.T) checkpoint.Store { return checkpointjsonl.New(t.TempDir()) }},
		{name: "sqlite", open: func(t *testing.T) checkpoint.Store {
			store, err := checkpointsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "checkpoints.db"))
			if err != nil {
				t.Fatalf("Open sqlite: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		}},
	}
	invalidStates := []struct {
		name   string
		mutate func(*harness.RunState)
		want   string
	}{
		{name: "version", mutate: func(state *harness.RunState) { state.Version = "zenforge.run_state.v2" }, want: "unsupported run state version"},
		{name: "phase", mutate: func(state *harness.RunState) { state.Phase = "future" }, want: "unsupported run phase"},
		{name: "mode", mutate: func(state *harness.RunState) { state.Mode = "future" }, want: "unsupported run mode"},
	}

	for _, storeTest := range stores {
		for _, stateTest := range invalidStates {
			t.Run(storeTest.name+"/"+stateTest.name, func(t *testing.T) {
				store := storeTest.open(t)
				cp := contractCheckpoint("run_invalid", 1)
				stateTest.mutate(&cp.State)
				if err := store.Save(context.Background(), cp); err != nil {
					t.Fatalf("Save fixture: %v", err)
				}
				loaded, err := store.Load(context.Background(), cp.RunID)
				if err == nil || !strings.Contains(err.Error(), stateTest.want) {
					t.Fatalf("Load() = %#v, %v, want error containing %q", loaded, err, stateTest.want)
				}
				if loaded != nil {
					t.Fatalf("Load returned invalid checkpoint: %#v", loaded)
				}
			})
		}
	}
}

func contractCheckpoint(runID string, seq int64) checkpoint.Checkpoint {
	now := time.Now().UTC()
	return checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   runID,
		Seq:     seq,
		State: harness.RunState{
			Version:   harness.RunStateVersion,
			RunID:     runID,
			Input:     "contract test",
			Phase:     harness.RunPhaseCreated,
			CreatedAt: now,
			UpdatedAt: now,
			Control:   harness.RunControlState{Status: harness.RunStatusIdle},
		},
		SavedAt: now,
	}
}

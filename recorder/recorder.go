package recorder

import (
	"context"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/harness"
)

type Recorder struct {
	Events      eventlog.Store
	Checkpoints checkpoint.Store
}

func (r Recorder) SaveCheckpoint(ctx context.Context, state harness.RunState, seq int64) (checkpoint.Checkpoint, error) {
	if r.Checkpoints == nil {
		return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint store is not configured")
	}
	cp := checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   state.RunID,
		Seq:     seq,
		State:   state,
		SavedAt: time.Now().UTC(),
	}
	if err := r.Checkpoints.Save(ctx, cp); err != nil {
		return checkpoint.Checkpoint{}, err
	}
	return cp, nil
}

func (r Recorder) RecordCheckpoint(ctx context.Context, state harness.RunState, seq int64) (checkpoint.Checkpoint, error) {
	if r.Events == nil {
		return checkpoint.Checkpoint{}, fmt.Errorf("event log is not configured")
	}
	cp, err := r.SaveCheckpoint(ctx, state, seq)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	event := zenforge.NewEvent(zenforge.EventCheckpointCreated, cp.RunID, map[string]any{
		"seq":     cp.Seq,
		"version": cp.Version,
		"phase":   string(cp.State.Phase),
	})
	if err := r.Events.Append(ctx, event); err != nil {
		return checkpoint.Checkpoint{}, err
	}
	return cp, nil
}

func (r Recorder) RecordEvent(ctx context.Context, event zenforge.Event) error {
	if r.Events == nil {
		return fmt.Errorf("event log is not configured")
	}
	return r.Events.Append(ctx, event)
}

func (r Recorder) Complete(ctx context.Context, state harness.RunState, seq int64, event zenforge.Event) (checkpoint.Checkpoint, error) {
	cp, err := r.RecordCheckpoint(ctx, state, seq)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	if err := r.RecordEvent(ctx, event); err != nil {
		return checkpoint.Checkpoint{}, err
	}
	return cp, nil
}

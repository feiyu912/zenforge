package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge/harness"
)

const CheckpointVersion = "zenforge.checkpoint.v1"

var (
	ErrNotFound        = errors.New("checkpoint not found")
	ErrStaleCheckpoint = errors.New("checkpoint sequence must increase")
)

type Store interface {
	Save(ctx context.Context, checkpoint Checkpoint) error
	Load(ctx context.Context, runID string) (*Checkpoint, error)
	Delete(ctx context.Context, runID string) error
}

type Checkpoint struct {
	Version string           `json:"version"`
	RunID   string           `json:"runId"`
	Seq     int64            `json:"seq"`
	State   harness.RunState `json:"state"`
	SavedAt time.Time        `json:"savedAt"`
}

func Validate(checkpoint Checkpoint) error {
	if checkpoint.Version == "" {
		return fmt.Errorf("checkpoint version is required")
	}
	if checkpoint.Version != CheckpointVersion {
		return fmt.Errorf("unsupported checkpoint version %q", checkpoint.Version)
	}
	if checkpoint.RunID == "" {
		return fmt.Errorf("checkpoint runId is required")
	}
	if checkpoint.Seq <= 0 {
		return fmt.Errorf("checkpoint seq is required")
	}
	if checkpoint.State.RunID == "" {
		return fmt.Errorf("checkpoint state runId is required")
	}
	if checkpoint.State.RunID != checkpoint.RunID {
		return fmt.Errorf("checkpoint runId %q does not match state runId %q", checkpoint.RunID, checkpoint.State.RunID)
	}
	if checkpoint.SavedAt.IsZero() {
		return fmt.Errorf("checkpoint savedAt is required")
	}
	return nil
}

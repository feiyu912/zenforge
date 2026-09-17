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

// HistoricalStore is an optional Store extension for time travel: it
// returns the newest checkpoint whose sequence is at or below seq, which
// is what a fork or a revert needs. Stores that keep only the latest
// checkpoint do not implement it, and LoadAt falls back to Load.
type HistoricalStore interface {
	Store
	LoadAt(ctx context.Context, runID string, seq int64) (*Checkpoint, error)
}

// LoadAt returns the newest checkpoint for runID at or below seq. A seq
// of zero means "the latest checkpoint". Stores without history support
// answer with their single checkpoint, which fails when it is newer than
// the requested sequence: a store that cannot reconstruct the past must
// not silently pretend to.
func LoadAt(ctx context.Context, store Store, runID string, seq int64) (*Checkpoint, error) {
	if historical, ok := store.(HistoricalStore); ok {
		return historical.LoadAt(ctx, runID, seq)
	}
	cp, err := store.Load(ctx, runID)
	if err != nil {
		return nil, err
	}
	if seq > 0 && cp.Seq > seq {
		return nil, fmt.Errorf("%w: store has no checkpoint at or below seq %d for runId %q (latest %d)", ErrNotFound, seq, runID, cp.Seq)
	}
	return cp, nil
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

// ValidateForLoad validates a checkpoint before its state is returned to a caller.
func ValidateForLoad(checkpoint Checkpoint) error {
	if err := Validate(checkpoint); err != nil {
		return err
	}
	if err := harness.ValidateRunState(checkpoint.State); err != nil {
		return fmt.Errorf("unsupported checkpoint version/state: %w", err)
	}
	return nil
}

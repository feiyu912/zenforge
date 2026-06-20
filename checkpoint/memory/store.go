package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/feiyu912/zenforge/checkpoint"
)

type Store struct {
	mu          sync.RWMutex
	checkpoints map[string]checkpoint.Checkpoint
}

func New() *Store {
	return &Store{
		checkpoints: make(map[string]checkpoint.Checkpoint),
	}
}

func (s *Store) Save(ctx context.Context, cp checkpoint.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkpoint.Validate(cp); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if latest, ok := s.checkpoints[cp.RunID]; ok && cp.Seq <= latest.Seq {
		return fmt.Errorf("%w: runId %q latest seq %d, got %d", checkpoint.ErrStaleCheckpoint, cp.RunID, latest.Seq, cp.Seq)
	}
	cloned, err := clone(cp)
	if err != nil {
		return err
	}
	s.checkpoints[cp.RunID] = cloned
	return nil
}

func (s *Store) Load(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}

	s.mu.RLock()
	cp, ok := s.checkpoints[runID]
	s.mu.RUnlock()
	if !ok {
		return nil, checkpoint.ErrNotFound
	}
	cloned, err := clone(cp)
	if err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (s *Store) Delete(ctx context.Context, runID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runID == "" {
		return checkpoint.ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.checkpoints[runID]; !ok {
		return checkpoint.ErrNotFound
	}
	delete(s.checkpoints, runID)
	return nil
}

func clone(cp checkpoint.Checkpoint) (checkpoint.Checkpoint, error) {
	data, err := json.Marshal(cp)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	var cloned checkpoint.Checkpoint
	if err := json.Unmarshal(data, &cloned); err != nil {
		return checkpoint.Checkpoint{}, err
	}
	return cloned, nil
}

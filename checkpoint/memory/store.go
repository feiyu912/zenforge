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
	history     map[string][]checkpoint.Checkpoint
}

func New() *Store {
	return &Store{
		checkpoints: make(map[string]checkpoint.Checkpoint),
		history:     make(map[string][]checkpoint.Checkpoint),
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
	s.history[cp.RunID] = append(s.history[cp.RunID], cloned)
	return nil
}

// LoadAt implements checkpoint.HistoricalStore.
func (s *Store) LoadAt(ctx context.Context, runID string, seq int64) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}
	s.mu.RLock()
	entries := s.history[runID]
	s.mu.RUnlock()
	if len(entries) == 0 {
		return nil, checkpoint.ErrNotFound
	}
	if seq <= 0 {
		seq = entries[len(entries)-1].Seq
	}
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Seq <= seq {
			cloned, err := clone(entries[index])
			if err != nil {
				return nil, err
			}
			if err := checkpoint.ValidateForLoad(cloned); err != nil {
				return nil, err
			}
			return &cloned, nil
		}
	}
	return nil, fmt.Errorf("%w: no checkpoint at or below seq %d for runId %q", checkpoint.ErrNotFound, seq, runID)
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
	if err := checkpoint.ValidateForLoad(cloned); err != nil {
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
	delete(s.history, runID)
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

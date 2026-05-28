package jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/feiyu912/zenforge/checkpoint"
)

const (
	checkpointsFileName = "checkpoints.jsonl"
	latestFileName      = "latest.json"
)

type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Save(ctx context.Context, cp checkpoint.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return fmt.Errorf("checkpoint root is required")
	}
	if err := checkpoint.Validate(cp); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	runDir := filepath.Join(s.root, cp.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	history, err := os.OpenFile(filepath.Join(runDir, checkpointsFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := history.Write(append(encoded, '\n')); err != nil {
		history.Close()
		return err
	}
	if err := history.Sync(); err != nil {
		history.Close()
		return err
	}
	if err := history.Close(); err != nil {
		return err
	}

	tmpPath := filepath.Join(runDir, latestFileName+".tmp")
	latestPath := filepath.Join(runDir, latestFileName)
	if err := os.WriteFile(tmpPath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, latestPath)
}

func (s *Store) Load(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("checkpoint root is required")
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, runID, latestFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, checkpoint.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var cp checkpoint.Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	if err := checkpoint.Validate(cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (s *Store) Delete(ctx context.Context, runID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return fmt.Errorf("checkpoint root is required")
	}
	if runID == "" {
		return checkpoint.ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, runID)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return checkpoint.ErrNotFound
	} else if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

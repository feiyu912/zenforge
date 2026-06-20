package jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
)

const (
	checkpointsFileName = "checkpoints.jsonl"
	latestFileName      = "latest.json"
)

type Store struct {
	root string
	mu   *sync.Mutex
}

// rootLocks coordinates Store instances that target the same checkpoint root.
var rootLocks sync.Map

// Summary is the latest checkpoint metadata for a run.
type Summary struct {
	RunID   string    `json:"runId"`
	Seq     int64     `json:"seq"`
	Phase   string    `json:"phase"`
	Status  string    `json:"status"`
	Step    int       `json:"step"`
	SavedAt time.Time `json:"savedAt"`
}

func New(root string) *Store {
	key := filepath.Clean(root)
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}
	lock, _ := rootLocks.LoadOrStore(key, &sync.Mutex{})
	return &Store{root: root, mu: lock.(*sync.Mutex)}
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

	latest, err := s.loadLocked(ctx, cp.RunID)
	if err != nil && !errors.Is(err, checkpoint.ErrNotFound) {
		return err
	}
	if err == nil && cp.Seq <= latest.Seq {
		return fmt.Errorf("%w: runId %q latest seq %d, got %d", checkpoint.ErrStaleCheckpoint, cp.RunID, latest.Seq, cp.Seq)
	}

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

// List returns latest checkpoint summaries sorted by newest saved time first.
func (s *Store) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("checkpoint root is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cp, err := s.loadLocked(ctx, entry.Name())
		if errors.Is(err, checkpoint.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, Summary{
			RunID:   cp.RunID,
			Seq:     cp.Seq,
			Phase:   string(cp.State.Phase),
			Status:  string(cp.State.Control.Status),
			Step:    cp.State.Step,
			SavedAt: cp.SavedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].SavedAt.Equal(summaries[j].SavedAt) {
			return summaries[i].RunID < summaries[j].RunID
		}
		return summaries[i].SavedAt.After(summaries[j].SavedAt)
	})
	return summaries, nil
}

func (s *Store) loadLocked(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}
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

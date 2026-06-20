package jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/feiyu912/zenforge"
)

const eventsFileName = "events.jsonl"

type Store struct {
	root string
	mu   *sync.Mutex
}

// rootLocks coordinates Store instances that target the same on-disk log.
var rootLocks sync.Map

func New(root string) *Store {
	key := filepath.Clean(root)
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}
	lock, _ := rootLocks.LoadOrStore(key, &sync.Mutex{})
	return &Store{root: root, mu: lock.(*sync.Mutex)}
}

func (s *Store) Append(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return fmt.Errorf("event log root is required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	runID := event.RunID()

	s.mu.Lock()
	defer s.mu.Unlock()

	latest, err := s.latestSeqLocked(ctx, runID)
	if err != nil {
		return err
	}
	next := zenforge.NextEventSeq(latest)
	if event.Seq == 0 {
		event.Seq = next
	}
	if event.Seq != next {
		return fmt.Errorf("event seq must be %d, got %d", next, event.Seq)
	}
	if err := event.ValidatePersisted(); err != nil {
		return err
	}

	runDir := filepath.Join(s.root, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(runDir, eventsFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("event log root is required")
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []zenforge.Event
	err := s.scanLocked(ctx, runID, func(event zenforge.Event) bool {
		if event.Seq <= afterSeq {
			return true
		}
		out = append(out, event)
		return limit <= 0 || len(out) < limit
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) LatestSeq(ctx context.Context, runID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.root == "" {
		return 0, fmt.Errorf("event log root is required")
	}
	if runID == "" {
		return 0, fmt.Errorf("runID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.latestSeqLocked(ctx, runID)
}

func (s *Store) latestSeqLocked(ctx context.Context, runID string) (int64, error) {
	var latest int64
	err := s.scanLocked(ctx, runID, func(event zenforge.Event) bool {
		latest = event.Seq
		return true
	})
	if err != nil {
		return 0, err
	}
	return latest, nil
}

func (s *Store) scanLocked(ctx context.Context, runID string, visit func(zenforge.Event) bool) error {
	path := filepath.Join(s.root, runID, eventsFileName)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var previousSeq int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event zenforge.Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("parse JSONL %s: %w", path, err)
		}
		if err := event.ValidatePersisted(); err != nil {
			return fmt.Errorf("read event %s: %w", path, err)
		}
		if event.RunID() != runID {
			return fmt.Errorf("read event %s: runID mismatch %q", path, event.RunID())
		}
		expectedSeq := zenforge.NextEventSeq(previousSeq)
		if event.Seq != expectedSeq {
			return fmt.Errorf("read event %s: event seq must be %d, got %d", path, expectedSeq, event.Seq)
		}
		previousSeq = event.Seq
		if !visit(event) {
			return nil
		}
	}
	return nil
}

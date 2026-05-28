package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/feiyu912/zenforge"
)

type Store struct {
	mu     sync.RWMutex
	events map[string][]zenforge.Event
}

func New() *Store {
	return &Store{
		events: make(map[string][]zenforge.Event),
	}
}

func (s *Store) Append(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	runID := event.RunID()

	s.mu.Lock()
	defer s.mu.Unlock()

	latest := latestSeq(s.events[runID])
	if event.Seq == 0 {
		event.Seq = zenforge.NextEventSeq(latest)
	}
	if event.Seq != zenforge.NextEventSeq(latest) {
		return fmt.Errorf("event seq must be %d, got %d", zenforge.NextEventSeq(latest), event.Seq)
	}
	if err := event.ValidatePersisted(); err != nil {
		return err
	}

	s.events[runID] = append(s.events[runID], event)
	return nil
}

func (s *Store) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []zenforge.Event
	for _, event := range s.events[runID] {
		if event.Seq <= afterSeq {
			continue
		}
		out = append(out, event)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *Store) LatestSeq(ctx context.Context, runID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if runID == "" {
		return 0, fmt.Errorf("runID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return latestSeq(s.events[runID]), nil
}

func latestSeq(events []zenforge.Event) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

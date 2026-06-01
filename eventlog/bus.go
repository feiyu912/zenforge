package eventlog

import (
	"context"
	"fmt"
	"sync"

	"github.com/feiyu912/zenforge"
)

// Bus fans out live events to subscribers. It is intentionally ephemeral: the
// event log remains the replay source of truth.
type Bus struct {
	mu          sync.Mutex
	subscribers map[string]map[chan zenforge.Event]struct{}
	closedRuns  map[string]struct{}
}

func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[string]map[chan zenforge.Event]struct{}),
		closedRuns:  make(map[string]struct{}),
	}
}

func (b *Bus) Subscribe(ctx context.Context, runID string, buffer int) (<-chan zenforge.Event, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if runID == "" {
		return nil, nil, fmt.Errorf("runID is required")
	}
	if buffer < 0 {
		return nil, nil, fmt.Errorf("buffer must be non-negative")
	}
	ch := make(chan zenforge.Event, buffer)
	b.mu.Lock()
	if _, closed := b.closedRuns[runID]; closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}, nil
	}
	if b.subscribers[runID] == nil {
		b.subscribers[runID] = make(map[chan zenforge.Event]struct{})
	}
	b.subscribers[runID][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	done := make(chan struct{})
	unsubscribe := func() {
		once.Do(func() {
			close(done)
			b.unsubscribe(runID, ch)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			unsubscribe()
		case <-done:
		}
	}()
	return ch, unsubscribe, nil
}

func (b *Bus) Publish(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.ValidatePersisted(); err != nil {
		return err
	}
	runID := event.RunID()

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, closed := b.closedRuns[runID]; closed {
		return nil
	}
	for ch := range b.subscribers[runID] {
		select {
		case ch <- event:
		default:
			delete(b.subscribers[runID], ch)
			close(ch)
		}
	}
	return nil
}

func (b *Bus) CloseRun(runID string) {
	if runID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closedRuns[runID] = struct{}{}
	for ch := range b.subscribers[runID] {
		close(ch)
	}
	delete(b.subscribers, runID)
}

func (b *Bus) unsubscribe(runID string, ch chan zenforge.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subscribers[runID][ch]; !ok {
		return
	}
	delete(b.subscribers[runID], ch)
	close(ch)
	if len(b.subscribers[runID]) == 0 {
		delete(b.subscribers, runID)
	}
}

// FanoutStore appends to a durable store and publishes the persisted event to a
// live bus after the append succeeds.
type FanoutStore struct {
	mu    sync.Mutex
	Store Store
	Bus   *Bus
}

func NewFanoutStore(store Store, bus *Bus) *FanoutStore {
	if bus == nil {
		bus = NewBus()
	}
	return &FanoutStore{Store: store, Bus: bus}
}

func (s *FanoutStore) Append(ctx context.Context, event zenforge.Event) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("event log store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.Seq == 0 {
		latest, err := s.Store.LatestSeq(ctx, event.RunID())
		if err != nil {
			return err
		}
		event = event.WithSeq(zenforge.NextEventSeq(latest))
	}
	if err := s.Store.Append(ctx, event); err != nil {
		return err
	}
	if s.Bus != nil {
		if err := s.Bus.Publish(ctx, event); err != nil {
			return err
		}
		if terminalEvent(event.Type) {
			s.Bus.CloseRun(event.RunID())
		}
	}
	return nil
}

func (s *FanoutStore) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("event log store is required")
	}
	return s.Store.Read(ctx, runID, afterSeq, limit)
}

func (s *FanoutStore) LatestSeq(ctx context.Context, runID string) (int64, error) {
	if s == nil || s.Store == nil {
		return 0, fmt.Errorf("event log store is required")
	}
	return s.Store.LatestSeq(ctx, runID)
}

func terminalEvent(eventType zenforge.EventType) bool {
	switch eventType {
	case zenforge.EventRunDone, zenforge.EventRunError, zenforge.EventRunCancelled:
		return true
	default:
		return false
	}
}

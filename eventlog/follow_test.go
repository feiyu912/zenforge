package eventlog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
)

func TestFollowDoesNotMissAppendBetweenWatermarkAndReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	durable := eventlogmemory.New()
	bus := NewBus()
	writer := NewFanoutStore(durable, bus)
	if err := writer.Append(ctx, followEvent(zenforge.EventRunStarted)); err != nil {
		t.Fatal(err)
	}
	store := &watermarkStore{
		Store:   durable,
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	events, errs, err := Follow(ctx, store, bus, "run_follow", 0, FollowOptions{PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	<-store.reached
	if err := writer.Append(ctx, followEvent(zenforge.EventModelDelta)); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	if err := writer.Append(ctx, followEvent(zenforge.EventRunDone)); err != nil {
		t.Fatal(err)
	}

	assertFollowSeqs(t, events, []int64{1, 2, 3})
	if err := <-errs; err != nil {
		t.Fatalf("Follow returned error: %v", err)
	}
}

func TestFollowRecoversFromLiveBufferOverflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	durable := eventlogmemory.New()
	bus := NewBus()
	writer := NewFanoutStore(durable, bus)
	events, errs, err := Follow(ctx, durable, bus, "run_follow", 0, FollowOptions{
		LiveBuffer:   1,
		ReadBatch:    1,
		PollInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, eventType := range []zenforge.EventType{
		zenforge.EventRunStarted,
		zenforge.EventModelDelta,
		zenforge.EventModelDelta,
		zenforge.EventRunDone,
	} {
		if err := writer.Append(ctx, followEvent(eventType)); err != nil {
			t.Fatal(err)
		}
	}

	assertFollowSeqs(t, events, []int64{1, 2, 3, 4})
	if err := <-errs; err != nil {
		t.Fatalf("Follow returned error: %v", err)
	}
}

func TestFollowPollsDurableStoreAndHonorsAfterSeq(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	store := eventlogmemory.New()
	if err := store.Append(ctx, followEvent(zenforge.EventRunStarted)); err != nil {
		t.Fatal(err)
	}
	events, errs, err := Follow(ctx, store, NewBus(), "run_follow", 1, FollowOptions{
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, followEvent(zenforge.EventModelDelta)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, followEvent(zenforge.EventRunDone)); err != nil {
		t.Fatal(err)
	}

	assertFollowSeqs(t, events, []int64{2, 3})
	if err := <-errs; err != nil {
		t.Fatalf("Follow returned error: %v", err)
	}
}

func TestFollowClosesWhenTerminalIsBeforeAfterSeq(t *testing.T) {
	ctx := context.Background()
	store := eventlogmemory.New()
	if err := store.Append(ctx, followEvent(zenforge.EventRunDone)); err != nil {
		t.Fatal(err)
	}
	events, errs, err := Follow(ctx, store, NewBus(), "run_follow", 10, FollowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertFollowSeqs(t, events, nil)
	if err := <-errs; err != nil {
		t.Fatalf("Follow returned error: %v", err)
	}
}

func TestFollowStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events, errs, err := Follow(ctx, eventlogmemory.New(), NewBus(), "run_follow", 0, FollowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	assertFollowSeqs(t, events, nil)
	if err := <-errs; err != nil {
		t.Fatalf("cancellation should not be reported as a stream error: %v", err)
	}
}

func TestFollowStopsWhenRunBusClosesWithoutTerminalEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := eventlogmemory.New()
	bus := NewBus()
	events, errs, err := Follow(ctx, store, bus, "run_follow", 0, FollowOptions{
		PollInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus.CloseRun("run_follow")
	assertFollowSeqs(t, events, nil)
	if err := <-errs; err != nil {
		t.Fatalf("closed run returned error: %v", err)
	}
}

func TestFollowCancellationDuringResubscribeDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	live := make(chan zenforge.Event)
	close(live)
	out := make(chan zenforge.Event)

	err := follow(
		ctx,
		eventlogmemory.New(),
		NewBus(),
		"run_follow",
		0,
		FollowOptions{LiveBuffer: 1, ReadBatch: 1, PollInterval: time.Hour},
		live,
		cancel,
		out,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error = %v, want context.Canceled", err)
	}
}

type watermarkStore struct {
	Store
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func (s *watermarkStore) LatestSeq(ctx context.Context, runID string) (int64, error) {
	latest, err := s.Store.LatestSeq(ctx, runID)
	s.once.Do(func() {
		close(s.reached)
		select {
		case <-ctx.Done():
		case <-s.release:
		}
	})
	return latest, err
}

func followEvent(eventType zenforge.EventType) zenforge.Event {
	return zenforge.NewEvent(eventType, "run_follow", nil)
}

func assertFollowSeqs(t *testing.T, events <-chan zenforge.Event, want []int64) {
	t.Helper()
	var got []int64
	for event := range events {
		got = append(got, event.Seq)
	}
	if len(got) != len(want) {
		t.Fatalf("event seqs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event seqs = %v, want %v", got, want)
		}
	}
}

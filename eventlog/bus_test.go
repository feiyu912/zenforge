package eventlog

import (
	"context"
	"testing"

	"github.com/feiyu912/zenforge"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
)

func TestBusPublishesToRunSubscribers(t *testing.T) {
	ctx := context.Background()
	bus := NewBus()
	events, unsubscribe, err := bus.Subscribe(ctx, "run_1", 1)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	defer unsubscribe()

	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_1", map[string]any{"textDelta": "hello"}).WithSeq(1)
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	got := <-events
	if got.RunID() != "run_1" || got.Seq != 1 || got.Type != zenforge.EventModelDelta {
		t.Fatalf("unexpected event: %#v", got)
	}
}

func TestBusDoesNotPublishAcrossRuns(t *testing.T) {
	ctx := context.Background()
	bus := NewBus()
	events, unsubscribe, err := bus.Subscribe(ctx, "run_1", 1)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	defer unsubscribe()

	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_2", nil).WithSeq(1)
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	select {
	case got := <-events:
		t.Fatalf("unexpected cross-run event: %#v", got)
	default:
	}
}

func TestBusUnsubscribesSlowSubscribers(t *testing.T) {
	ctx := context.Background()
	bus := NewBus()
	events, unsubscribe, err := bus.Subscribe(ctx, "run_1", 0)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	defer unsubscribe()

	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_1", nil).WithSeq(1)
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	if _, ok := <-events; ok {
		t.Fatalf("slow subscriber channel was not closed")
	}
}

func TestFanoutStoreAppendsThenPublishesAssignedSeq(t *testing.T) {
	ctx := context.Background()
	bus := NewBus()
	store := NewFanoutStore(eventlogmemory.New(), bus)
	events, unsubscribe, err := bus.Subscribe(ctx, "run_1", 2)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	defer unsubscribe()

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	live := <-events
	if live.Seq != 1 || live.Type != zenforge.EventRunStarted {
		t.Fatalf("unexpected live event: %#v", live)
	}
	replayed, err := store.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(replayed) != 1 || replayed[0].Seq != live.Seq {
		t.Fatalf("replay mismatch: live=%#v replay=%#v", live, replayed)
	}
}

func TestFanoutStoreClosesRunOnTerminalEvent(t *testing.T) {
	ctx := context.Background()
	bus := NewBus()
	store := NewFanoutStore(eventlogmemory.New(), bus)
	events, unsubscribe, err := bus.Subscribe(ctx, "run_1", 1)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	defer unsubscribe()

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunDone, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	got := <-events
	if got.Type != zenforge.EventRunDone {
		t.Fatalf("unexpected terminal event: %#v", got)
	}
	if _, ok := <-events; ok {
		t.Fatalf("subscriber remained open after terminal event")
	}

	late, lateUnsubscribe, err := bus.Subscribe(ctx, "run_1", 1)
	if err != nil {
		t.Fatalf("late Subscribe returned error: %v", err)
	}
	defer lateUnsubscribe()
	if _, ok := <-late; ok {
		t.Fatalf("late subscriber should be closed for a terminal run")
	}
}

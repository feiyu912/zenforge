package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestStoreAppendAssignsSeqAndReadFiltersByRun(t *testing.T) {
	ctx := context.Background()
	store := New()

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunDone, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_2", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	events, err := store.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("unexpected event count: got %d want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("unexpected seqs: got %d, %d", events[0].Seq, events[1].Seq)
	}

	latest, err := store.LatestSeq(ctx, "run_1")
	if err != nil {
		t.Fatalf("LatestSeq returned error: %v", err)
	}
	if latest != 2 {
		t.Fatalf("unexpected latest seq: got %d want 2", latest)
	}
}

func TestStoreReadSupportsAfterSeqAndLimit(t *testing.T) {
	ctx := context.Background()
	store := New()

	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventModelDelta, "run_1", nil)); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	events, err := store.Read(ctx, "run_1", 1, 1)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unexpected event count: got %d want 1", len(events))
	}
	if events[0].Seq != 2 {
		t.Fatalf("unexpected seq: got %d want 2", events[0].Seq)
	}
}

func TestStoreRejectsOutOfOrderSeq(t *testing.T) {
	ctx := context.Background()
	store := New()

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil).WithSeq(2)); err == nil {
		t.Fatalf("expected out-of-order seq error")
	}
}

func TestStoreClonesAppendedAndReadEvents(t *testing.T) {
	ctx := context.Background()
	store := New()
	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_1", map[string]any{
		"nested": map[string]any{"text": "original"},
	})
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	event.Payload["nested"].(map[string]any)["text"] = "changed after append"
	first, err := store.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if got := first[0].Payload["nested"].(map[string]any)["text"]; got != "original" {
		t.Fatalf("stored event was mutated: got %v", got)
	}

	first[0].Payload["nested"].(map[string]any)["text"] = "changed after read"
	second, err := store.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("second Read returned error: %v", err)
	}
	if got := second[0].Payload["nested"].(map[string]any)["text"]; got != "original" {
		t.Fatalf("read event was not cloned: got %v", got)
	}
}

func TestStoreRejectsUnserializablePayload(t *testing.T) {
	store := New()
	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_1", map[string]any{"bad": make(chan int)})
	if err := store.Append(context.Background(), event); err == nil {
		t.Fatalf("expected serialization error")
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := New()
	err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

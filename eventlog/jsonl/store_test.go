package jsonl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestStoreAppendReadAndLatestSeq(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())

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
	store := New(t.TempDir())

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
	store := New(t.TempDir())

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil).WithSeq(2)); err == nil {
		t.Fatalf("expected out-of-order seq error")
	}
}

func TestStoreReturnsErrorForCorruptLine(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	path := filepath.Join(runDir, eventsFileName)
	if err := os.WriteFile(path, []byte("{bad json}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := New(root).Read(ctx, "run_1", 0, 0)
	if err == nil {
		t.Fatalf("expected corrupt line error")
	}
	if !strings.Contains(err.Error(), "parse JSONL") {
		t.Fatalf("expected parse JSONL error, got %v", err)
	}
}

func TestStoreReadsPrettyPrintedEventsLikePlatform(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	path := filepath.Join(runDir, eventsFileName)
	body := `{
  "seq": 1,
  "type": "run.started",
  "runId": "run_1",
  "timestamp": 1
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	events, err := New(root).Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 1 || events[0].Type != zenforge.EventRunStarted {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := New(t.TempDir())
	err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

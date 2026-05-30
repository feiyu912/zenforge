package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestAugmentTaskAddsMemoryBlockAndMetadata(t *testing.T) {
	augmenter := Augmenter{
		Store: NewStaticStore(
			Entry{ID: "low", Text: "lower score", Score: 0.1},
			Entry{ID: "high", Text: "higher score", Score: 0.9},
		),
		MaxEntries: 1,
	}
	task := zenforge.Task{
		RunID: "run_1",
		Input: "What should I do next?",
		Meta:  map[string]any{"sessionId": "s1"},
	}

	got, entries, err := augmenter.AugmentTask(context.Background(), task)
	if err != nil {
		t.Fatalf("AugmentTask returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "high" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if !strings.Contains(got.Input, "Relevant memory:\n- [high] higher score\n\nUser request:\nWhat should I do next?") {
		t.Fatalf("unexpected input: %q", got.Input)
	}
	if got.Meta["sessionId"] != "s1" || got.Meta["memory"] == nil {
		t.Fatalf("metadata not preserved: %#v", got.Meta)
	}
	if task.Input == got.Input {
		t.Fatalf("expected cloned augmented task")
	}
}

func TestAugmentTaskWithoutStoreClonesTask(t *testing.T) {
	task := zenforge.Task{Input: "hello", Meta: map[string]any{"k": "v"}}
	got, entries, err := (Augmenter{}).AugmentTask(context.Background(), task)
	if err != nil {
		t.Fatalf("AugmentTask returned error: %v", err)
	}
	if len(entries) != 0 || got.Input != task.Input {
		t.Fatalf("unexpected result: %#v entries=%#v", got, entries)
	}
	got.Meta["k"] = "changed"
	if task.Meta["k"] != "v" {
		t.Fatalf("task meta was mutated")
	}
}

func TestAugmentTaskHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := (Augmenter{Store: NewStaticStore(Entry{Text: "x"})}).AugmentTask(ctx, zenforge.Task{Input: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

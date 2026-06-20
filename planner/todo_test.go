package planner

import (
	"context"
	"testing"
	"time"
)

func TestNormalizeTodosGeneratesIDsAndRejectsDuplicates(t *testing.T) {
	now := time.Date(2026, 5, 28, 1, 2, 3, 0, time.UTC)
	todos, err := NormalizeTodos([]Todo{{Content: "Inspect repo"}}, now)
	if err != nil {
		t.Fatalf("NormalizeTodos returned error: %v", err)
	}
	if todos[0].ID != "todo_1" || todos[0].Status != TodoPending || !todos[0].CreatedAt.Equal(now) {
		t.Fatalf("unexpected normalized todo: %#v", todos[0])
	}

	_, err = NormalizeTodos([]Todo{{ID: "x", Content: "one"}, {ID: "x", Content: "two"}}, now)
	if err == nil {
		t.Fatalf("expected duplicate id error")
	}
}

func TestNormalizeTodosGeneratesIDWithoutCollidingWithExplicitID(t *testing.T) {
	now := time.Date(2026, 5, 28, 1, 2, 3, 0, time.UTC)
	todos, err := NormalizeTodos([]Todo{
		{Content: "generated"},
		{ID: "todo_1", Content: "explicit"},
	}, now)
	if err != nil {
		t.Fatalf("NormalizeTodos returned error: %v", err)
	}
	if todos[0].ID != "todo_2" || todos[1].ID != "todo_1" {
		t.Fatalf("generated IDs = %#v", todos)
	}
}

func TestMemoryManagerReplaceUpdateListAndEvents(t *testing.T) {
	var events []Event
	manager := NewMemoryManager(MemoryConfig{
		Now: func() time.Time { return time.Date(2026, 5, 28, 1, 0, 0, 0, time.UTC) },
		Sink: func(ctx context.Context, event Event) {
			events = append(events, event)
		},
	})
	todos, err := manager.Replace(context.Background(), "run_1", []Todo{{Content: "Plan work"}})
	if err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	if len(todos) != 1 || todos[0].ID != "todo_1" {
		t.Fatalf("unexpected todos: %#v", todos)
	}
	status := TodoInProgress
	notes := "started"
	todos, err = manager.Update(context.Background(), "run_1", "todo_1", Patch{Status: &status, Notes: &notes})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if todos[0].Status != TodoInProgress || todos[0].Notes != "started" {
		t.Fatalf("unexpected updated todos: %#v", todos)
	}
	listed, err := manager.List(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if listed[0].Status != TodoInProgress {
		t.Fatalf("unexpected listed todos: %#v", listed)
	}
	if len(events) != 2 || events[0].Type != EventTodoUpdated || events[1].Type != EventTodoUpdated {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestMemoryManagerRejectsInvalidUpdate(t *testing.T) {
	manager := NewMemoryManager(MemoryConfig{})
	if _, err := manager.Replace(context.Background(), "run_1", []Todo{{ID: "a", Content: "A"}}); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	bad := TodoStatus("bogus")
	if _, err := manager.Update(context.Background(), "run_1", "a", Patch{Status: &bad}); err == nil {
		t.Fatalf("expected invalid status error")
	}
	if _, err := manager.Update(context.Background(), "run_1", "missing", Patch{}); err == nil {
		t.Fatalf("expected missing todo error")
	}
}

func TestMemoryManagerMissingUpdateDoesNotKeepLock(t *testing.T) {
	manager := NewMemoryManager(MemoryConfig{})
	if _, err := manager.Replace(context.Background(), "run_1", []Todo{{ID: "a", Content: "A"}}); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	notes := "not used"
	if _, err := manager.Update(context.Background(), "run_1", "missing", Patch{Notes: &notes}); err == nil {
		t.Fatal("expected missing todo error")
	}
	if _, err := manager.List(context.Background(), "run_1"); err != nil {
		t.Fatalf("List after failed update returned error: %v", err)
	}
}

func TestMemoryManagerReturnsIsolatedTodoSnapshots(t *testing.T) {
	manager := NewMemoryManager(MemoryConfig{})
	input := []Todo{{
		ID:      "a",
		Content: "A",
		Meta: map[string]any{
			"nested": map[string]any{"value": "original"},
			"list":   []any{map[string]any{"value": "original"}},
		},
	}}
	written, err := manager.Replace(context.Background(), "run_1", input)
	if err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	input[0].Meta["nested"].(map[string]any)["value"] = "input mutation"
	written[0].Meta["list"].([]any)[0].(map[string]any)["value"] = "result mutation"

	listed, err := manager.List(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if got := listed[0].Meta["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("nested metadata was mutated through input: %v", got)
	}
	if got := listed[0].Meta["list"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("nested metadata was mutated through result: %v", got)
	}
}

func TestMemoryManagerRejectsEmptyPatchAndContent(t *testing.T) {
	manager := NewMemoryManager(MemoryConfig{})
	if _, err := manager.Replace(context.Background(), "run_1", []Todo{{ID: "a", Content: "A"}}); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	if _, err := manager.Update(context.Background(), "run_1", "a", Patch{}); err == nil {
		t.Fatal("expected empty patch error")
	}
	empty := "  "
	if _, err := manager.Update(context.Background(), "run_1", "a", Patch{Content: &empty}); err == nil {
		t.Fatal("expected empty content error")
	}
}

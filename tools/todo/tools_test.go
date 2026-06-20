package todo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/tool"
)

func TestTodoToolsWorkThroughInvoker(t *testing.T) {
	manager := planner.NewMemoryManager(planner.MemoryConfig{})
	todoTools, err := Tools(Config{Manager: manager})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	registry, err := tool.NewRegistry(todoTools...)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	invoker := tool.NewInvoker(registry)

	result, err := invoker.Invoke(context.Background(), tool.Call{
		ID:    "call_1",
		RunID: "run_1",
		Name:  "todo_write",
		Arguments: json.RawMessage(`{"todos":[
			{"content":"Inspect repo"},
			{"id":"test","content":"Run tests","status":"pending"}
		]}`),
	})
	if err != nil {
		t.Fatalf("todo_write returned error: %v", err)
	}
	if result.Output == "" {
		t.Fatalf("expected formatted todo output")
	}

	result, err = invoker.Invoke(context.Background(), tool.Call{
		ID:        "call_2",
		RunID:     "run_1",
		Name:      "todo_update",
		Arguments: json.RawMessage(`{"id":"todo_1","status":"in_progress","notes":"working"}`),
	})
	if err != nil {
		t.Fatalf("todo_update returned error: %v", err)
	}
	todos := result.Structured["todos"].([]planner.Todo)
	if todos[0].Status != planner.TodoInProgress || todos[0].Notes != "working" {
		t.Fatalf("unexpected updated todos: %#v", todos)
	}

	result, err = invoker.Invoke(context.Background(), tool.Call{
		ID:        "call_3",
		RunID:     "run_1",
		Name:      "todo_read",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("todo_read returned error: %v", err)
	}
	todos = result.Structured["todos"].([]planner.Todo)
	if len(todos) != 2 {
		t.Fatalf("unexpected todos: %#v", todos)
	}
}

func TestTodoToolsRejectInvalidStatusAndDuplicates(t *testing.T) {
	manager := planner.NewMemoryManager(planner.MemoryConfig{})
	todoTools, err := Tools(Config{Manager: manager})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	registry := tool.MustRegistry(todoTools...)
	invoker := tool.NewInvoker(registry)

	_, err = invoker.Invoke(context.Background(), tool.Call{
		ID:        "call_1",
		RunID:     "run_1",
		Name:      "todo_write",
		Arguments: json.RawMessage(`{"todos":[{"id":"x","content":"one"},{"id":"x","content":"two"}]}`),
	})
	if err == nil {
		t.Fatalf("expected duplicate id error")
	}

	if _, err := manager.Replace(context.Background(), "run_1", []planner.Todo{{ID: "x", Content: "one"}}); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	_, err = invoker.Invoke(context.Background(), tool.Call{
		ID:        "call_2",
		RunID:     "run_1",
		Name:      "todo_update",
		Arguments: json.RawMessage(`{"id":"x","status":"bogus"}`),
	})
	if err == nil {
		t.Fatalf("expected invalid status error")
	}
}

func TestTodoCompatibilityAliases(t *testing.T) {
	manager := planner.NewMemoryManager(planner.MemoryConfig{})
	aliases, err := CompatibilityAliases(Config{Manager: manager})
	if err != nil {
		t.Fatalf("CompatibilityAliases returned error: %v", err)
	}
	registry := tool.MustRegistry(aliases...)
	if _, ok := registry.Lookup("plan_add_tasks"); !ok {
		t.Fatalf("expected plan_add_tasks alias")
	}
}

func TestTodoToolsRejectArgumentsOutsideSchema(t *testing.T) {
	manager := planner.NewMemoryManager(planner.MemoryConfig{})
	todoTools, err := Tools(Config{Manager: manager})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	invoker := tool.NewInvoker(tool.MustRegistry(todoTools...))
	tests := []tool.Call{
		{ID: "write_unknown", RunID: "run_1", Name: "todo_write", Arguments: json.RawMessage(`{"todos":[{"content":"A","createdAt":"2026-01-01T00:00:00Z"}]}`)},
		{ID: "read_unknown", RunID: "run_1", Name: "todo_read", Arguments: json.RawMessage(`{"unexpected":true}`)},
		{ID: "update_unknown", RunID: "run_1", Name: "todo_update", Arguments: json.RawMessage(`{"id":"a","unexpected":true}`)},
		{ID: "read_null", RunID: "run_1", Name: "todo_read", Arguments: json.RawMessage(`null`)},
	}
	for _, call := range tests {
		if _, err := invoker.Invoke(context.Background(), call); err == nil {
			t.Errorf("%s accepted invalid arguments", call.ID)
		}
	}
}

func TestTodoUpdateCanClearNotesWithoutAuxiliaryFlag(t *testing.T) {
	manager := planner.NewMemoryManager(planner.MemoryConfig{Now: func() time.Time { return time.Unix(1, 0).UTC() }})
	if _, err := manager.Replace(context.Background(), "run_1", []planner.Todo{{ID: "a", Content: "A", Notes: "old"}}); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	update, err := Update(Config{Manager: manager}, "todo_update")
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	result, err := update.Call(context.Background(), json.RawMessage(`{"id":"a","notes":""}`), tool.Context{RunID: "run_1"})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	todos := result.Structured["todos"].([]planner.Todo)
	if todos[0].Notes != "" {
		t.Fatalf("notes = %q, want empty", todos[0].Notes)
	}
}

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRegistryLookupIsCaseInsensitiveAndDefinitionsAreSorted(t *testing.T) {
	registry, err := NewRegistry(fakeTool{name: "zeta"}, fakeTool{name: "alpha"})
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	if _, ok := registry.Lookup("ALPHA"); !ok {
		t.Fatalf("expected case-insensitive lookup")
	}
	definitions := registry.Definitions()
	if definitions[0].Name != "alpha" || definitions[1].Name != "zeta" {
		t.Fatalf("definitions are not sorted: %#v", definitions)
	}
}

func TestRegistryRejectsDuplicateNormalizedName(t *testing.T) {
	_, err := NewRegistry(fakeTool{name: "Search"}, fakeTool{name: "search"})
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("expected ErrDuplicateTool, got %v", err)
	}
}

func TestRegistryRejectsTypedNilTool(t *testing.T) {
	var typedNil *nilTool
	_, err := NewRegistry(typedNil)
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("expected ErrInvalidTool, got %v", err)
	}
}

func TestDefaultInvokerEmitsEventsAndNormalizesErrors(t *testing.T) {
	registry, err := NewRegistry(fakeTool{name: "echo", result: Result{Output: "ok"}})
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	var events []Event
	invoker := NewDefaultInvoker(InvokerConfig{
		Registry: registry,
		Sink: func(ctx context.Context, event Event) {
			events = append(events, event)
		},
	})
	result, err := invoker.Invoke(context.Background(), Call{
		ID:        "call_1",
		RunID:     "run_1",
		Name:      "echo",
		Arguments: json.RawMessage(`{"text":"hi"}`),
	})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(events) != 2 || events[0].Type != EventCall || events[1].Type != EventResult {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestDefaultInvokerMissingToolReturnsModelFacingResult(t *testing.T) {
	registry := MustRegistry()
	invoker := NewDefaultInvoker(InvokerConfig{Registry: registry})
	result, err := invoker.Invoke(context.Background(), Call{Name: "missing", RunID: "run_1", ID: "call_1"})
	if !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("expected ErrToolNotFound, got %v", err)
	}
	if result.Error == "" || result.ExitCode == 0 {
		t.Fatalf("expected model-facing error result, got %#v", result)
	}
}

func TestDefaultInvokerPassesContextDeadlineToTool(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	var received time.Time
	registry := MustRegistry(deadlineTool{received: &received})
	invoker := NewDefaultInvoker(InvokerConfig{Registry: registry})
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if _, err := invoker.Invoke(ctx, Call{Name: "deadline"}); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !received.Equal(deadline) {
		t.Fatalf("tool deadline = %v, want %v", received, deadline)
	}
}

type fakeTool struct {
	name   string
	result Result
	err    error
}

type nilTool struct{}

func (*nilTool) Name() string           { return "nil" }
func (*nilTool) Description() string    { return "nil" }
func (*nilTool) Schema() map[string]any { return nil }
func (*nilTool) Call(context.Context, json.RawMessage, Context) (Result, error) {
	return Result{}, nil
}

type deadlineTool struct {
	received *time.Time
}

func (deadlineTool) Name() string           { return "deadline" }
func (deadlineTool) Description() string    { return "deadline" }
func (deadlineTool) Schema() map[string]any { return nil }
func (t deadlineTool) Call(_ context.Context, _ json.RawMessage, call Context) (Result, error) {
	*t.received = call.Deadline
	return Result{}, nil
}

func (t fakeTool) Name() string {
	return t.name
}

func (t fakeTool) Description() string {
	return "fake"
}

func (t fakeTool) Schema() map[string]any {
	return map[string]any{"type": "object"}
}

func (t fakeTool) Call(ctx context.Context, input json.RawMessage, call Context) (Result, error) {
	return t.result, t.err
}

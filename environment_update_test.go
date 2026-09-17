package zenforge

import (
	"context"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

func TestAgentSkipsEnvironmentUpdateWithoutChange(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurn("call_1", "record"),
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:              fakeModel,
		Tools:              []Tool{&bigOutputTool{output: "ok"}},
		Events:             events,
		Checkpoints:        checkpoints,
		WorkingDir:         t.TempDir(),
		EnvironmentContext: true,
		MaxSteps:           4,
	})

	stream, err := agent.Stream(ctx, Task{Input: "go"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	if updates := eventsByType(collected, EventEnvironmentUpdated); len(updates) != 0 {
		t.Fatalf("environment.updated events = %d, want 0 for a stable environment", len(updates))
	}
	cp, err := checkpoints.Load(ctx, collected[0].RunID())
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	for _, message := range cp.State.Messages {
		if strings.Contains(message.Content, "<environment_update>") {
			t.Fatalf("stable run injected an environment update: %q", message.Content)
		}
	}
}

func TestMaybeInjectEnvironmentUpdateAppendsOnlyOnChange(t *testing.T) {
	ctx := context.Background()
	agent := New(Config{
		Model:              &scriptedModel{},
		WorkingDir:         t.TempDir(),
		EnvironmentContext: true,
	})
	stale := "<environment_context>\n<cwd>/stale</cwd>\n</environment_context>"
	state := harness.RunState{Meta: map[string]any{metaEnvironmentContext: stale}}

	var emitted []EventType
	emit := func(eventType EventType, _ map[string]any) error {
		emitted = append(emitted, eventType)
		return nil
	}
	checkpointCalls := 0
	checkpoint := func(context.Context, harness.RunState) error {
		checkpointCalls++
		return nil
	}

	if err := agent.maybeInjectEnvironmentUpdate(ctx, emit, checkpoint, &state); err != nil {
		t.Fatalf("maybeInjectEnvironmentUpdate returned error: %v", err)
	}
	if len(state.Messages) != 1 || state.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v, want one appended system message", state.Messages)
	}
	if !strings.Contains(state.Messages[0].Content, "<environment_update>") ||
		strings.Contains(state.Messages[0].Content, "/stale") {
		t.Fatalf("update content = %q", state.Messages[0].Content)
	}
	if len(emitted) != 1 || emitted[0] != EventEnvironmentUpdated {
		t.Fatalf("emitted = %v", emitted)
	}
	if checkpointCalls != 1 {
		t.Fatalf("checkpoint calls = %d, want 1", checkpointCalls)
	}
	injected := stringValue(state.Meta[metaEnvironmentUpdate])
	if injected == "" || injected == stale {
		t.Fatalf("baseline metadata = %q", injected)
	}

	// A second boundary with unchanged facts injects nothing.
	if err := agent.maybeInjectEnvironmentUpdate(ctx, emit, checkpoint, &state); err != nil {
		t.Fatalf("second call returned error: %v", err)
	}
	if len(state.Messages) != 1 || len(emitted) != 1 || checkpointCalls != 1 {
		t.Fatalf("stable boundary mutated state: messages=%d emitted=%d checkpoints=%d",
			len(state.Messages), len(emitted), checkpointCalls)
	}

	// Disabled environment context never injects.
	disabled := New(Config{Model: &scriptedModel{}, WorkingDir: t.TempDir()})
	disabledState := harness.RunState{Meta: map[string]any{metaEnvironmentContext: stale}}
	if err := disabled.maybeInjectEnvironmentUpdate(ctx, emit, checkpoint, &disabledState); err != nil {
		t.Fatalf("disabled call returned error: %v", err)
	}
	if len(disabledState.Messages) != 0 || len(emitted) != 1 {
		t.Fatalf("disabled agent injected an update: %+v", disabledState.Messages)
	}
}

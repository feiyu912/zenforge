package zenforge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/hooks"
	"github.com/feiyu912/zenforge/model"
)

// hookEngine builds an engine from a hook set that runs real commands.
func hookEngine(t *testing.T, config hooks.Config) *hooks.Engine {
	t.Helper()
	engine, err := hooks.New(hooks.Options{Hooks: config, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("hooks.New returned error: %v", err)
	}
	return engine
}

func TestSessionStartHookContextEntersTheSystemPrompt(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Hooks: hookEngine(t, hooks.Config{
			hooks.EventSessionStart:     {{Command: `printf '{"additionalContext":"SESSION-RULE: never touch main"}'`}},
			hooks.EventUserPromptSubmit: {{Command: `printf '{"additionalContext":"PROMPT-RULE: cite sources"}'`}},
		}),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	// Both hooks report their decision.
	completed := eventsByType(collected, EventHookCompleted)
	if len(completed) != 2 {
		t.Fatalf("hook.completed events = %d, want 2: %v", len(completed), collected)
	}
	// Their context reaches the model as a system section, and it is
	// attributed to the hooks rather than silently mixed into the request.
	var sections []string
	for _, message := range fakeModel.requests[0].Messages {
		if message.Role == "system" {
			sections = append(sections, message.Content)
		}
	}
	joined := strings.Join(sections, "\n")
	for _, want := range []string{"SESSION-RULE: never touch main", "PROMPT-RULE: cite sources"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the hook context %q is missing from the system prompt: %q", want, joined)
		}
	}
}

func TestSessionStartBlockRefusesTheRunBeforeAnyModelCall(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "must not run"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Hooks: hookEngine(t, hooks.Config{
			hooks.EventSessionStart: {{Command: `echo "the repo is frozen" >&2; exit 2`}},
		}),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	errors := eventsByType(collected, EventRunError)
	if len(errors) != 1 {
		t.Fatalf("run was not refused: %v", collected)
	}
	if message := stringValue(errors[0].Payload["error"]); !strings.Contains(message, "the repo is frozen") {
		t.Fatalf("error = %q", message)
	}
	if len(fakeModel.requests) != 0 {
		t.Fatalf("the model was called despite the refusal: %d requests", len(fakeModel.requests))
	}
}

func TestUserPromptSubmitStopDecisionRefusesTheRun(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "must not run"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Hooks: hookEngine(t, hooks.Config{
			hooks.EventUserPromptSubmit: {{Command: `printf '{"continue":false,"stopReason":"the request is out of scope"}'`}},
		}),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	errors := eventsByType(collected, EventRunError)
	if len(errors) != 1 {
		t.Fatalf("run was not stopped: %v", collected)
	}
	if message := stringValue(errors[0].Payload["error"]); !strings.Contains(message, "out of scope") {
		t.Fatalf("error = %q", message)
	}
	if len(fakeModel.requests) != 0 {
		t.Fatalf("the model was called despite the stop: %d requests", len(fakeModel.requests))
	}
}

func TestStopHookSendsTheAgentBackToWork(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	counter := filepath.Join(dir, "stops")
	// A real script that refuses the first stop only: the second attempt is
	// allowed, so the run finishes instead of looping until the retry bound.
	script := fmt.Sprintf(
		`n=$(cat %q 2>/dev/null || echo 0); if [ "$n" -lt 1 ]; then echo $((n+1)) > %q; echo "run the tests before you finish" >&2; exit 2; fi; exit 0`,
		counter, counter)
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Delta: "first answer"}}},
		{events: []model.Event{{Delta: "second answer"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  dir,
		MaxSteps:    3,
		Hooks:       hookEngine(t, hooks.Config{hooks.EventStop: {{Command: script}}}),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	done := eventsByType(collected, EventRunDone)
	if len(done) != 1 {
		t.Fatalf("run did not finish: %v", collected)
	}
	if output := stringValue(done[0].Payload["output"]); output != "second answer" {
		t.Fatalf("output = %q", output)
	}
	if len(fakeModel.requests) != 2 {
		t.Fatalf("model requests = %d, want 2 (the hook forced a second turn)", len(fakeModel.requests))
	}
	// The refusal is visible in the log, with the hook's reason.
	blocked := eventsByType(collected, EventType("stop.blocked"))
	if len(blocked) != 1 {
		t.Fatalf("stop.blocked events = %d: %v", len(blocked), collected)
	}
	if reason := stringValue(blocked[0].Payload["reason"]); !strings.Contains(reason, "run the tests") {
		t.Fatalf("reason = %q", reason)
	}
	// The reason reached the model as the newest instruction.
	last := fakeModel.requests[len(fakeModel.requests)-1]
	found := false
	for _, message := range last.Messages {
		if message.Role == "user" && strings.Contains(message.Content, "run the tests before you finish") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the hook reason was not sent to the model: %#v", last.Messages)
	}
}

func TestStopHookRetriesAreBoundedAtTheAgentLevel(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Delta: "answer 1"}}},
		{events: []model.Event{{Delta: "answer 2"}}},
		{events: []model.Event{{Delta: "answer 3"}}},
		{events: []model.Event{{Delta: "answer 4"}}},
		{events: []model.Event{{Delta: "answer 5"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    6,
		Hooks:       hookEngine(t, hooks.Config{hooks.EventStop: {{Command: `echo "never enough" >&2; exit 2`}}}),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	// The run still completes: a hook cannot hold the agent hostage.
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not finish: %v", collected)
	}
	if got := len(eventsByType(collected, EventType("stop.blocked"))); got != 3 {
		t.Fatalf("stop.blocked events = %d, want 3", got)
	}
	if len(fakeModel.requests) != 4 {
		t.Fatalf("model requests = %d, want 4 (initial plus three refusals)", len(fakeModel.requests))
	}
}

func TestNoHooksMeansNoHookWork(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventHookCompleted)) != 0 {
		t.Fatalf("hook events without hooks: %v", collected)
	}
	if _, err := os.Stat(t.TempDir()); err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
}

package zenforge

import (
	"context"
	"errors"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/memory"
	"github.com/feiyu912/zenforge/model"
)

// scriptedMemoryProvider stands in for the memory manager.
type scriptedMemoryProvider struct {
	summary    string
	summaryErr error
	recorded   []memory.RunSummary
	added      int
	recordErr  error
}

func (p *scriptedMemoryProvider) Summary(context.Context) (string, error) {
	return p.summary, p.summaryErr
}

func (p *scriptedMemoryProvider) Record(_ context.Context, summary memory.RunSummary) (int, error) {
	p.recorded = append(p.recorded, summary)
	return p.added, p.recordErr
}

func TestMemorySummaryIsInjectedIntoTheSystemPrompt(t *testing.T) {
	ctx := context.Background()
	provider := &scriptedMemoryProvider{summary: "# Agent memories\n- the repo runs make check (run run_1)"}
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Memory:      provider,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	injected := eventsByType(collected, EventMemoryInjected)
	if len(injected) != 1 {
		t.Fatalf("memory.injected events = %d: %v", len(injected), collected)
	}
	var system strings.Builder
	for _, message := range fakeModel.requests[0].Messages {
		if message.Role == "system" {
			system.WriteString(message.Content + "\n")
		}
	}
	if !strings.Contains(system.String(), "the repo runs make check") {
		t.Fatalf("the memory summary is missing from the system prompt: %q", system.String())
	}
}

func TestEmptyMemorySummaryInjectsNothing(t *testing.T) {
	ctx := context.Background()
	provider := &scriptedMemoryProvider{}
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Memory:      provider,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventMemoryInjected)) != 0 {
		t.Fatalf("an empty summary was injected: %v", collected)
	}
	// The run still recorded what it did, so the next one can learn from it.
	if len(provider.recorded) != 1 || provider.recorded[0].Task != "hello" {
		t.Fatalf("recorded = %#v", provider.recorded)
	}
}

func TestMemorySummaryFailureFailsTheRun(t *testing.T) {
	ctx := context.Background()
	provider := &scriptedMemoryProvider{summaryErr: errors.New("memory store is unreadable")}
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "must not run"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Memory:      provider,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	errorsSeen := eventsByType(collected, EventRunError)
	if len(errorsSeen) != 1 || !strings.Contains(stringValue(errorsSeen[0].Payload["error"]), "unreadable") {
		t.Fatalf("the run did not fail: %v", collected)
	}
	// Failing before the model is called is the point: a run whose memories
	// cannot be read must not half-run on different instructions.
	if len(fakeModel.requests) != 0 {
		t.Fatalf("the model was called despite the memory failure: %d", len(fakeModel.requests))
	}
}

func TestRunEndRecordsWhatHappened(t *testing.T) {
	ctx := context.Background()
	provider := &scriptedMemoryProvider{added: 2}
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "the build is fixed"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Memory:      provider,
	})
	stream, err := agent.Stream(ctx, Task{Input: "fix the build"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	recorded := eventsByType(collected, EventMemoryRecorded)
	if len(recorded) != 1 || recorded[0].Payload["entries"] != 2 {
		t.Fatalf("memory.recorded = %#v", recorded)
	}
	if len(provider.recorded) != 1 {
		t.Fatalf("recorded = %#v", provider.recorded)
	}
	summary := provider.recorded[0]
	if summary.Task != "fix the build" || summary.Output != "the build is fixed" || summary.RunID == "" {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Project == "" {
		t.Fatalf("the summary lost the project: %#v", summary)
	}
}

func TestMemoryRecordingFailureDoesNotFailTheRun(t *testing.T) {
	ctx := context.Background()
	provider := &scriptedMemoryProvider{recordErr: errors.New("distiller model is down")}
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "the answer"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Memory:      provider,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	done := eventsByType(collected, EventRunDone)
	if len(done) != 1 || stringValue(done[0].Payload["output"]) != "the answer" {
		t.Fatalf("the run did not finish: %v", collected)
	}
	// Memory is an optimization: its failure is reported, not fatal.
	recorded := eventsByType(collected, EventMemoryRecorded)
	if len(recorded) != 1 || !strings.Contains(stringValue(recorded[0].Payload["error"]), "distiller model is down") {
		t.Fatalf("memory.recorded = %#v", recorded)
	}
}

func TestNoMemoryProviderMeansNoMemoryWork(t *testing.T) {
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
	if len(eventsByType(collected, EventMemoryInjected))+len(eventsByType(collected, EventMemoryRecorded)) != 0 {
		t.Fatalf("memory events without a provider: %v", collected)
	}
}

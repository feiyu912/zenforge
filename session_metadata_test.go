package zenforge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools/present"
	"github.com/feiyu912/zenforge/workspace/local"
)

func toolCallTurnArgs(id, name, args string) scriptedTurn {
	return scriptedTurn{events: []model.Event{{
		ToolCalls: []model.ToolCallSpec{{ID: id, Name: name, Arguments: json.RawMessage(args)}},
	}}}
}

func TestAgentDerivesFallbackSessionTitle(t *testing.T) {
	ctx := context.Background()
	events := &testEventStore{}
	agent := New(Config{
		Model:    &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}},
		Events:   events,
		MaxSteps: 2,
	})

	stream, err := agent.Stream(ctx, Task{Input: "Fix the failing build in the parser and report back soon"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	titles := eventsByType(collected, EventSessionTitle)
	if len(titles) != 1 {
		t.Fatalf("session.title events = %d, want 1", len(titles))
	}
	want := "Fix the failing build in the parser and"
	if titles[0].Payload["title"] != want {
		t.Fatalf("title = %q, want %q", titles[0].Payload["title"], want)
	}
	if titles[0].Payload["source"] != "fallback" {
		t.Fatalf("source = %v, want fallback", titles[0].Payload["source"])
	}
	// Titles are log-only: they must never reach the model surface.
	for _, message := range collectedMessages(t, agent, collected) {
		if strings.Contains(message, want) {
			t.Fatalf("session title leaked into the model surface: %q", message)
		}
	}
}

func TestAgentSessionTitlePrefersExplicitOverride(t *testing.T) {
	ctx := context.Background()
	events := &testEventStore{}
	agent := New(Config{
		Model:        &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}},
		Events:       events,
		SessionTitle: "  \x1b[31mRelease\x1b[0m  audit\u202e  ",
		MaxSteps:     2,
	})
	stream, err := agent.Stream(ctx, Task{Input: "ignored input words"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	titles := eventsByType(collected, EventSessionTitle)
	if len(titles) != 1 {
		t.Fatalf("session.title events = %d, want 1", len(titles))
	}
	if titles[0].Payload["title"] != "Release audit" {
		t.Fatalf("title = %q, want sanitized explicit title", titles[0].Payload["title"])
	}
	if titles[0].Payload["source"] != "user" {
		t.Fatalf("source = %v, want user", titles[0].Payload["source"])
	}
}

func TestAgentRejectsExplicitTitleThatNormalizesToEmpty(t *testing.T) {
	ctx := context.Background()
	agent := New(Config{
		Model:        &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}},
		SessionTitle: "\x1b]0;window\x07\u202e\u200b",
		MaxSteps:     2,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	failures := eventsByType(collected, EventRunError)
	if len(failures) != 1 {
		t.Fatalf("run.error events = %d, want 1", len(failures))
	}
	message, _ := failures[0].Payload["error"].(string)
	if !strings.Contains(message, "session title is empty after normalization") {
		t.Fatalf("run error = %q", message)
	}
}

func TestAgentEmitsDeliverablesForPresentCall(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	ws, err := local.New(local.Config{Root: root})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	presenter, err := present.New(present.Config{Workspace: ws})
	if err != nil {
		t.Fatalf("present New returned error: %v", err)
	}
	events := &testEventStore{}
	agent := New(Config{
		Model: &scriptedModel{turns: []scriptedTurn{
			toolCallTurnArgs("call_1", present.Name, `{"files":[{"path":"report.md","description":"Final report"}]}`),
			{events: []model.Event{{Delta: "delivered"}}},
		}},
		Tools:    []tool.Tool{presenter},
		Events:   events,
		MaxSteps: 4,
	})

	stream, err := agent.Stream(ctx, Task{Input: "produce the report"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	deliveries := eventsByType(collected, EventDeliverables)
	if len(deliveries) != 1 {
		t.Fatalf("deliverables.presented events = %d, want 1", len(deliveries))
	}
	files, ok := deliveries[0].Payload["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("deliverable files = %#v", deliveries[0].Payload["files"])
	}
	entry := files[0].(map[string]any)
	if entry["path"] != "report.md" || entry["description"] != "Final report" {
		t.Fatalf("deliverable entry = %#v", entry)
	}
	if deliveries[0].Payload["toolCallId"] != "call_1" {
		t.Fatalf("toolCallId = %v", deliveries[0].Payload["toolCallId"])
	}
}

func TestAgentSkipsDeliverablesEventForFailedPresent(t *testing.T) {
	ctx := context.Background()
	ws, err := local.New(local.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	presenter, err := present.New(present.Config{Workspace: ws})
	if err != nil {
		t.Fatalf("present New returned error: %v", err)
	}
	agent := New(Config{
		Model: &scriptedModel{turns: []scriptedTurn{
			toolCallTurnArgs("call_1", present.Name, `{"files":[{"path":"missing.md"}]}`),
			{events: []model.Event{{Delta: "done"}}},
		}},
		Tools:    []tool.Tool{presenter},
		MaxSteps: 4,
	})

	stream, err := agent.Stream(ctx, Task{Input: "present a missing file"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if deliveries := eventsByType(collected, EventDeliverables); len(deliveries) != 0 {
		t.Fatalf("failed present emitted %d deliverable events", len(deliveries))
	}
	if failures := eventsByType(collected, EventToolError); len(failures) == 0 {
		t.Fatalf("failed present produced no tool.error event: %v", collected)
	}
}

// collectedMessages loads the run's final checkpoint messages for
// leak checks. It skips the assertion when no checkpoint was written.
func collectedMessages(t *testing.T, agent *Agent, collected []Event) []string {
	t.Helper()
	if agent.config.Checkpoints == nil {
		return nil
	}
	cp, err := agent.config.Checkpoints.Load(context.Background(), collected[0].RunID())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cp.State.Messages))
	for _, message := range cp.State.Messages {
		out = append(out, message.Content)
	}
	return out
}

func TestAgentPlanExecuteTitleNamesTheOperatorsTask(t *testing.T) {
	ctx := context.Background()
	events := &testEventStore{}
	agent := New(Config{
		Model: &scriptedModel{turns: []scriptedTurn{
			toolCallTurnArgs("plan_call", "todo_write", `{"todos":[{"id":"task_1","content":"Read the runtime"}]}`),
			{events: []model.Event{{Delta: "plan created"}}},
			toolCallTurnArgs("done_call", "todo_update", `{"id":"task_1","status":"done"}`),
			{events: []model.Event{{Delta: "task done"}}},
			{events: []model.Event{{Delta: "summary done"}}},
		}},
		Events: events,
		Mode:   ModePlanExecute,
	})

	stream, err := agent.Stream(ctx, Task{RunID: "run_title_plan", Input: "h"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	titles := eventsByType(collected, EventSessionTitle)
	if len(titles) == 0 {
		t.Fatal("no session.title events")
	}
	// The plan stage's own input carries planner.PlanPrompt; the title must name
	// what the operator typed, not the instruction the preset appended to it.
	for _, event := range titles {
		if event.Payload["title"] != "h" {
			t.Fatalf("title = %q, want the operator's own task", event.Payload["title"])
		}
	}
	if got := titles[0].Payload["source"]; got != "fallback" {
		t.Fatalf("source = %v, want fallback", got)
	}
}

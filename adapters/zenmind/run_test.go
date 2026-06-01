package zenmind

import (
	"context"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/tools"
)

func TestBuildRunMapsCatalogSessionToConfigAndTask(t *testing.T) {
	echo := tools.Must("echo", "Echo.", func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})
	hidden := tools.Must("hidden", "Hidden.", func(ctx context.Context, in struct{}) (string, error) {
		return "hidden", nil
	})

	run, err := BuildRun(context.Background(), CatalogAgent{
		Name:         "reviewer",
		Instructions: "Review carefully.",
		Model:        ModelRef{Provider: "openai", Name: "gpt-test"},
		ToolNames:    []string{"echo"},
		MaxSteps:     7,
		Planning:     "enabled",
		SubAgents:    "enabled",
		Metadata:     map[string]any{"catalog": "agent"},
	}, Session{
		RunID:          "run_1",
		Input:          "What next?",
		UserID:         "user_1",
		ConversationID: "chat_1",
		TeamID:         "team_1",
		Memory: []MemoryEntry{{
			ID:    "m1",
			Text:  "Prefer short answers.",
			Score: 0.9,
		}},
		Metadata: map[string]any{"session": "meta"},
	}, Runtime{Tools: []zenforge.Tool{echo, hidden}})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}

	if run.Config.Instructions != "Review carefully." || run.Config.MaxSteps != 7 {
		t.Fatalf("unexpected config: %#v", run.Config)
	}
	if run.Config.Planning != zenforge.PlanningEnabled || run.Config.SubAgents != zenforge.SubAgentsEnabled {
		t.Fatalf("unexpected modes: planning=%s subagents=%s", run.Config.Planning, run.Config.SubAgents)
	}
	if len(run.Config.Tools) != 1 || run.Config.Tools[0].Name() != "echo" {
		t.Fatalf("unexpected tools: %#v", run.Config.Tools)
	}
	if run.Task.RunID != "run_1" || !strings.Contains(run.Task.Input, "Prefer short answers.") || !strings.Contains(run.Task.Input, "User request:\nWhat next?") {
		t.Fatalf("unexpected task: %#v", run.Task)
	}
	if run.Task.Meta["session"] != "meta" || run.Task.Meta["memory"] == nil {
		t.Fatalf("task meta missing session/memory: %#v", run.Task.Meta)
	}
	zenmindMeta, ok := run.Task.Meta["zenmind"].(map[string]any)
	if !ok {
		t.Fatalf("missing zenmind meta: %#v", run.Task.Meta)
	}
	sessionMeta, ok := zenmindMeta["session"].(map[string]any)
	if !ok || sessionMeta["conversationId"] != "chat_1" {
		t.Fatalf("unexpected zenmind session meta: %#v", zenmindMeta)
	}
}

func TestBuildRunRequiresInput(t *testing.T) {
	_, err := BuildRun(context.Background(), CatalogAgent{}, Session{}, Runtime{})
	if err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("expected input validation error, got %v", err)
	}
}

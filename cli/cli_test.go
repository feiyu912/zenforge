package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
)

func TestMainVersion(t *testing.T) {
	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"version"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != Version {
		t.Fatalf("unexpected version output: %q", stdout.String())
	}
}

func TestRunRequiresInputBeforeAPIKey(t *testing.T) {
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"run"}, IO{Stderr: &stderr})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "run input is required") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestEventsPrintsTimeline(t *testing.T) {
	dir := t.TempDir()
	store := eventlogjsonl.New(dir)
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventToolCall, "run_1", map[string]any{
		"toolName":  "workspace_grep",
		"arguments": map[string]any{"pattern": "TODO"},
	})); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"events", "--checkpoint-dir", dir, "run_1"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "run run_1 started") || !strings.Contains(output, "tool workspace_grep") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRenderApprovalRequested(t *testing.T) {
	var stdout bytes.Buffer
	renderEvent(&stdout, zenforge.NewEvent(zenforge.EventApprovalRequested, "run_1", map[string]any{
		"operation": "shell.command",
		"risk":      "high",
		"request": map[string]any{
			"title":       "Approve shell command",
			"description": "Run tests",
		},
	}))
	output := stdout.String()
	if !strings.Contains(output, "approval required: shell.command (high)") || !strings.Contains(output, "Approve shell command") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestPlanningModeParsing(t *testing.T) {
	tests := map[string]zenforge.PlanningMode{
		"enabled":      zenforge.PlanningEnabled,
		"true":         zenforge.PlanningEnabled,
		"plan_execute": zenforge.PlanningPlanExecute,
		"plan-execute": zenforge.PlanningPlanExecute,
		"disabled":     zenforge.PlanningDisabled,
		"bogus":        zenforge.PlanningDisabled,
	}
	for input, want := range tests {
		if got := planningMode(input); got != want {
			t.Fatalf("planningMode(%q) = %q, want %q", input, got, want)
		}
	}
}

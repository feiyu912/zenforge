package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/checkpoint"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/harness"
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

func TestEventsLoadsCheckpointDirFromConfig(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "runs")
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{Checkpoint: checkpointConfig{Path: runDir}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	store := eventlogjsonl.New(runDir)
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "run_config", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"events", "--config", configPath, "run_config"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run run_config started") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestEventsCanReadSQLiteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := eventlogsqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "run_sqlite", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"events", "--checkpoint-type", "sqlite", "--checkpoint-dir", path, "run_sqlite"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run run_sqlite started") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestRunsPrintsCheckpointSummaries(t *testing.T) {
	dir := t.TempDir()
	store := checkpointjsonl.New(dir)
	cp := testCLICheckpoint("run_cli", 2)
	cp.State.Phase = harness.RunPhaseCompleted
	cp.State.Control.Status = harness.RunStatusCompleted
	cp.State.Step = 3
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--checkpoint-dir", dir}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "RUN ID") || !strings.Contains(output, "run_cli") || !strings.Contains(output, "completed") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunsCanReadSQLiteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := checkpointsqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	cp := testCLICheckpoint("run_sqlite", 2)
	cp.State.Phase = harness.RunPhaseCompleted
	cp.State.Control.Status = harness.RunStatusCompleted
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--checkpoint-type", "sqlite", "--checkpoint-dir", path}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run_sqlite") || !strings.Contains(stdout.String(), "completed") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestRunsJSONOutput(t *testing.T) {
	dir := t.TempDir()
	store := checkpointjsonl.New(dir)
	if err := store.Save(context.Background(), testCLICheckpoint("run_json", 1)); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--checkpoint-dir", dir, "--json"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	var summaries []checkpointjsonl.Summary
	if err := json.Unmarshal(stdout.Bytes(), &summaries); err != nil {
		t.Fatalf("Unmarshal returned error: %v; output=%q", err, stdout.String())
	}
	if len(summaries) != 1 || summaries[0].RunID != "run_json" {
		t.Fatalf("unexpected summaries: %#v", summaries)
	}
}

func TestRunsLoadsCheckpointDirFromConfig(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "runs")
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{Checkpoint: checkpointConfig{Path: runDir}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	store := checkpointjsonl.New(runDir)
	if err := store.Save(context.Background(), testCLICheckpoint("run_config", 1)); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--config", configPath}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run_config") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestOptionsFromConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "zenforge.json")
	enabled := false
	config := configFile{
		Model:     modelConfig{Name: "gpt-test", APIKeyEnv: "TEST_KEY", BaseURL: "https://api.example"},
		Agent:     agentConfig{Instructions: "Be exact.", MaxSteps: 3, Planning: false},
		Workspace: workspaceConfig{Root: "repo"},
		Shell: shellConfig{
			Enabled:        &enabled,
			Allow:          []string{"go test ./..."},
			Timeout:        "5s",
			MaxOutputBytes: 99,
		},
		Approval:   approvalConfig{Mode: "always"},
		Checkpoint: checkpointConfig{Type: "sqlite", Path: "runs.db"},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	opts, err := optionsFromArgs([]string{"--config", configPath})
	if err != nil {
		t.Fatalf("optionsFromArgs returned error: %v", err)
	}
	if opts.model != "gpt-test" || opts.apiKeyEnv != "TEST_KEY" || opts.baseURL != "https://api.example" {
		t.Fatalf("model opts not applied: %#v", opts)
	}
	if opts.instructions != "Be exact." || opts.maxSteps != 3 || opts.planning != "disabled" {
		t.Fatalf("agent opts not applied: %#v", opts)
	}
	if opts.workspace != "repo" || !opts.noShell || opts.shellTimeout.String() != "5s" || opts.shellMaxOutputBytes != 99 {
		t.Fatalf("tool opts not applied: %#v", opts)
	}
	if opts.approve != "always" {
		t.Fatalf("approval opts not applied: %#v", opts)
	}
	if opts.checkpointType != "sqlite" || opts.checkpointDir != "runs.db" {
		t.Fatalf("checkpoint opts not applied: %#v", opts)
	}
}

func testCLICheckpoint(runID string, seq int64) checkpoint.Checkpoint {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	return checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   runID,
		Seq:     seq,
		State: harness.RunState{
			Version:   harness.RunStateVersion,
			RunID:     runID,
			Input:     "hello",
			Phase:     harness.RunPhaseCreated,
			CreatedAt: now,
			UpdatedAt: now,
			Control:   harness.RunControlState{Status: harness.RunStatusIdle},
		},
		SavedAt: now,
	}
}

func TestInitCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "zenforge.json")
	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"init", "--config", configPath}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var config configFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if config.Model.Name == "" || config.Checkpoint.Path == "" {
		t.Fatalf("default config incomplete: %#v", config)
	}
	if config.Approval.Mode != "prompt" {
		t.Fatalf("default approval mode = %q", config.Approval.Mode)
	}
	if !strings.Contains(stdout.String(), "created") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestApprovalBrokerModes(t *testing.T) {
	always, err := approvalBroker(options{approve: "always"}, IO{})
	if err != nil || always == nil {
		t.Fatalf("always broker = %#v err=%v", always, err)
	}
	never, err := approvalBroker(options{approve: "never"}, IO{})
	if err != nil || never == nil {
		t.Fatalf("never broker = %#v err=%v", never, err)
	}
	prompt, err := approvalBroker(options{approve: "prompt"}, IO{Stdin: strings.NewReader("1\n"), Stderr: &bytes.Buffer{}})
	if err != nil || prompt == nil {
		t.Fatalf("prompt broker = %#v err=%v", prompt, err)
	}
	if _, err := approvalBroker(options{approve: "later"}, IO{}); err == nil {
		t.Fatalf("expected unknown approval mode error")
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

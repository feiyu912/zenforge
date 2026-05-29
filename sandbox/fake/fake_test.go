package fake

import (
	"context"
	"testing"

	"github.com/feiyu912/zenforge/sandbox"
)

func TestFakeSandboxRecordsLifecycle(t *testing.T) {
	fake := &Sandbox{Result: sandbox.ExecuteResult{ExitCode: 0, Stdout: "ok"}}
	session, err := fake.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1", SubtaskID: "task_1", EnvironmentID: "go"})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	result, err := fake.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "go test ./...", CWD: "/workspace"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if err := fake.Close(context.Background(), session); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if session.ID != "run-run_1-task_1" || result.Stdout != "ok" {
		t.Fatalf("unexpected session/result: %#v %#v", session, result)
	}
	if len(fake.OpenCalls) != 1 || len(fake.ExecuteCalls) != 1 || len(fake.CloseCalls) != 1 {
		t.Fatalf("calls not recorded: %#v", fake)
	}
}

func TestPromptProvider(t *testing.T) {
	provider := PromptProvider{Prompts: map[string]sandbox.Prompt{
		"go": {EnvironmentID: "go", Content: "Go tools are installed."},
	}}
	prompt, err := provider.Prompt(context.Background(), "go")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if prompt.Content == "" {
		t.Fatalf("expected prompt content")
	}
	if _, err := provider.Prompt(context.Background(), "missing"); err != sandbox.ErrEnvironmentNotFound {
		t.Fatalf("expected environment_not_found, got %v", err)
	}
}

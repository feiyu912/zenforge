package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	coresandbox "github.com/feiyu912/zenforge/sandbox"
	sandboxfake "github.com/feiyu912/zenforge/sandbox/fake"
)

func TestAugmentTaskInjectsSandboxPromptAndMetadata(t *testing.T) {
	provider := sandboxfake.PromptProvider{
		Prompts: map[string]coresandbox.Prompt{
			"go": {
				EnvironmentID: "go",
				Content:       "Default cwd: /workspace\nNetwork: disabled",
				Metadata:      map[string]any{"version": "dev"},
			},
		},
	}
	task := zenforge.Task{
		RunID: "run_1",
		Input: "Run tests.",
		Meta:  map[string]any{"tenantId": "tenant_1"},
	}

	got, prompt, err := (Augmenter{Provider: provider, EnvironmentID: "go"}).AugmentTask(context.Background(), task)
	if err != nil {
		t.Fatalf("AugmentTask returned error: %v", err)
	}
	if prompt.EnvironmentID != "go" {
		t.Fatalf("prompt = %#v", prompt)
	}
	if !strings.Contains(got.Input, "Sandbox environment:\nDefault cwd: /workspace\nNetwork: disabled\n\nUser request:\nRun tests.") {
		t.Fatalf("unexpected input: %q", got.Input)
	}
	if got.Meta["tenantId"] != "tenant_1" || got.Meta[MetadataEnvironmentID] != "go" {
		t.Fatalf("metadata not preserved and enriched: %#v", got.Meta)
	}
	promptMeta, ok := got.Meta[MetadataPrompt].(map[string]any)
	if !ok || promptMeta["environmentId"] != "go" {
		t.Fatalf("sandbox prompt metadata missing: %#v", got.Meta)
	}
	if task.Input == got.Input {
		t.Fatalf("expected cloned augmented task")
	}
}

func TestAugmentTaskUsesEnvironmentIDFromMetadata(t *testing.T) {
	provider := sandboxfake.PromptProvider{
		Prompts: map[string]coresandbox.Prompt{
			"python": {Content: "Python 3.12 is installed."},
		},
	}
	task := zenforge.Task{
		Input: "Inspect package.",
		Meta:  map[string]any{MetadataEnvironmentID: "python"},
	}

	got, prompt, err := (Augmenter{Provider: provider}).AugmentTask(context.Background(), task)
	if err != nil {
		t.Fatalf("AugmentTask returned error: %v", err)
	}
	if prompt.EnvironmentID != "python" || got.Meta[MetadataEnvironmentID] != "python" {
		t.Fatalf("environment id fallback failed: prompt=%#v meta=%#v", prompt, got.Meta)
	}
	if !strings.Contains(got.Input, "Python 3.12 is installed.") {
		t.Fatalf("prompt was not injected: %q", got.Input)
	}
}

func TestAugmentTaskWithoutProviderClonesTask(t *testing.T) {
	task := zenforge.Task{Input: "hello", Meta: map[string]any{"k": "v"}}
	got, prompt, err := (Augmenter{}).AugmentTask(context.Background(), task)
	if err != nil {
		t.Fatalf("AugmentTask returned error: %v", err)
	}
	if prompt.EnvironmentID != "" || got.Input != task.Input {
		t.Fatalf("unexpected result: %#v prompt=%#v", got, prompt)
	}
	got.Meta["k"] = "changed"
	if task.Meta["k"] != "v" {
		t.Fatalf("task meta was mutated")
	}
}

func TestAugmentTaskRequiresEnvironmentID(t *testing.T) {
	_, _, err := (Augmenter{Provider: sandboxfake.PromptProvider{Prompts: map[string]coresandbox.Prompt{}}}).AugmentTask(context.Background(), zenforge.Task{Input: "hello"})
	if err == nil || !strings.Contains(err.Error(), "environment id is required") {
		t.Fatalf("expected environment id error, got %v", err)
	}
}

func TestAugmentTaskPropagatesProviderErrors(t *testing.T) {
	_, _, err := (Augmenter{
		Provider:      sandboxfake.PromptProvider{Err: coresandbox.ErrEnvironmentNotFound},
		EnvironmentID: "missing",
	}).AugmentTask(context.Background(), zenforge.Task{Input: "hello"})
	if !errors.Is(err, coresandbox.ErrEnvironmentNotFound) {
		t.Fatalf("expected ErrEnvironmentNotFound, got %v", err)
	}
}

func TestAugmentTaskHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := (Augmenter{
		Provider:      sandboxfake.PromptProvider{},
		EnvironmentID: "go",
	}).AugmentTask(ctx, zenforge.Task{Input: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

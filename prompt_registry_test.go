package zenforge

import (
	"context"
	"runtime"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/instructions"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/prompt"
)

func TestAgentAssemblesPersonaSectionsInOrder(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:              fakeModel,
		Events:             &testEventStore{},
		Checkpoints:        checkpoints,
		WorkingDir:         t.TempDir(),
		EnvironmentContext: true,
		Instructions:       "FIRST-PARTY-GUIDANCE",
		PersonaPrefix:      "PERSONA-PREFIX in {{workspace}} on {{platform}} with {{team}}",
		PersonaSuffix:      "PERSONA-SUFFIX",
		PromptVariables:    map[string]string{"team": "safety"},
		MaxSteps:           2,
	})

	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	if len(fakeModel.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fakeModel.requests))
	}
	var system []string
	for _, message := range fakeModel.requests[0].Messages {
		if message.Role == "system" {
			system = append(system, message.Content)
		}
	}
	if len(system) != 4 {
		t.Fatalf("system messages = %d (%q), want 4", len(system), system)
	}
	if !strings.HasPrefix(system[0], "PERSONA-PREFIX in ") || !strings.Contains(system[0], "on "+runtime.GOOS+"/"+runtime.GOARCH) || !strings.HasSuffix(system[0], "with safety") {
		t.Fatalf("persona prefix = %q", system[0])
	}
	if system[1] != "FIRST-PARTY-GUIDANCE" {
		t.Fatalf("instructions section = %q", system[1])
	}
	if !strings.HasPrefix(system[2], "<environment_context>") {
		t.Fatalf("environment section = %q", system[2])
	}
	if system[3] != "PERSONA-SUFFIX" {
		t.Fatalf("persona suffix = %q", system[3])
	}
}

func TestAgentPromptOrderUnchangedWithoutPersona(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:              fakeModel,
		Events:             &testEventStore{},
		Checkpoints:        checkpoints,
		WorkingDir:         t.TempDir(),
		EnvironmentContext: true,
		Instructions:       "GUIDANCE",
		MaxSteps:           2,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	_ = collectRunEvents(t, stream)
	var system []string
	for _, message := range fakeModel.requests[0].Messages {
		if message.Role == "system" {
			system = append(system, message.Content)
		}
	}
	if len(system) != 2 || system[0] != "GUIDANCE" || !strings.HasPrefix(system[1], "<environment_context>") {
		t.Fatalf("system messages = %q, want guidance then environment", system)
	}
}

func TestAgentFailsRunOnUnknownPromptVariable(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "must not run"}}}}}
	agent := New(Config{
		Model:         fakeModel,
		Events:        &testEventStore{},
		Checkpoints:   checkpointmemory.New(),
		PersonaPrefix: "Review for {{missing_team}}",
		MaxSteps:      2,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(fakeModel.requests) != 0 {
		t.Fatalf("model calls = %d, want 0 after a prompt-assembly failure", len(fakeModel.requests))
	}
	failures := eventsByType(collected, EventRunError)
	if len(failures) != 1 {
		t.Fatalf("run.error events = %d, want 1", len(failures))
	}
	message, _ := failures[0].Payload["error"].(string)
	if !strings.Contains(message, "assemble system prompt") || !strings.Contains(message, "unknown prompt variable") {
		t.Fatalf("run error = %q", message)
	}
	if !strings.Contains(message, "missing_team") {
		t.Fatalf("run error does not name the variable: %q", message)
	}
}

func TestAgentFailsRunOnMalformedPromptReference(t *testing.T) {
	ctx := context.Background()
	agent := New(Config{
		Model:         &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "must not run"}}}}},
		Events:        &testEventStore{},
		Checkpoints:   checkpointmemory.New(),
		PersonaSuffix: "end {{bad name}} tail",
		MaxSteps:      2,
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
	if !strings.Contains(message, "malformed prompt variable reference") {
		t.Fatalf("run error = %q", message)
	}
}

func TestAgentInstructionFilesWithBracesDoNotFailRuns(t *testing.T) {
	ctx := context.Background()
	root := seedProject(t, "Use {{.Task}} placeholders freely")
	agent := New(Config{
		Model:            &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}},
		Events:           &testEventStore{},
		Checkpoints:      checkpointmemory.New(),
		WorkingDir:       root,
		InstructionFiles: &instructions.Config{},
		MaxSteps:         2,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("discovered instructions with braces failed the run: %v", collected)
	}
}

func TestPromptRegistryOrdersMatchDSHSlots(t *testing.T) {
	if prompt.OrderHarnessIdentity >= prompt.OrderPersonaPrefix ||
		prompt.OrderPersonaPrefix >= prompt.OrderDeploymentPolicy ||
		prompt.OrderDeploymentPolicy >= prompt.OrderRuntimeContext ||
		prompt.OrderRuntimeContext >= prompt.OrderProjectRules ||
		prompt.OrderProjectRules >= prompt.OrderSkillCatalog ||
		prompt.OrderSkillCatalog >= prompt.OrderPersonaSuffix {
		t.Fatal("prompt section order slots drifted from the DSH table")
	}
}

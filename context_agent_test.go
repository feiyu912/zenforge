package zenforge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/instructions"
	"github.com/feiyu912/zenforge/model"
)

func seedProject(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("seed .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	return root
}

func TestAgentInjectsEnvironmentContextAndProjectInstructions(t *testing.T) {
	ctx := context.Background()
	root := seedProject(t, "PROJECT-RULES-XYZ")
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:              fakeModel,
		Tools:              []Tool{&recordingTool{}},
		Events:             events,
		Checkpoints:        checkpoints,
		WorkingDir:         root,
		EnvironmentContext: true,
		InstructionFiles:   &instructions.Config{},
	})

	stream, err := agent.Stream(ctx, Task{RunID: "run_context", Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %+v", collected)
	}
	loaded := eventsByType(collected, EventInstructionsLoaded)
	if len(loaded) != 1 {
		t.Fatalf("instructions.loaded events = %d, want 1", len(loaded))
	}
	files, _ := loaded[0].Payload["files"].([]string)
	wantFile := filepath.Join(root, "AGENTS.md")
	if len(files) != 1 || files[0] != wantFile {
		t.Fatalf("instructions files = %v, want [%s]", files, wantFile)
	}
	if loaded[0].Payload["projectRoot"] != root {
		t.Fatalf("projectRoot = %v, want %v", loaded[0].Payload["projectRoot"], root)
	}

	if len(fakeModel.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fakeModel.requests))
	}
	messages := fakeModel.requests[0].Messages
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want env + instructions + user", len(messages))
	}
	environment := messages[0]
	if environment.Role != "system" || !strings.Contains(environment.Content, "<environment_context>") ||
		!strings.Contains(environment.Content, "<cwd>"+root+"</cwd>") ||
		!strings.Contains(environment.Content, "<tools>") {
		t.Fatalf("unexpected environment context message: %+v", environment)
	}
	project := messages[1]
	if project.Role != "system" || !strings.Contains(project.Content, "# Project instructions") ||
		!strings.Contains(project.Content, "PROJECT-RULES-XYZ") {
		t.Fatalf("unexpected project instructions message: %+v", project)
	}
	if messages[2].Role != "user" || messages[2].Content != "hello" {
		t.Fatalf("user message misplaced: %+v", messages[2])
	}

	cp, err := checkpoints.Load(ctx, "run_context")
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	rendered, _ := cp.State.Meta[metaProjectInstructions].(string)
	if !strings.Contains(rendered, "PROJECT-RULES-XYZ") {
		t.Fatalf("project instructions not persisted in Meta: %v", cp.State.Meta)
	}
	environmentMeta, _ := cp.State.Meta[metaEnvironmentContext].(string)
	if !strings.Contains(environmentMeta, "<environment_context>") {
		t.Fatalf("environment context not persisted in Meta: %v", cp.State.Meta)
	}
}

func TestAgentResumeReplaysPersistedInstructions(t *testing.T) {
	root := seedProject(t, "ORIGINAL-RULES")
	checkpoints := checkpointmemory.New()

	// A mid-run checkpoint carries the frozen prompt context in Meta,
	// exactly as a fresh run would have persisted it.
	state := newRunState("run_resume_context", "work", "", nil)
	state.Phase = harness.RunPhaseModel
	state.Control.Status = harness.RunStatusModelStreaming
	if state.Meta == nil {
		state.Meta = map[string]any{}
	}
	state.Meta[metaProjectInstructions] = "# Project instructions\n\nORIGINAL-RULES\n"
	state.Meta[metaEnvironmentContext] = "<environment_context>\n<cwd>" + root + "</cwd>\n</environment_context>"
	if err := checkpoints.Save(context.Background(), checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   state.RunID,
		Seq:     1,
		State:   state,
		SavedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Save checkpoint returned error: %v", err)
	}

	// The instruction file changes on disk while the run is stopped; a
	// resumed run must replay the persisted context, not rediscover it.
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("CHANGED-RULES"), 0o644); err != nil {
		t.Fatalf("rewrite AGENTS.md: %v", err)
	}

	resumeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "resumed"}}}}}
	resumingAgent := New(Config{
		Model:              resumeModel,
		Checkpoints:        checkpoints,
		WorkingDir:         root,
		EnvironmentContext: true,
		InstructionFiles:   &instructions.Config{},
	})
	events, err := resumingAgent.Resume(context.Background(), "run_resume_context")
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	collected := collectRunEvents(t, events)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("resumed run did not complete: %+v", collected)
	}
	if len(eventsByType(collected, EventInstructionsLoaded)) != 0 {
		t.Fatalf("resume rediscovered instructions instead of replaying Meta")
	}
	if len(resumeModel.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(resumeModel.requests))
	}
	joined := ""
	for _, message := range resumeModel.requests[0].Messages {
		if message.Role == "system" {
			joined += message.Content + "\n"
		}
	}
	if !strings.Contains(joined, "ORIGINAL-RULES") {
		t.Fatalf("resumed request lost the persisted instructions: %s", joined)
	}
	if strings.Contains(joined, "CHANGED-RULES") {
		t.Fatalf("resumed request picked up on-disk changes: %s", joined)
	}
	if !strings.Contains(joined, "<environment_context>") {
		t.Fatalf("resumed request lost the environment context: %s", joined)
	}
}

func TestAgentInstructionsConfigErrorFailsRun(t *testing.T) {
	ctx := context.Background()
	agent := New(Config{
		Model:            &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}},
		WorkingDir:       t.TempDir(),
		InstructionFiles: &instructions.Config{FileNames: []string{"../evil.md"}},
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
	if !strings.Contains(message, "prepare run context") {
		t.Fatalf("run error = %q, want discovery failure", message)
	}
}

func TestAgentWithoutContextConfigKeepsLegacyPrompt(t *testing.T) {
	ctx := context.Background()
	root := seedProject(t, "PROJECT-RULES-XYZ")
	fakeModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{&recordingTool{}},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  root,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete")
	}
	if len(eventsByType(collected, EventInstructionsLoaded)) != 0 {
		t.Fatalf("instructions discovered without configuration")
	}
	messages := fakeModel.requests[0].Messages
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("legacy prompt changed: %+v", messages)
	}
}

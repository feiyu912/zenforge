package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	"github.com/feiyu912/zenforge/examples/internal/modelstub"
	"github.com/feiyu912/zenforge/harness"
)

const (
	testRunID = "long_checkpoint_resume"
	noteOne   = "inspect the workspace layout"
	noteTwo   = "draft the findings"
	finalText = "audit summary: two steps recorded, report written"
)

// TestLongTaskAgentPausesForApprovalAndResumesFromCheckpoint runs the example
// twice, as two real processes over one durable run directory.
//
// The first process has no approval broker, so the tool that closes the task
// out pauses the run: it exits non-zero, prints the id to resume, and leaves a
// checkpoint at phase `approval`. The second process resumes that id with an
// approval broker fed "1", and finishes. What the test asserts is that the
// resumed run read its conversation out of the checkpoint: the model request it
// makes carries the tool results recorded before the pause.
func TestLongTaskAgentPausesForApprovalAndResumesFromCheckpoint(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	binary := buildExample(t)

	// Process 1's script: two recorded steps, then the call that needs a human
	// decision. The runner asks for the third tool call, which pauses the run
	// before any final answer exists.
	first := modelstub.New(
		modelstub.Turn{ToolCalls: []modelstub.ToolCall{{
			ID: "call_step_1", Name: recordToolName, Arguments: map[string]any{"step": 1, "note": noteOne},
		}}},
		modelstub.Turn{ToolCalls: []modelstub.ToolCall{{
			ID: "call_step_2", Name: recordToolName, Arguments: map[string]any{"step": 2, "note": noteTwo},
		}}},
		modelstub.Turn{ToolCalls: []modelstub.ToolCall{{
			ID: "call_finalize", Name: finalizeToolName, Arguments: map[string]any{"report": "audited"},
		}}},
	)
	defer first.Close()

	stdout, stderr, exitCode := runExample(t, binary, first, "",
		"-run-dir", runDir,
		"-workspace", workspace,
		"-run-id", testRunID,
		"-task", "record the audit steps, then finalize the report")

	// A pause is not success and not failure: the process stops without
	// finishing, so it exits non-zero (EX_TEMPFAIL) and says how to continue.
	if exitCode != exitPaused {
		t.Fatalf("first run exit = %d, want %d\nstdout:\n%s\nstderr:\n%s",
			exitCode, exitPaused, stdout, stderr)
	}
	for _, want := range []string{
		"run: started " + testRunID,
		"step: 1",
		"tool: " + recordToolName,
		"tool: " + finalizeToolName,
		"approval: requested " + finalizeToolName,
		"checkpoint: ",
		"run: incomplete " + testRunID,
		"-resume " + testRunID,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("first run transcript is missing %q\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
	if first.Calls() != 3 {
		t.Fatalf("first run made %d model calls, want 3", first.Calls())
	}
	firstRequest := first.Requests()[0]
	if !firstRequest.HasTool(recordToolName) || !firstRequest.HasTool(finalizeToolName) {
		t.Fatalf("first request advertised %v, want both example tools", firstRequest.ToolNames())
	}

	// The run directory is the durable record the second process reads.
	runPath := filepath.Join(runDir, testRunID)
	entries, err := os.ReadDir(runPath)
	if err != nil {
		t.Fatalf("read run directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("run directory %s holds no checkpoint files", runPath)
	}
	for _, name := range []string{"latest.json", "checkpoints.jsonl", "events.jsonl"} {
		if _, err := os.Stat(filepath.Join(runPath, name)); err != nil {
			t.Fatalf("run directory is missing %s: %v (entries: %v)", name, err, entries)
		}
	}

	// The checkpoint is not terminal: it is waiting on the finalize decision,
	// and the conversation recorded before the pause is inside it.
	paused, err := checkpointjsonl.New(runDir).Load(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("load the paused checkpoint: %v", err)
	}
	if paused.State.Phase != harness.RunPhaseApproval {
		t.Fatalf("paused checkpoint phase = %q, want %q", paused.State.Phase, harness.RunPhaseApproval)
	}
	if paused.State.Approval.Waiting == nil || paused.State.Approval.Waiting.ToolName != finalizeToolName {
		t.Fatalf("paused checkpoint approval = %#v, want a waiting %s request", paused.State.Approval, finalizeToolName)
	}
	if recorded := messageText(paused.State.Messages); !strings.Contains(recorded, noteOne) {
		t.Fatalf("paused checkpoint does not carry the step recorded before the pause:\n%s", recorded)
	}

	// Process 2 is a fresh process over the same run directory. Its script is
	// the continuation only: the resumed run makes no model call until the
	// approval resolves and the approved tool result is in the conversation,
	// so the first turn it sees is the one that answers.
	second := modelstub.New(modelstub.Say(finalText))
	defer second.Close()

	stdout, stderr, exitCode = runExample(t, binary, second, "1\n",
		"-run-dir", runDir,
		"-workspace", workspace,
		"-resume", testRunID)
	if exitCode != 0 {
		t.Fatalf("resumed run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	for _, want := range []string{
		"run: resumed " + testRunID,
		"approval: " + finalizeToolName + " approve",
		"tool: " + finalizeToolName,
		"checkpoint: ",
		"run: done " + testRunID,
		"answer: " + finalText,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("resumed run transcript is missing %q\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
	if second.Calls() != 1 {
		t.Fatalf("resumed run made %d model calls, want 1", second.Calls())
	}
	resumed, ok := second.Last()
	if !ok {
		t.Fatalf("resumed run made no model request\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	// This is the checkpoint-and-resume claim: the model the resumed run
	// talked to saw the tool results from the run that was interrupted.
	if !resumed.Delivered(recordToolName) {
		t.Fatalf("resumed request carried no %s result: %+v", recordToolName, resumed.Messages)
	}
	if !resumed.Delivered(finalizeToolName) {
		t.Fatalf("resumed request carried no %s result after the approval: %+v", finalizeToolName, resumed.Messages)
	}
	for _, note := range []string{noteOne, noteTwo} {
		if !strings.Contains(resumed.Text(), note) {
			t.Fatalf("resumed request does not carry the pre-pause note %q:\n%s", note, resumed.Text())
		}
	}

	// The run finished, and the artifact the approved tool wrote is the work
	// the earlier process had already recorded.
	completed, err := checkpointjsonl.New(runDir).Load(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("load the completed checkpoint: %v", err)
	}
	if completed.State.Phase != harness.RunPhaseCompleted {
		t.Fatalf("completed checkpoint phase = %q, want %q", completed.State.Phase, harness.RunPhaseCompleted)
	}
	report, err := os.ReadFile(filepath.Join(workspace, reportName))
	if err != nil {
		t.Fatalf("read the final report: %v", err)
	}
	for _, want := range []string{noteOne, noteTwo, "audited"} {
		if !strings.Contains(string(report), want) {
			t.Fatalf("final report is missing %q:\n%s", want, report)
		}
	}
}

// TestLongTaskAgentResumesWithoutAPendingDecision keeps the resume path honest
// for a run that finished normally: a completed checkpoint replays its terminal
// event instead of rerunning work.
func TestLongTaskAgentResumesWithoutAPendingDecision(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	binary := buildExample(t)

	stub := modelstub.New(
		modelstub.Turn{ToolCalls: []modelstub.ToolCall{{
			ID: "call_step_1", Name: recordToolName, Arguments: map[string]any{"step": 1, "note": noteOne},
		}}},
		modelstub.Say(finalText),
	)
	defer stub.Close()

	stdout, stderr, exitCode := runExample(t, binary, stub, "",
		"-run-dir", runDir,
		"-workspace", workspace,
		"-run-id", testRunID)
	if exitCode != 0 {
		t.Fatalf("run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "run: done "+testRunID) {
		t.Fatalf("run did not finish:\n%s", stdout)
	}

	replay := modelstub.New(modelstub.Say("unused"))
	defer replay.Close()
	stdout, stderr, exitCode = runExample(t, binary, replay, "",
		"-run-dir", runDir,
		"-workspace", workspace,
		"-resume", testRunID)
	if exitCode != 0 {
		t.Fatalf("replay exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "run: resumed "+testRunID) || !strings.Contains(stdout, "run: done "+testRunID) {
		t.Fatalf("terminal checkpoint did not replay its outcome:\n%s", stdout)
	}
	if replay.Calls() != 0 {
		t.Fatalf("resuming a completed run made %d model calls, want 0", replay.Calls())
	}
}

func buildExample(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), programName)
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", programName, err, output)
	}
	return binary
}

// runExample runs the example as a child process against a scripted endpoint
// and returns its transcript and exit code.
func runExample(t *testing.T, binary string, stub *modelstub.Server, stdin string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), stub.Env()...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s did not finish: %v\nstdout:\n%s\nstderr:\n%s", programName, ctx.Err(), stdout.String(), stderr.String())
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s: %v\nstderr:\n%s", programName, err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func messageText(messages []harness.MessageState) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

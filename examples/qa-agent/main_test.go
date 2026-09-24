package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/examples/internal/modelstub"
)

// The fixture the example ships. The test asserts on these exact strings, so a
// change to the skill's description or body is a change the test notices. The
// body marker is plain prose on purpose: the loaded body crosses the wire as
// JSON, which escapes characters like & and < that a heading would carry.
const (
	qaSkillName        = "qa-evidence-lookup"
	qaSkillDescription = "Answer a question from observed workspace evidence instead of recall, using approved shell inspection."
	qaSkillBodyMarker  = "Answer from what the workspace shows, not from memory."
	qaFinalAnswer      = "the workspace contains the requested evidence"
	qaShellCommand     = "echo qa-agent-ok"
	qaShellMarker      = "qa-agent-ok"
)

// TestQAAgentLoadsSkillAndRunsApprovedLocalShell runs the example as a real
// process against a scripted OpenAI-compatible endpoint. The model's words are
// scripted; the adapter, the agent loop, the filesystem skill catalog, the
// shell tool, the approval broker and the transcript are the real ones. The
// test proves three things the example exists to show: progressive disclosure
// (the description is advertised before the body is loaded), a skill load whose
// result reaches the model, and an approved shell command whose output reaches
// the model.
func TestQAAgentLoadsSkillAndRunsApprovedLocalShell(t *testing.T) {
	stub := modelstub.New(
		qaToolCall("skill-1", "load_skill", map[string]any{"name": qaSkillName}),
		qaToolCall("shell-1", "shell", map[string]any{
			"command":     qaShellCommand,
			"description": "confirm the shell path the operator approved",
		}),
		modelstub.Say(qaFinalAnswer),
	)
	defer stub.Close()

	binary := buildQAAgent(t)
	// -sandbox local is what CI uses: no Docker, no credential, no TTY.
	stdout, stderr := runQAAgent(t, binary, stub, t.TempDir(), "1\n",
		"-question", "What does the workspace evidence say?",
		"-sandbox", "local")

	// The transcript is the example's human-readable surface: a line per
	// observable step, in the prefixes the example documents.
	for _, want := range []string{
		"skill: loaded " + qaSkillName,
		"tool: load_skill",
		"tool: shell",
		"approval: shell approve",
		"answer: " + qaFinalAnswer,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
	// The CLI broker owns the prompt on stderr; the operator's "1" selects the
	// first option, which is Approve.
	if !strings.Contains(stderr, "Approve shell command") {
		t.Errorf("stderr does not carry the CLI approval prompt:\n%s", stderr)
	}
	// An approval makes the agent re-invoke the same call, so the transcript
	// must still print one line for the one call the model requested.
	if got := strings.Count(stdout, "tool: shell\n"); got != 1 {
		t.Errorf("the transcript printed %d tool: shell lines, want 1:\n%s", got, stdout)
	}

	requests := stub.Requests()
	if len(requests) != 3 {
		t.Fatalf("the run made %d model calls, want 3; stderr=%s", len(requests), stderr)
	}

	// Turn 1 advertises the skill's name and description -- progressive
	// disclosure -- and the body stays out of the prompt until it is loaded.
	first := requests[0]
	if !first.HasTool("load_skill") || !first.HasTool("shell") {
		t.Fatalf("the first request advertised %v, want load_skill and shell", first.ToolNames())
	}
	for _, want := range []string{"Available skills:", qaSkillName, qaSkillDescription} {
		if !strings.Contains(first.Text(), want) {
			t.Errorf("the first request is missing descriptor %q:\n%s", want, first.Text())
		}
	}
	if strings.Contains(first.Text(), qaSkillBodyMarker) {
		t.Errorf("the first request leaked the skill body before it was loaded:\n%s", first.Text())
	}
	if first.Delivered("load_skill") {
		t.Errorf("the first request already carried a load_skill result:\n%+v", first.Messages)
	}

	// Turn 2 carries the body, which is what proves the load tool actually ran.
	second := requests[1]
	if !second.Delivered("load_skill") {
		t.Fatalf("the second request carried no load_skill result:\n%+v", second.Messages)
	}
	if !strings.Contains(second.Text(), qaSkillBodyMarker) {
		t.Errorf("the loaded skill body is not in the second request:\n%s", second.Text())
	}
	if second.Delivered("shell") {
		t.Errorf("the second request already carried a shell result:\n%+v", second.Messages)
	}

	// Turn 3 carries the approved command's stdout, which is what proves the
	// operator's decision was accepted and the shell ran.
	third := requests[2]
	if !third.Delivered("shell") {
		t.Fatalf("the third request carried no shell result:\n%+v", third.Messages)
	}
	if !strings.Contains(third.Text(), qaShellMarker) {
		t.Errorf("the shell command's output is not in the third request:\n%s", third.Text())
	}
}

// TestQAAgentRunsApprovedDockerShellWhenDockerIsEnabled is the same run path
// with the Docker sandbox backend the example defaults to. It is gated because
// the default CI job has no Docker; the Docker job sets the variable. macOS
// reports Darwin from uname, so a Linux answer proves the command ran in the
// Alpine container rather than on the host.
func TestQAAgentRunsApprovedDockerShellWhenDockerIsEnabled(t *testing.T) {
	if os.Getenv("ZENFORGE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ZENFORGE_DOCKER_INTEGRATION=1 to run qa-agent Docker integration")
	}
	stub := modelstub.New(
		qaToolCall("shell-docker-1", "shell", map[string]any{
			"command":     "uname -s",
			"description": "verify the container the operator approved",
		}),
		modelstub.Say("docker sandbox verified"),
	)
	defer stub.Close()

	binary := buildQAAgent(t)
	stdout, stderr := runQAAgent(t, binary, stub, t.TempDir(), "1\n",
		"-question", "Which kernel is the sandbox running?",
		"-sandbox", "docker",
		"-image", "alpine:3.20")

	for _, want := range []string{
		"skill: loaded " + qaSkillName,
		"tool: shell",
		"approval: shell approve",
		"answer: docker sandbox verified",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "Approve shell command") {
		t.Errorf("stderr does not carry the CLI approval prompt:\n%s", stderr)
	}

	last, ok := stub.Last()
	if !ok {
		t.Fatalf("the endpoint served no request; stderr=%s", stderr)
	}
	if !last.Delivered("shell") {
		t.Fatalf("the last request carried no shell result:\n%+v", last.Messages)
	}
	if !strings.Contains(last.Text(), "Linux") {
		t.Errorf("the Docker shell result is not in the last request:\n%s", last.Text())
	}
}

// qaToolCall scripts one named tool call with an explicit id. modelstub
// generates "call_<index>" when an id is empty, which would repeat across
// turns; the example's transcript prints one `tool:` line per call id, so the
// ids a real provider sends are what the test supplies too.
func qaToolCall(id, name string, arguments any) modelstub.Turn {
	return modelstub.Turn{ToolCalls: []modelstub.ToolCall{{ID: id, Name: name, Arguments: arguments}}}
}

// buildQAAgent builds the example the way a user would run it.
func buildQAAgent(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "qa-agent")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build qa-agent: %v\n%s", err, output)
	}
	return binary
}

// runQAAgent starts the built example with the scripted endpoint's environment,
// a fresh workspace, the shipped skill fixture and the operator's stdin, and
// returns both streams. A non-zero exit fails the test, because the example
// prints qa-agent: <err> to stderr in that case.
func runQAAgent(t *testing.T, binary string, stub *modelstub.Server, workspace, stdin string, flags ...string) (string, string) {
	t.Helper()
	skillRoot, err := filepath.Abs("skills")
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{
		"-workspace", workspace,
		"-skill-root", skillRoot,
	}, flags...)
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(), stub.Env()...)
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("qa-agent exited with %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

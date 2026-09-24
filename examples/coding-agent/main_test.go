package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/examples/internal/modelstub"
)

const (
	fixtureFile = "greeting.txt"
	fixtureOld  = "hello world\n"
)

// TestCodingAgentEditsAFileAndRunsAnApprovedCommand runs the example as a real
// child process against a scripted OpenAI-compatible endpoint. Only the model's
// words are scripted: the provider adapter, the agent loop, the snapshot guard,
// the file and shell policies, the CLI approval broker and the JSONL stores are
// all the example's own code path, and the operator's stdin answers are the ones
// a person would type.
func TestCodingAgentEditsAFileAndRunsAnApprovedCommand(t *testing.T) {
	const (
		task        = "Correct the greeting in " + fixtureFile + " and verify the change."
		newContent  = "hello, coding agent\n"
		shellLine   = "printf 'check-ok\\n'"
		finalAnswer = "Updated greeting.txt and verified it with printf check-ok."
	)
	stub := modelstub.New(
		modelstub.Call("workspace_read", map[string]any{"path": fixtureFile}),
		modelstub.Call("workspace_write", map[string]any{
			"path":        fixtureFile,
			"content":     newContent,
			"description": "Correct the greeting the task names.",
		}),
		modelstub.Call("shell", map[string]any{
			"command":     shellLine,
			"description": "Verify the edited file is still readable.",
		}),
		modelstub.Say(finalAnswer),
	)
	defer stub.Close()

	workspace := newWorkspace(t)
	run := runAgent(t, buildAgent(t), workspace, task, []promptAnswer{
		{prompt: "Approval required: Approve workspace write", answer: "1\n"},
		{prompt: "Approval required: Approve shell command", answer: "1\n"},
	}, stub.Env())
	t.Logf("stdout:\n%s\nstderr:\n%s", run.stdout, run.stderr)

	if run.err != nil {
		t.Fatalf("the agent exited with %v", run.err)
	}

	// The edit is on disk. A transcript alone would not prove a write happened.
	if got := readFile(t, filepath.Join(workspace, fixtureFile)); got != newContent {
		t.Fatalf("%s contains %q, want %q", fixtureFile, got, newContent)
	}

	// The operator was prompted on stderr for both the write and the command,
	// and the transcript records the accepted decisions.
	for _, prompt := range []string{
		"Approval required: Approve workspace write",
		"Approval required: Approve shell command",
	} {
		if !strings.Contains(run.stderr, prompt) {
			t.Fatalf("stderr does not carry the prompt %q", prompt)
		}
	}
	for _, line := range []string{
		"tool: workspace_read",
		"tool: workspace_write",
		"approval: workspace_write approve",
		"write: " + fixtureFile,
		"tool: shell",
		"approval: shell approve",
		"shell: " + shellLine,
		"answer: " + finalAnswer,
	} {
		requireLine(t, run.stdout, line)
	}

	requests := stub.Requests()
	if calls := stub.Calls(); calls != 4 {
		t.Fatalf("the run made %d model calls, want 4 (read, write, shell, answer)", calls)
	}
	for _, name := range []string{"workspace_read", "workspace_write", "workspace_edit", "shell"} {
		if !requests[0].HasTool(name) {
			t.Fatalf("the first request advertised %v, want %s", requests[0].ToolNames(), name)
		}
	}

	// The model was shown the file before it asked to change it: the write call
	// appears in a request that already carried the read's result.
	readIndex, ok := firstRequestDelivering(requests, "workspace_read")
	if !ok {
		t.Fatal("no request carried a workspace_read result; the read never ran")
	}
	writeRequest, ok := firstRequestAsking(requests, "workspace_write")
	if !ok {
		t.Fatal("no request asked for workspace_write")
	}
	if writeRequest <= readIndex || !requests[writeRequest].Delivered("workspace_read") {
		t.Fatalf("the write was requested at request %d and the read result arrived at %d", writeRequest, readIndex)
	}

	// The write actually ran, and so did the shell command: each result came
	// back to the model, which is what separates a real call from an announced
	// one.
	writtenIndex, ok := firstRequestDelivering(requests, "workspace_write")
	if !ok {
		t.Fatal("no request carried a workspace_write result; the write never ran")
	}
	if writtenIndex <= readIndex {
		t.Fatalf("the write result appeared at request %d, before the read result at %d", writtenIndex, readIndex)
	}
	last, ok := stub.Last()
	if !ok {
		t.Fatal("the scripted endpoint was never called")
	}
	if !last.Delivered("shell") {
		t.Fatalf("the final request carried no shell result: %+v", last.Messages)
	}
	if !strings.Contains(last.Text(), "check-ok") {
		t.Fatalf("the shell command's output never reached the model: %q", last.Text())
	}
}

// TestCodingAgentHonoursADeniedWrite answers the write prompt with the CLI
// broker's reject option. The SDK's real behaviour, which this pins: the agent
// loop turns the rejection into an "approval_rejected" tool error, sends it
// back to the model, and lets the run finish, so the file is untouched and the
// run is not a failure -- the refusal is reported, not swallowed.
func TestCodingAgentHonoursADeniedWrite(t *testing.T) {
	const (
		task        = "Correct the greeting in " + fixtureFile + "."
		finalAnswer = "The operator rejected the write, so greeting.txt is unchanged."
	)
	stub := modelstub.New(
		modelstub.Call("workspace_read", map[string]any{"path": fixtureFile}),
		modelstub.Call("workspace_write", map[string]any{
			"path":        fixtureFile,
			"content":     "hello, coding agent\n",
			"description": "Correct the greeting the task names.",
		}),
		modelstub.Say(finalAnswer),
	)
	defer stub.Close()

	workspace := newWorkspace(t)
	run := runAgent(t, buildAgent(t), workspace, task, []promptAnswer{
		{prompt: "Approval required: Approve workspace write", answer: "2\n"},
	}, stub.Env())
	t.Logf("stdout:\n%s\nstderr:\n%s", run.stdout, run.stderr)

	if run.err != nil {
		t.Fatalf("the agent exited with %v; a rejected approval is a tool error the model recovers from", run.err)
	}
	if got := readFile(t, filepath.Join(workspace, fixtureFile)); got != fixtureOld {
		t.Fatalf("%s contains %q after the operator rejected the write, want %q", fixtureFile, got, fixtureOld)
	}
	if !strings.Contains(run.stderr, "Approval required: Approve workspace write") {
		t.Fatalf("stderr does not carry the write prompt:\n%s", run.stderr)
	}
	// The rejection is visible as the operator's decision and nothing else: the
	// agent loop synthesizes "approval_rejected" itself and never calls the
	// tool, so a rejected write correctly has no write line at all.
	requireLine(t, run.stdout, "approval: workspace_write reject")
	refuseLine(t, run.stdout, "tool: workspace_write")
	refuseLine(t, run.stdout, "write: "+fixtureFile)
	requireLine(t, run.stdout, "answer: "+finalAnswer)

	requests := stub.Requests()
	writeRequest, ok := firstRequestAsking(requests, "workspace_write")
	if !ok {
		t.Fatal("no request asked for workspace_write")
	}
	if !requests[writeRequest].Delivered("workspace_write") {
		t.Fatalf("the refusal was never delivered back to the model: %+v", requests[writeRequest].Messages)
	}
	if !strings.Contains(requests[writeRequest].Text(), "approval_rejected") {
		t.Fatalf("the model was not told the write was refused: %q", requests[writeRequest].Text())
	}
}

// buildAgent compiles the example the way a user would run it, so the test
// exercises the binary's own flag parsing and exit codes rather than calling
// into its functions.
func buildAgent(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "coding-agent")
	// go test runs in this package's source directory, so "." is the example.
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -o %s .: %v\n%s", binary, err, output)
	}
	return binary
}

// newWorkspace is the temporary workspace the agent may edit: one file with
// known content, so the test can tell whether it changed.
func newWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, fixtureFile), []byte(fixtureOld), 0o644); err != nil {
		t.Fatalf("seed the workspace: %v", err)
	}
	return workspace
}

type agentRun struct {
	stdout string
	stderr string
	err    error
}

// promptAnswer is one approval prompt the test's operator expects, and the
// option number they type at it. The CLI broker reads the numbered choice
// (1 = Approve, 2 = Reject) and prints it on stderr; there is no yes/no input.
type promptAnswer struct {
	prompt string
	answer string
}

// operator plays the person at the keyboard. It answers each prompt only when
// that prompt appears, which is what a person does and keeps the recorded
// streams in the order an interactive session produces them. The broker also
// accepts several answers written ahead of time, so this is a fidelity choice
// rather than a workaround.
type operator struct {
	mu       sync.Mutex
	log      bytes.Buffer
	answers  []promptAnswer
	answered []bool
	stdin    io.WriteCloser
}

func newOperator(stdin io.WriteCloser, answers []promptAnswer) *operator {
	return &operator{stdin: stdin, answers: answers, answered: make([]bool, len(answers))}
}

// Write is the child's stderr sink: it records what the child printed and
// answers a newly appeared prompt once.
func (o *operator) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.log.Write(p)
	for index, step := range o.answers {
		if o.answered[index] || !strings.Contains(o.log.String(), step.prompt) {
			continue
		}
		o.answered[index] = true
		// A write error means the child already exited; its exit status is the
		// real signal, and the missing answer would show up as a run error.
		_, _ = io.WriteString(o.stdin, step.answer)
	}
	return len(p), nil
}

func (o *operator) stderr() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.log.String()
}

// runAgent starts the built example with the stub's environment, a hermetic run
// directory, and an operator answering the approval prompts on its stdin.
func runAgent(t *testing.T, binary, workspace, task string, answers []promptAnswer, stubEnv []string) agentRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	child := exec.CommandContext(ctx, binary, "-workspace", workspace, "-run-dir", t.TempDir(), "-task", task)
	child.Env = append(os.Environ(), stubEnv...)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatalf("open the child's stdin: %v", err)
	}
	operator := newOperator(stdin, answers)
	var stdout bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = operator
	err = child.Run()
	_ = stdin.Close()
	if ctx.Err() != nil {
		t.Fatalf("the agent did not exit within the timeout\nstdout:\n%s\nstderr:\n%s", stdout.String(), operator.stderr())
	}
	return agentRun{stdout: stdout.String(), stderr: operator.stderr(), err: err}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// requireLine asserts the transcript carries a whole line, so a prefix cannot
// pass by appearing inside some other value.
func requireLine(t *testing.T, output, want string) {
	t.Helper()
	if !strings.Contains(output, want+"\n") {
		t.Fatalf("the transcript does not carry the line %q:\n%s", want, output)
	}
}

// refuseLine asserts a line the transcript must not carry: nothing was written,
// so nothing may be rendered as if it had been.
func refuseLine(t *testing.T, output, unwanted string) {
	t.Helper()
	if strings.Contains(output, unwanted+"\n") {
		t.Fatalf("the transcript carries %q, but the call was refused:\n%s", unwanted, output)
	}
}

// firstRequestDelivering is the index of the first request whose messages
// already carried a tool's result, which is how a test proves the tool ran
// rather than only being announced.
func firstRequestDelivering(requests []modelstub.Request, toolName string) (int, bool) {
	for index, request := range requests {
		if request.Delivered(toolName) {
			return index, true
		}
	}
	return 0, false
}

// firstRequestAsking is the index of the first request whose assistant messages
// asked for a tool, i.e. the model call that decided to use it.
func firstRequestAsking(requests []modelstub.Request, toolName string) (int, bool) {
	for index, request := range requests {
		for _, message := range request.Messages {
			if message.Role != "assistant" {
				continue
			}
			for _, call := range message.ToolCalls {
				if call.Function.Name == toolName {
					return index, true
				}
			}
		}
	}
	return 0, false
}

package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/feiyu912/zenforge/benchmarks/internal/scripted"
)

// PhaseResult is one runner process's reported outcome, as the harness saw it
// after mapping its exit code and reading BENCH_RESULT.
type PhaseResult struct {
	Phase  string
	Status string
	Detail string
}

// Input is everything a task verifier judges a run from: the workspace the
// runner left behind, the durable state directory, the statuses of the
// processes the harness started, and the endpoint's record of every request.
//
// The endpoint record is not decoration. A runner could write the artifact
// directly without ever calling the model or the tool, and only the recorded
// conversation can tell the difference.
type Input struct {
	Metadata  Metadata
	Approval  string
	Workspace string
	StateDir  string
	// StateFilesAfterRun is how many files the state directory held when the
	// first process exited, measured before the resume process started. It is
	// what makes "the first process wrote durable state" observable instead of
	// inferred from a later success.
	StateFilesAfterRun int
	Phases             []PhaseResult
	Requests           []scripted.Request
}

// Verdict is a task verifier's judgment.
type Verdict struct {
	Success  bool
	Checks   []string
	Failures []string
}

func (v *Verdict) pass(format string, args ...any) {
	v.Checks = append(v.Checks, fmt.Sprintf(format, args...))
}

func (v *Verdict) fail(format string, args ...any) {
	v.Failures = append(v.Failures, fmt.Sprintf(format, args...))
}

// Verify runs the verifier for the task the input names. The task's own
// metadata decides what success means; this function only dispatches.
//
// Every task is judged first against the same universal rule: the runner sent
// the task's frozen query as the user message. Without it, the byte-cost metric
// would reward a runner for writing a shorter task sentence than its
// competitors, which is a difference between prompts and not between
// frameworks.
func Verify(in Input) Verdict {
	var verdict Verdict
	requireQuery(&verdict, in)
	switch in.Metadata.ID {
	case EditFile.ID:
		verifyEditFile(in, &verdict)
	case ApproveCommand.ID:
		verifyApproveCommand(in, &verdict)
	case DurableTask.ID:
		verifyDurableTask(in, &verdict)
	default:
		verdict.fail("no verifier for task %q", in.Metadata.ID)
	}
	verdict.Success = len(verdict.Failures) == 0
	return verdict
}

// requireQuery checks that the frozen user message reached the endpoint
// verbatim, ignoring only surrounding whitespace.
func requireQuery(verdict *Verdict, in Input) {
	query := strings.TrimSpace(in.Metadata.Query)
	if query == "" {
		verdict.fail("the task declares no frozen query, so prompt cost is not comparable")
		return
	}
	for _, request := range in.Requests {
		for _, message := range request.Messages {
			if message.Role == "user" && strings.TrimSpace(message.Content) == query {
				verdict.pass("the task's frozen query was sent as the user message")
				return
			}
		}
	}
	verdict.fail("no request sent the task's frozen user message %q", query)
}

// verifyEditFile judges the read-then-write task: the edited file exists with
// the expected bytes, the read actually read the seeded input, and the read's
// result reached the model before the write was issued.
func verifyEditFile(in Input, verdict *Verdict) {
	requirePhase(verdict, in, PhaseRun, "completed")

	out := in.Metadata.Artifacts[0]
	checkArtifact(verdict, in.Workspace, out)

	writeTurn := turnIssuing(in.Metadata.Script, ToolWriteFile)
	if writeTurn < 0 {
		verdict.fail("the edit-file script never issues %s", ToolWriteFile)
		return
	}
	served := servedTurn(in.Requests, writeTurn)
	if len(served) == 0 {
		verdict.fail("the endpoint never issued the %s turn, so no write was asked for", ToolWriteFile)
		return
	}
	if id, content, ok := deliveredBefore(served[0], ToolReadFile); ok {
		verdict.pass("the %s result for call %s reached the model before %s was issued", ToolReadFile, id, ToolWriteFile)
		if expected := in.Metadata.Seed["input.txt"]; expected != "" && !strings.Contains(content, strings.TrimSpace(expected)) {
			verdict.fail("the %s result did not contain input.txt's contents: a tool result was delivered, but it was not the read of the seeded input", ToolReadFile)
		}
	} else {
		verdict.fail("no %s result reached the model before the %s turn", ToolReadFile, ToolWriteFile)
	}
	if !requestedRead(in.Requests, "input.txt") {
		verdict.fail("no request asked %s for input.txt", ToolReadFile)
	}
}

// verifyApproveCommand judges the approval task in the mode the harness ran it
// with.
//
// With approve, the command must have run exactly once and left its effect. The
// command appends to a file, so a second execution would be visible as a second
// line: "exactly once" is judged from the artifact, not from a tool call count
// that a retry under the same call id would hide.
//
// With reject, the same script must leave no effect at all, even though the
// framework was asked for the command and had to deliver the denial back to the
// model.
func verifyApproveCommand(in Input, verdict *Verdict) {
	requirePhase(verdict, in, PhaseRun, "completed")

	artifact := in.Metadata.Artifacts[0]
	command := in.Metadata.Command()
	if command == "" {
		verdict.fail("the approve-command script issues no %s call", ToolRunShell)
		return
	}
	calls := runShellCalls(in.Requests)
	matching := matchingCommands(calls, command)
	if len(matching) == 0 {
		verdict.fail("the endpoint never saw a %s call for %q", ToolRunShell, command)
	} else {
		verdict.pass("the %s call for %q was issued %d time(s) with distinct call ids", ToolRunShell, command, len(matching))
	}

	if in.Approval == ApprovalReject {
		if exists, err := fileExists(filepath.Join(in.Workspace, artifact.Path)); err != nil {
			verdict.fail("inspect %s: %v", artifact.Path, err)
		} else if exists {
			verdict.fail("%s exists after the operator rejected the command: the command ran anyway", artifact.Path)
		} else {
			verdict.pass("%s was not created after the rejection, so the command did not run", artifact.Path)
		}
		return
	}

	checkArtifact(verdict, in.Workspace, artifact)
	if len(matching) != 1 {
		verdict.fail("the command was requested %d times; the task asks for exactly one %s call", len(matching), ToolRunShell)
	}
	if len(matching) > 0 {
		content, delivered := toolResultContent(in.Requests, matching[0].ID)
		stdout := in.Metadata.CommandStdout
		switch {
		case !delivered:
			verdict.fail("no tool result for call %s reached the model", matching[0].ID)
		case stdout != "" && !strings.Contains(content, stdout):
			verdict.fail("the tool result for call %s does not contain the command's stdout %q, so the command's output never reached the model",
				matching[0].ID, stdout)
		case strings.TrimSpace(content) == "":
			verdict.fail("the tool result for call %s was empty, so the command's output never reached the model", matching[0].ID)
		default:
			verdict.pass("the tool result for call %s delivered the command's stdout (%q) to the model", matching[0].ID, stdout)
		}
	}
}

// verifyDurableTask judges the recovery task.
//
// Four things have to be true at once: the first process stopped durably with
// state on disk, the second process finished, the artifact written before the
// pause survived it, and the command the pause was waiting on ran only after
// the resume delivered its approval back to the model.
func verifyDurableTask(in Input, verdict *Verdict) {
	requirePhase(verdict, in, PhaseRun, "paused")
	requirePhase(verdict, in, PhaseResume, "completed")

	for _, artifact := range in.Metadata.Artifacts {
		checkArtifact(verdict, in.Workspace, artifact)
	}

	if in.StateFilesAfterRun <= 0 {
		verdict.fail("the state directory held no files after the first process exited: the pause left nothing durable to resume")
	} else {
		verdict.pass("the first process left %d durable state file(s) before the resume", in.StateFilesAfterRun)
	}

	// The two steps must have been recorded before the pause, which the
	// endpoint can confirm: the second write_file turn is only issued after the
	// first write's result came back, and the pause happens two turns later.
	stepTurn := turnIssuing(in.Metadata.Script, ToolWriteFile)
	if stepTurn < 0 || len(servedTurn(in.Requests, stepTurn)) == 0 {
		verdict.fail("the endpoint never issued the step-recording %s turn", ToolWriteFile)
	} else {
		verdict.pass("the step-recording %s turn was issued before the pause", ToolWriteFile)
	}

	// The approval the pause was waiting on must have been resolved on resume,
	// and the command's result must have reached the model, because that is
	// what let the script advance to the artifact turn.
	shellTurn := turnIssuing(in.Metadata.Script, ToolRunShell)
	if shellTurn < 0 {
		verdict.fail("the durable-task script issues no %s call", ToolRunShell)
		return
	}
	resumeTurn := shellTurn + 1
	if resumeTurn > len(in.Metadata.Script.Turns)-1 {
		resumeTurn = len(in.Metadata.Script.Turns) - 1
	}
	advanced := false
	stdoutDelivered := false
	stdout := in.Metadata.CommandStdout
	for _, request := range servedTurn(in.Requests, resumeTurn) {
		if _, content, ok := deliveredBefore(request, ToolRunShell); ok {
			advanced = true
			stdoutDelivered = stdout == "" || strings.Contains(content, stdout)
			break
		}
	}
	switch {
	case !advanced:
		verdict.fail("the endpoint never issued the post-approval turn from a request carrying the %s result, so the resumed run never delivered the approved command's result to the model", ToolRunShell)
	case !stdoutDelivered:
		verdict.fail("the approved %s result reached the model without the command's stdout %q", ToolRunShell, stdout)
	default:
		verdict.pass("the approved %s result (stdout %q) reached the model and the script advanced after the resume", ToolRunShell, stdout)
	}
}

// requirePhase checks one named process reported the expected status.
func requirePhase(verdict *Verdict, in Input, phase, want string) {
	for _, result := range in.Phases {
		if result.Phase != phase {
			continue
		}
		if result.Status == want {
			verdict.pass("phase %s reported %s", phase, want)
		} else {
			detail := strings.TrimSpace(result.Detail)
			if detail != "" {
				detail = ": " + detail
			}
			verdict.fail("phase %s reported %s, want %s%s", phase, result.Status, want, detail)
		}
		return
	}
	verdict.fail("phase %s never ran", phase)
}

// checkArtifact compares one expected file literally.
func checkArtifact(verdict *Verdict, workspace string, artifact Artifact) {
	path := filepath.Join(workspace, artifact.Path)
	content, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		verdict.fail("%s does not exist", artifact.Path)
	case err != nil:
		verdict.fail("read %s: %v", artifact.Path, err)
	case string(content) != artifact.Content:
		verdict.fail("%s holds %q, want %q", artifact.Path, string(content), artifact.Content)
	default:
		verdict.pass("%s holds the expected %d bytes (%s)", artifact.Path, len(content), artifact.Description)
	}
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// turnIssuing finds the first turn that issues a named tool.
func turnIssuing(script scripted.Script, name string) int {
	for index, turn := range script.Turns {
		for _, call := range turn.ToolCalls {
			if call.Name == name {
				return index
			}
		}
	}
	return -1
}

// servedTurn returns the requests the endpoint answered with the given turn,
// in arrival order.
func servedTurn(requests []scripted.Request, turn int) []scripted.Request {
	var served []scripted.Request
	for _, request := range requests {
		if request.ServedTurn == turn {
			served = append(served, request)
		}
	}
	return served
}

// deliveredBefore reports whether one request carried a tool result for a call
// of the named tool, positioned after the assistant message that requested it.
// The position matters: a result that arrives before its own request did not
// come from running that tool.
func deliveredBefore(request scripted.Request, name string) (string, string, bool) {
	requestedAt := map[string]int{}
	for index, message := range request.Messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Name == name {
				requestedAt[call.ID] = index
			}
		}
	}
	for index, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		if requested, ok := requestedAt[message.ToolCallID]; ok && requested < index {
			return message.ToolCallID, message.Content, true
		}
	}
	return "", "", false
}

// toolResultContent returns the first tool result recorded for a call id.
func toolResultContent(requests []scripted.Request, id string) (string, bool) {
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Role == "tool" && message.ToolCallID == id {
				return message.Content, true
			}
		}
	}
	return "", false
}

// requestedRead reports whether any request asked the read tool for a path.
func requestedRead(requests []scripted.Request, path string) bool {
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Role != "assistant" {
				continue
			}
			for _, call := range message.ToolCalls {
				if call.Name != ToolReadFile {
					continue
				}
				var arguments struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(call.Arguments), &arguments); err == nil && arguments.Path == path {
					return true
				}
			}
		}
	}
	return false
}

// runShellCall is one distinct run_shell call the endpoint saw.
type runShellCall struct {
	ID      string
	Command string
}

// runShellCalls returns each distinct run_shell call id once, in the order it
// first appeared. Distinct ids rather than occurrences: a retried call keeps
// its id, so the same id appearing in many requests is one call.
func runShellCalls(requests []scripted.Request) []runShellCall {
	seen := map[string]bool{}
	var calls []runShellCall
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Role != "assistant" {
				continue
			}
			for _, call := range message.ToolCalls {
				if call.Name != ToolRunShell || seen[call.ID] {
					continue
				}
				seen[call.ID] = true
				calls = append(calls, runShellCall{ID: call.ID, Command: commandArgument(call.Arguments)})
			}
		}
	}
	return calls
}

func matchingCommands(calls []runShellCall, command string) []runShellCall {
	var matching []runShellCall
	for _, call := range calls {
		if call.Command == command {
			matching = append(matching, call)
		}
	}
	return matching
}

func commandArgument(arguments string) string {
	var decoded struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return ""
	}
	return decoded.Command
}

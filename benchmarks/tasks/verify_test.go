package tasks_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/benchmarks/internal/scripted"
	"github.com/feiyu912/zenforge/benchmarks/tasks"
)

func userMessage(content string) scripted.Message {
	return scripted.Message{Role: "user", Content: content}
}

func assistantMessage(calls ...scripted.IssuedCall) scripted.Message {
	return scripted.Message{Role: "assistant", ToolCalls: calls}
}

func toolMessage(id, content string) scripted.Message {
	return scripted.Message{Role: "tool", ToolCallID: id, Content: content}
}

func servedRequest(turn int, messages ...scripted.Message) scripted.Request {
	return scripted.Request{ServedTurn: turn, Messages: messages}
}

func issuedCall(id, name, arguments string) scripted.IssuedCall {
	return scripted.IssuedCall{ID: id, Name: name, Arguments: arguments}
}

// writeWorkspace creates a workspace holding the given files.
func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func verdictFor(t *testing.T, input tasks.Input) tasks.Verdict {
	t.Helper()
	verdict := tasks.Verify(input)
	if verdict.Success && len(verdict.Failures) != 0 {
		t.Fatalf("verdict is successful but carries failures: %v", verdict.Failures)
	}
	if !verdict.Success && len(verdict.Failures) == 0 {
		t.Fatalf("verdict failed without naming a failure")
	}
	return verdict
}

func failedWith(t *testing.T, verdict tasks.Verdict, fragment string) {
	t.Helper()
	for _, failure := range verdict.Failures {
		if strings.Contains(failure, fragment) {
			return
		}
	}
	t.Fatalf("failures %v do not mention %q", verdict.Failures, fragment)
}

// TestVerifyEditFile covers the read-then-write task's judgment, including the
// call-order rule that separates "wrote the file" from "read the input and then
// wrote the file".
func TestVerifyEditFile(t *testing.T) {
	expected := tasks.EditFile.Artifacts[0]
	delivered := []scripted.Request{
		servedRequest(0, userMessage(tasks.EditFile.Query)),
		servedRequest(1,
			userMessage(tasks.EditFile.Query),
			assistantMessage(issuedCall("read-1", tasks.ToolReadFile, `{"path":"input.txt"}`)),
			toolMessage("read-1", tasks.EditFile.Seed["input.txt"]),
		),
	}

	t.Run("success", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, map[string]string{expected.Path: expected.Content}),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}},
			Requests:  delivered,
		})
		if !verdict.Success {
			t.Fatalf("verdict failed: %v", verdict.Failures)
		}
		if len(verdict.Checks) < 3 {
			t.Fatalf("checks = %v, want the artifact, call-order, and read checks", verdict.Checks)
		}
	})

	t.Run("missing artifact", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, nil),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}},
			Requests:  delivered,
		})
		failedWith(t, verdict, "does not exist")
	})

	t.Run("wrong artifact", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, map[string]string{expected.Path: "something else\n"}),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}},
			Requests:  delivered,
		})
		failedWith(t, verdict, "want")
	})

	t.Run("write issued without the read result", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, map[string]string{expected.Path: expected.Content}),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}},
			Requests: []scripted.Request{
				servedRequest(0, userMessage(tasks.EditFile.Query)),
				servedRequest(1, userMessage(tasks.EditFile.Query)),
			},
		})
		failedWith(t, verdict, "no read_file result reached the model")
	})

	t.Run("fabricated read result", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, map[string]string{expected.Path: expected.Content}),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}},
			Requests: []scripted.Request{
				servedRequest(0, userMessage(tasks.EditFile.Query)),
				servedRequest(1,
					userMessage(tasks.EditFile.Query),
					assistantMessage(issuedCall("read-1", tasks.ToolReadFile, `{"path":"input.txt"}`)),
					toolMessage("read-1", "contents the tool never read"),
				),
			},
		})
		failedWith(t, verdict, "did not contain input.txt's contents")
	})

	t.Run("failed phase", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, map[string]string{expected.Path: expected.Content}),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "failed"}},
			Requests:  delivered,
		})
		failedWith(t, verdict, "want completed")
	})
}

// TestVerifyApproveCommand covers both modes: with approve the command must have
// run exactly once and its result must have reached the model; with reject it
// must not have run at all.
func TestVerifyApproveCommand(t *testing.T) {
	artifact := tasks.ApproveCommand.Artifacts[0]
	command := tasks.ApproveCommand.Command()
	call := issuedCall("shell-1", tasks.ToolRunShell, `{"command":"`+command+`"}`)
	requests := []scripted.Request{
		servedRequest(0, userMessage(tasks.ApproveCommand.Query)),
		servedRequest(1,
			userMessage(tasks.ApproveCommand.Query),
			assistantMessage(call),
			toolMessage("shell-1", `{"command":"`+command+`","output":"`+tasks.ApproveCommand.CommandStdout+`\n"}`),
		),
	}
	completed := []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}}

	t.Run("approve success", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.ApproveCommand,
			Approval:  tasks.ApprovalApprove,
			Workspace: writeWorkspace(t, map[string]string{artifact.Path: artifact.Content}),
			Phases:    completed,
			Requests:  requests,
		})
		if !verdict.Success {
			t.Fatalf("verdict failed: %v", verdict.Failures)
		}
	})

	t.Run("approve ran twice", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.ApproveCommand,
			Approval:  tasks.ApprovalApprove,
			Workspace: writeWorkspace(t, map[string]string{artifact.Path: artifact.Content + artifact.Content}),
			Phases:    completed,
			Requests:  requests,
		})
		failedWith(t, verdict, "want")
	})

	t.Run("approve result was empty", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata: tasks.ApproveCommand,
			Approval: tasks.ApprovalApprove,
			Workspace: writeWorkspace(t, map[string]string{
				artifact.Path: artifact.Content,
			}),
			Phases: completed,
			Requests: []scripted.Request{
				servedRequest(0, userMessage(tasks.ApproveCommand.Query)),
				servedRequest(1, userMessage(tasks.ApproveCommand.Query), assistantMessage(call), toolMessage("shell-1", "")),
			},
		})
		failedWith(t, verdict, "never reached the model")
	})

	t.Run("approve requested twice", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.ApproveCommand,
			Approval:  tasks.ApprovalApprove,
			Workspace: writeWorkspace(t, map[string]string{artifact.Path: artifact.Content}),
			Phases:    completed,
			Requests: append(requests,
				servedRequest(0, userMessage(tasks.ApproveCommand.Query), assistantMessage(
					issuedCall("shell-2", tasks.ToolRunShell, `{"command":"`+command+`"}`),
				)),
			),
		})
		failedWith(t, verdict, "requested 2 times")
	})

	t.Run("reject success", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.ApproveCommand,
			Approval:  tasks.ApprovalReject,
			Workspace: writeWorkspace(t, nil),
			Phases:    completed,
			Requests:  requests,
		})
		if !verdict.Success {
			t.Fatalf("verdict failed: %v", verdict.Failures)
		}
	})

	t.Run("reject but the command ran", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.ApproveCommand,
			Approval:  tasks.ApprovalReject,
			Workspace: writeWorkspace(t, map[string]string{artifact.Path: artifact.Content}),
			Phases:    completed,
			Requests:  requests,
		})
		failedWith(t, verdict, "the command ran anyway")
	})

	t.Run("reject without the call being made", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.ApproveCommand,
			Approval:  tasks.ApprovalReject,
			Workspace: writeWorkspace(t, nil),
			Phases:    completed,
			Requests:  []scripted.Request{servedRequest(0, userMessage(tasks.ApproveCommand.Query))},
		})
		failedWith(t, verdict, "never saw a run_shell call")
	})
}

// TestVerifyDurableTask covers the recovery task: a pause, the state it left,
// the artifact that survived it, and the approval the resume delivered.
func TestVerifyDurableTask(t *testing.T) {
	steps := tasks.DurableTask.Artifacts[0]
	output := tasks.DurableTask.Artifacts[1]
	milestone := tasks.DurableTask.Artifacts[2]
	command := tasks.DurableTask.Command()

	conversation := []scripted.Message{
		userMessage(tasks.DurableTask.Query),
		assistantMessage(issuedCall("step-1", tasks.ToolWriteFile, `{"path":"steps.txt","content":"step 1: read the input\n"}`)),
		toolMessage("step-1", "written"),
		assistantMessage(issuedCall("step-2", tasks.ToolWriteFile, `{"path":"steps.txt","content":"step 1: read the input\nstep 2: draft the output\n"}`)),
		toolMessage("step-2", "written"),
		assistantMessage(issuedCall("shell-1", tasks.ToolRunShell, `{"command":"`+command+`"}`)),
		toolMessage("shell-1", `{"command":"`+command+`","output":"`+tasks.DurableTask.CommandStdout+`\n"}`),
		assistantMessage(issuedCall("out-1", tasks.ToolWriteFile, `{"path":"out.txt","content":"durable task complete\n"}`)),
		toolMessage("out-1", "written"),
	}
	requests := []scripted.Request{
		servedRequest(0, conversation[0]),
		servedRequest(1, conversation[0:3]...),
		servedRequest(2, conversation[0:5]...),
		servedRequest(3, conversation[0:7]...),
		servedRequest(4, conversation[0:9]...),
	}
	files := map[string]string{
		steps.Path:     steps.Content,
		output.Path:    output.Content,
		milestone.Path: milestone.Content,
	}
	phases := []tasks.PhaseResult{
		{Phase: tasks.PhaseRun, Status: "paused"},
		{Phase: tasks.PhaseResume, Status: "completed"},
	}

	t.Run("success", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:           tasks.DurableTask,
			Workspace:          writeWorkspace(t, files),
			StateDir:           t.TempDir(),
			StateFilesAfterRun: 3,
			Phases:             phases,
			Requests:           requests,
		})
		if !verdict.Success {
			t.Fatalf("verdict failed: %v", verdict.Failures)
		}
	})

	t.Run("no durable state", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:  tasks.DurableTask,
			Workspace: writeWorkspace(t, files),
			Phases:    phases,
			Requests:  requests,
		})
		failedWith(t, verdict, "left nothing durable")
	})

	t.Run("never paused", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:           tasks.DurableTask,
			Workspace:          writeWorkspace(t, files),
			StateFilesAfterRun: 3,
			Phases: []tasks.PhaseResult{
				{Phase: tasks.PhaseRun, Status: "completed"},
				{Phase: tasks.PhaseResume, Status: "completed"},
			},
			Requests: requests,
		})
		failedWith(t, verdict, "want paused")
	})

	t.Run("resume never finished", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:           tasks.DurableTask,
			Workspace:          writeWorkspace(t, files),
			StateFilesAfterRun: 3,
			Phases:             []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "paused"}},
			Requests:           requests,
		})
		failedWith(t, verdict, "phase resume never ran")
	})

	t.Run("steps lost across the pause", func(t *testing.T) {
		broken := map[string]string{
			steps.Path:     "step 1: read the input\n",
			output.Path:    output.Content,
			milestone.Path: milestone.Content,
		}
		verdict := verdictFor(t, tasks.Input{
			Metadata:           tasks.DurableTask,
			Workspace:          writeWorkspace(t, broken),
			StateFilesAfterRun: 3,
			Phases:             phases,
			Requests:           requests,
		})
		failedWith(t, verdict, steps.Path)
	})

	t.Run("recovery turn without the approved result", func(t *testing.T) {
		verdict := verdictFor(t, tasks.Input{
			Metadata:           tasks.DurableTask,
			Workspace:          writeWorkspace(t, files),
			StateFilesAfterRun: 3,
			Phases:             phases,
			Requests: []scripted.Request{
				servedRequest(0, conversation[0]),
				servedRequest(1, conversation[0:3]...),
				servedRequest(2, conversation[0:5]...),
				servedRequest(3, conversation[0:6]...),
			},
		})
		failedWith(t, verdict, "never delivered the approved command's result")
	})
}

// TestScriptsMatchMetadata guards the seam between the frozen JSON scripts and
// the Go metadata: a script edited without its verifier (or the reverse) would
// make success mean something the task never asked for.
func TestScriptsMatchMetadata(t *testing.T) {
	editWriteTurn := turnToolArguments(t, tasks.EditFile.Script.Turns, tasks.ToolWriteFile)
	var editWrite struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(editWriteTurn, &editWrite); err != nil {
		t.Fatalf("edit-file write arguments: %v", err)
	}
	if editWrite.Path != tasks.EditFile.Artifacts[0].Path || editWrite.Content != tasks.EditFile.Artifacts[0].Content {
		t.Fatalf("edit-file writes %+v, artifact expects %+v", editWrite, tasks.EditFile.Artifacts[0])
	}
	if got, want := tasks.ApproveCommand.Command(), "echo approved | tee -a approval.txt"; got != want {
		t.Fatalf("approve-command runs %q, want %q", got, want)
	}
	if got, want := tasks.DurableTask.Command(), "echo milestone | tee milestone.txt"; got != want {
		t.Fatalf("durable-task runs %q, want %q", got, want)
	}
	if got := turnToolArguments(t, tasks.DurableTask.Script.Turns, tasks.ToolRunShell); len(got) == 0 {
		t.Fatalf("durable-task has no run_shell call")
	}
	var steps map[string]string
	if err := json.Unmarshal(turnToolArgumentsAt(t, tasks.DurableTask.Script.Turns, 1, tasks.ToolWriteFile), &steps); err != nil {
		t.Fatalf("durable-task second step arguments: %v", err)
	}
	if steps["content"] != tasks.DurableTask.Artifacts[0].Content {
		t.Fatalf("durable-task records %q, artifact expects %q", steps["content"], tasks.DurableTask.Artifacts[0].Content)
	}
	for _, task := range tasks.All {
		for _, tool := range task.Tools {
			if tool == "" {
				t.Fatalf("%s names an empty tool", task.ID)
			}
		}
		if len(task.Approvals) == 0 {
			t.Fatalf("%s has no approval mode", task.ID)
		}
	}
}

// turnToolArguments is the arguments of the first call of a named tool.
func turnToolArguments(t *testing.T, turns []scripted.Turn, name string) []byte {
	t.Helper()
	for index, turn := range turns {
		for _, call := range turn.ToolCalls {
			if call.Name == name {
				return argumentsAt(t, turns, index, name)
			}
		}
	}
	t.Fatalf("script has no %s call", name)
	return nil
}

// turnToolArgumentsAt is the arguments of the call of a named tool in a
// specific turn.
func turnToolArgumentsAt(t *testing.T, turns []scripted.Turn, turnIndex int, name string) []byte {
	t.Helper()
	return argumentsAt(t, turns, turnIndex, name)
}

func argumentsAt(t *testing.T, turns []scripted.Turn, turnIndex int, name string) []byte {
	t.Helper()
	if turnIndex >= len(turns) {
		t.Fatalf("turn %d does not exist", turnIndex)
	}
	for _, call := range turns[turnIndex].ToolCalls {
		if call.Name == name {
			return call.Arguments
		}
	}
	t.Fatalf("turn %d has no %s call", turnIndex, name)
	return nil
}

// TestSelectResolvesTaskLists covers the harness's task selection.
func TestSelectResolvesTaskLists(t *testing.T) {
	all, err := tasks.Select("all")
	if err != nil || len(all) != len(tasks.All) {
		t.Fatalf("Select(all) = (%d tasks, %v)", len(all), err)
	}
	one, err := tasks.Select("durable-task")
	if err != nil || len(one) != 1 || one[0].ID != "durable-task" {
		t.Fatalf("Select(durable-task) = (%+v, %v)", one, err)
	}
	two, err := tasks.Select("edit-file, durable-task")
	if err != nil || len(two) != 2 {
		t.Fatalf("Select(list) = (%d tasks, %v)", len(two), err)
	}
	if _, err := tasks.Select("edit-file,nope"); err == nil {
		t.Fatalf("Select with an unknown id = nil error")
	}
}

// TestSeedWorkspaceWritesInputs covers the harness's workspace setup.
func TestSeedWorkspaceWritesInputs(t *testing.T) {
	dir := t.TempDir()
	if err := tasks.EditFile.SeedWorkspace(dir); err != nil {
		t.Fatalf("SeedWorkspace: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "input.txt"))
	if err != nil {
		t.Fatalf("read seeded input: %v", err)
	}
	if string(data) != tasks.EditFile.Seed["input.txt"] {
		t.Fatalf("seeded input = %q", string(data))
	}
	if err := tasks.ApproveCommand.SeedWorkspace(dir); err != nil {
		t.Fatalf("SeedWorkspace without seed files: %v", err)
	}
}

// TestFrozenQueriesArePresentAndDistinct freezes the exact user message every
// runner must send. The strings are spelled out here rather than read back from
// the metadata: a change to a task's query is a change to the benchmark's cost
// baseline, and it should have to be made twice on purpose.
func TestFrozenQueriesArePresentAndDistinct(t *testing.T) {
	want := map[string]string{
		"edit-file":       "Read input.txt, then write its edited contents to out.txt.",
		"approve-command": "Append the word approved to approval.txt with a shell command.",
		"durable-task":    "Record step 1 and step 2 in steps.txt, create milestone.txt with a shell command, then write the final artifact to out.txt.",
	}
	seen := map[string]string{}
	for _, task := range tasks.All {
		if task.Query != want[task.ID] {
			t.Fatalf("%s query = %q, want %q", task.ID, task.Query, want[task.ID])
		}
		if task.Script.Query != task.Query {
			t.Fatalf("%s metadata query %q does not match its script's %q", task.ID, task.Query, task.Script.Query)
		}
		if other, ok := seen[task.Query]; ok {
			t.Fatalf("%s and %s share one query, so their prompt costs are not distinguishable", task.ID, other)
		}
		seen[task.Query] = task.ID
	}
}

// TestVerifyRequiresTheFrozenQuery is the survey rule that keeps the byte-cost
// metric comparable: a request that asks the task in a shorter sentence of its
// own is not the frozen task.
func TestVerifyRequiresTheFrozenQuery(t *testing.T) {
	artifact := tasks.EditFile.Artifacts[0]
	input := func(message string) tasks.Input {
		return tasks.Input{
			Metadata:  tasks.EditFile,
			Workspace: writeWorkspace(t, map[string]string{artifact.Path: artifact.Content}),
			Phases:    []tasks.PhaseResult{{Phase: tasks.PhaseRun, Status: "completed"}},
			Requests: []scripted.Request{
				servedRequest(0, userMessage(message)),
				servedRequest(1,
					userMessage(message),
					assistantMessage(issuedCall("read-1", tasks.ToolReadFile, `{"path":"input.txt"}`)),
					toolMessage("read-1", tasks.EditFile.Seed["input.txt"]),
				),
			},
		}
	}
	if verdict := verdictFor(t, input(tasks.EditFile.Query)); !verdict.Success {
		t.Fatalf("verdict failed with the frozen query: %v", verdict.Failures)
	}
	// Surrounding whitespace is not a different prompt.
	if verdict := verdictFor(t, input("  "+tasks.EditFile.Query+"\n")); !verdict.Success {
		t.Fatalf("verdict failed with a whitespace-padded frozen query: %v", verdict.Failures)
	}
	verdict := verdictFor(t, input("Edit it."))
	failedWith(t, verdict, "frozen user message")
	if verdict.Success {
		t.Fatalf("a runner that invented its own shorter prompt was accepted")
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// The three tools are named identically in every runner, per the contract:
// read_file(path), write_file(path, content), run_shell(command).
//
// They are declared with Eino's schema-inferring helper
// (components/tool/utils.InferTool), which reflects the struct tags below into
// the JSON Schema Eino sends as the tool definition.

// Argument schemas use Eino's documented tag split: `jsonschema:"required"`
// marks the field mandatory and `jsonschema_description:"..."` carries the
// description. A description embedded in the jsonschema tag is cut at its first
// comma (components/tool/utils/doc.go warns about exactly this), which silently
// truncated the first version of these three tools.
type readFileArgs struct {
	Path string `json:"path" jsonschema:"required" jsonschema_description:"Path of the file to read, relative to the workspace root."`
}

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"Path of the file to write, relative to the workspace root."`
	Content string `json:"content" jsonschema:"required" jsonschema_description:"Exact content to write to the file."`
}

type runShellArgs struct {
	Command string `json:"command" jsonschema:"required" jsonschema_description:"Shell command to execute in the workspace root. Requires human approval before it runs."`
}

// shellInterruptState is the tool state Eino persists inside the durable
// checkpoint when run_shell interrupts for approval. It is restored by
// tool.GetInterruptState on the same process (in-process resume) and on a
// second, fresh process (cross-process resume).
//
// Eino checkpoints are gob-encoded, and a value behind an `any` field must be
// registered before it can be encoded; schema.RegisterName performs both the
// gob registration and Eino's internal serializer registration.
type shellInterruptState struct {
	Command   string `json:"command"`
	RequestNo int    `json:"request_no"`
}

func init() {
	schema.RegisterName[shellInterruptState]("zf_bench_eino_shell_interrupt_state")
}

// approvalRequired is the user-facing interrupt payload. Eino documents that
// interrupt Info is not persisted, so it only has to be printable.
func approvalRequired(command string) string {
	return fmt.Sprintf("run_shell requires approval before executing: %s", oneLine(command))
}

// newTools builds the fixed three-tool set.
func newTools(ws *workspace, approval string) ([]tool.BaseTool, error) {
	readFile, err := utils.InferTool("read_file",
		"Read a UTF-8 text file inside the workspace and return its contents.",
		func(_ context.Context, in readFileArgs) (string, error) {
			return ws.ReadFile(in.Path)
		})
	if err != nil {
		return nil, err
	}

	writeFile, err := utils.InferTool("write_file",
		"Write exact content to a file inside the workspace, creating it if needed.",
		func(_ context.Context, in writeFileArgs) (string, error) {
			if err := ws.WriteFile(in.Path, in.Content); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
		})
	if err != nil {
		return nil, err
	}

	runShell, err := utils.InferTool("run_shell",
		"Run a shell command with the workspace as its working directory and return its combined output. "+
			"The command only runs after it has been approved; otherwise it is refused and nothing is executed.",
		func(ctx context.Context, in runShellArgs) (string, error) {
			return runShellWithApproval(ctx, ws, in.Command, approval)
		})
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{readFile, writeFile, runShell}, nil
}

// runShellWithApproval is the approval path of run_shell.
//
// First execution (the tool was not part of a previous interrupt): it does not
// run the command. It returns Eino's tool.StatefulInterrupt, which makes the
// surrounding ToolsNode emit a CompositeInterrupt; the ADK runner then persists
// a checkpoint under BENCH_STATE_DIR and surfaces an interrupt event.
//
// Resumed execution: Eino replays this tool call with the same arguments and
// the persisted state available through tool.GetInterruptState. The answer
// comes from the resume data the driver targeted at this interrupt id
// (BENCH_APPROVAL), falling back to the process's own BENCH_APPROVAL. Only
// "approve" reaches the subprocess.
//
// A tool that was interrupted but is NOT the explicit resume target re-issues
// its interrupt, which is the strategy Eino documents for leaf components.
func runShellWithApproval(ctx context.Context, ws *workspace, command, approval string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("run_shell: command is required")
	}

	wasInterrupted, hasState, state := tool.GetInterruptState[shellInterruptState](ctx)
	isResumeTarget, hasResumeData, resumeData := tool.GetResumeContext[string](ctx)

	if wasInterrupted && !isResumeTarget {
		// Eino's documented "Explicit Targeted Resume" contract: a leaf that was
		// interrupted and is not the target must re-interrupt to keep its state.
		return "", tool.StatefulInterrupt(ctx, approvalRequired(command), stateOr(state, hasState, command))
	}

	if !wasInterrupted {
		return "", tool.StatefulInterrupt(ctx, approvalRequired(command), shellInterruptState{Command: command})
	}

	decision := approval
	if hasResumeData && resumeData != "" {
		decision = resumeData
	}

	switch decision {
	case ApprovalApprove:
		return execShell(ctx, ws, command)
	case ApprovalReject:
		return fmt.Sprintf("approval denied: run_shell did not execute %q", command), nil
	default:
		return "", fmt.Errorf("run_shell: unknown approval decision %q", decision)
	}
}

func stateOr(state shellInterruptState, hasState bool, fallback string) shellInterruptState {
	if hasState && state.Command != "" {
		return state
	}
	return shellInterruptState{Command: fallback}
}

// execShell runs the approved command as a real subprocess with the workspace
// as its working directory, and returns its combined stdout+stderr verbatim.
//
// The result is NOT decorated. An earlier version appended "[exit status N]" so
// that a command printing nothing would still satisfy the task verifier's
// non-empty-tool-result check; that was a workaround for a verifier artefact and
// it made Eino's tool result incomparable with the other frameworks'. The frozen
// commands were changed at the source to produce stdout, so the tool result is
// now exactly what the command printed.
//
// A non-zero exit is reported on stderr and is still returned as a tool result
// rather than an orchestration error, because a failing command is information
// for the model, not a reason to abort the run.
func execShell(ctx context.Context, ws *workspace, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = ws.Root()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintf(os.Stderr, "eino-runner: run_shell exited %d: %s\n", exitErr.ExitCode(), oneLine(command))
			return out, nil
		}
		return "", fmt.Errorf("run_shell: %w", err)
	}
	return out, nil
}

package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tool/jsonschema"
)

type Config struct {
	Policy policy.ShellPolicy
}

func New(config Config) (tool.Tool, error) {
	return shellTool{config: config, schema: jsonschema.Infer(input{})}, nil
}

func Must(config Config) tool.Tool {
	tool, err := New(config)
	if err != nil {
		panic(err)
	}
	return tool
}

type input struct {
	Command     string `json:"command" jsonschema:"required,description=Shell command to run"`
	CWD         string `json:"cwd,omitempty" jsonschema:"description=Workspace-relative working directory"`
	TimeoutMs   int64  `json:"timeoutMs,omitempty" jsonschema:"description=Timeout in milliseconds"`
	Description string `json:"description" jsonschema:"required,description=Why this command is needed"`
}

type output struct {
	Command   string               `json:"command"`
	CWD       string               `json:"cwd"`
	Output    string               `json:"output"`
	ExitCode  int                  `json:"exitCode"`
	TimedOut  bool                 `json:"timedOut"`
	Truncated bool                 `json:"truncated"`
	Review    policy.CommandReview `json:"review"`
}

type shellTool struct {
	config Config
	schema map[string]any
}

func (t shellTool) Name() string {
	return "shell"
}

func (t shellTool) Description() string {
	return "Run an allowlisted shell command in the configured workspace."
}

func (t shellTool) Schema() map[string]any {
	return t.schema
}

func (t shellTool) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	var in input
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 {
		decoder = json.NewDecoder(strings.NewReader(`{}`))
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&in); err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	out, err := t.run(ctx, in, call)
	if err != nil {
		if errors.Is(err, approval.ErrRequired) {
			return out, err
		}
		if out.Output != "" || len(out.Structured) > 0 {
			out.Error = err.Error()
			if out.ExitCode == 0 {
				out.ExitCode = 1
			}
			return out, err
		}
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	return out, nil
}

func (t shellTool) run(ctx context.Context, in input, call tool.Context) (tool.Result, error) {
	if strings.TrimSpace(in.Description) == "" {
		return tool.Result{}, fmt.Errorf("%w: description is required", tool.ErrInvalidArguments)
	}
	review := policy.ReviewCommand(t.config.Policy, in.Command)
	if review.Decision == policy.ReviewBlock {
		return tool.Result{}, fmt.Errorf("command blocked: %s", review.Reason)
	}
	cwd, err := policy.ResolveWorkingDir(t.config.Policy.WorkingDir, in.CWD)
	if err != nil {
		return tool.Result{}, err
	}
	if review.Decision == policy.ReviewRequireApproval {
		req := approval.Request{
			ID:          approval.NewRequestID(call.RunID, call.ToolCallID, "shell_command"),
			RunID:       call.RunID,
			ToolCallID:  call.ToolCallID,
			ToolName:    "shell",
			Operation:   "shell.command",
			Title:       "Approve shell command",
			Description: in.Description,
			Risk:        approval.RiskHigh,
			Options:     approval.DefaultOptions(),
			Payload: map[string]any{
				"command": in.Command,
				"cwd":     cwd,
				"review":  review,
			},
			CreatedAt: time.Now().UTC(),
		}
		return approval.RequiredResult(req), approval.ErrRequired
	}

	timeout := time.Duration(in.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = t.config.Policy.MaxTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if t.config.Policy.MaxTimeout > 0 && timeout > t.config.Policy.MaxTimeout {
		timeout = t.config.Policy.MaxTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, exitCode, err := run(runCtx, cwd, t.config.Policy, in.Command)
	combined := joinOutput(stdout, stderr)
	truncated := false
	maxOutput := t.config.Policy.MaxOutputBytes
	if maxOutput > 0 && int64(len(combined)) > maxOutput {
		combined = combined[:maxOutput]
		truncated = true
	}
	out := output{
		Command:   in.Command,
		CWD:       cwd,
		Output:    combined,
		ExitCode:  exitCode,
		Truncated: truncated,
		Review:    review,
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		out.ExitCode = 124
		out.TimedOut = true
		return encodeOutput(out, tool.ErrTimeout)
	}
	return encodeOutput(out, err)
}

func encodeOutput(out output, err error) (tool.Result, error) {
	data, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		return tool.Result{}, marshalErr
	}
	var structured map[string]any
	if unmarshalErr := json.Unmarshal(data, &structured); unmarshalErr != nil {
		return tool.Result{Output: string(data), ExitCode: out.ExitCode}, err
	}
	return tool.Result{Output: string(data), Structured: structured, ExitCode: out.ExitCode}, err
}

func run(ctx context.Context, cwd string, shellPolicy policy.ShellPolicy, command string) (string, string, int, error) {
	name, args := shellCommand(command)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Env = append(cmd.Environ(), policy.AllowedEnv(shellPolicy)...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), exitCode, err
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

func joinOutput(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + stderr
	}
}

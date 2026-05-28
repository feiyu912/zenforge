package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

const approvalRequired = "approval_required"

type Config struct {
	Policy policy.ShellPolicy
}

func New(config Config) (tool.Tool, error) {
	return tools.New("shell", "Run an allowlisted shell command in the configured workspace.", func(ctx context.Context, in input) (output, error) {
		if strings.TrimSpace(in.Description) == "" {
			return output{}, fmt.Errorf("%w: description is required", tool.ErrInvalidArguments)
		}
		review := policy.ReviewCommand(config.Policy, in.Command)
		if review.Decision == policy.ReviewBlock {
			return output{}, fmt.Errorf("command blocked: %s", review.Reason)
		}
		if review.Decision == policy.ReviewRequireApproval {
			return output{}, approvalError{review: review}
		}

		timeout := time.Duration(in.TimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = config.Policy.MaxTimeout
		}
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		if config.Policy.MaxTimeout > 0 && timeout > config.Policy.MaxTimeout {
			timeout = config.Policy.MaxTimeout
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		cwd, err := policy.ResolveWorkingDir(config.Policy.WorkingDir, in.CWD)
		if err != nil {
			return output{}, err
		}
		stdout, stderr, exitCode, err := run(runCtx, cwd, config.Policy, in.Command)
		combined := joinOutput(stdout, stderr)
		truncated := false
		maxOutput := config.Policy.MaxOutputBytes
		if maxOutput > 0 && int64(len(combined)) > maxOutput {
			combined = combined[:maxOutput]
			truncated = true
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return output{
				Command:   in.Command,
				CWD:       cwd,
				Output:    combined,
				ExitCode:  124,
				TimedOut:  true,
				Truncated: truncated,
				Review:    review,
			}, tool.ErrTimeout
		}
		return output{
			Command:   in.Command,
			CWD:       cwd,
			Output:    combined,
			ExitCode:  exitCode,
			Truncated: truncated,
			Review:    review,
		}, err
	})
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

type approvalError struct {
	review policy.CommandReview
}

func (e approvalError) Error() string {
	return approvalRequired + ": " + e.review.Reason
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

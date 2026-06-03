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
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tool/jsonschema"
)

type ShellBackend string

const (
	ShellBackendLocal   ShellBackend = "local"
	ShellBackendSandbox ShellBackend = "sandbox"
)

type Config struct {
	Policy          policy.ShellPolicy
	Backend         ShellBackend
	Sandbox         sandbox.Sandbox
	EnvironmentID   string
	Mounts          []sandbox.Mount
	KeepSessionOpen bool
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
	Command      string               `json:"command"`
	CWD          string               `json:"cwd"`
	Output       string               `json:"output"`
	Backend      ShellBackend         `json:"backend"`
	ExitCode     int                  `json:"exitCode"`
	TimedOut     bool                 `json:"timedOut"`
	Truncated    bool                 `json:"truncated"`
	Review       policy.CommandReview `json:"review"`
	Sandbox      *sandbox.State       `json:"sandbox,omitempty"`
	SandboxError string               `json:"sandboxError,omitempty"`
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
		if approvedByMetadata(call.Metadata, review) {
			review.Decision = policy.ReviewAllow
			review.Reason = "command approved by broker"
		} else {
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
					"command":     in.Command,
					"cwd":         cwd,
					"fingerprint": review.Fingerprint,
					"ruleKey":     review.RuleKey,
					"review":      review,
				},
				CreatedAt: time.Now().UTC(),
			}
			return approval.RequiredResult(req), approval.ErrRequired
		}
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
	stdout, stderr, exitCode, backend, sandboxState, err := t.execute(ctx, call, in.Command, cwd, timeout)
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
		Backend:   backend,
		ExitCode:  exitCode,
		Truncated: truncated,
		Review:    review,
		Sandbox:   sandboxState,
	}
	if code := sandbox.Code(err); code != "" {
		out.SandboxError = string(code)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sandbox.ErrTimeout) {
		out.ExitCode = 124
		out.TimedOut = true
		if out.SandboxError == "" && errors.Is(err, sandbox.ErrTimeout) {
			out.SandboxError = string(sandbox.ErrTimeout)
		}
		return encodeOutput(out, tool.ErrTimeout)
	}
	return encodeOutput(out, err)
}

func (t shellTool) execute(ctx context.Context, call tool.Context, command, cwd string, timeout time.Duration) (string, string, int, ShellBackend, *sandbox.State, error) {
	backend := t.config.Backend
	if backend == "" {
		backend = ShellBackendLocal
	}
	switch backend {
	case ShellBackendLocal:
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		stdout, stderr, exitCode, err := run(runCtx, cwd, t.config.Policy, command)
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		return stdout, stderr, exitCode, backend, nil, err
	case ShellBackendSandbox:
		return t.executeSandbox(ctx, call, command, cwd, timeout)
	default:
		return "", "", 1, backend, nil, fmt.Errorf("unknown shell backend: %s", backend)
	}
}

func (t shellTool) executeSandbox(ctx context.Context, call tool.Context, command, cwd string, timeout time.Duration) (string, string, int, ShellBackend, *sandbox.State, error) {
	if t.config.Sandbox == nil {
		return "", "", 1, ShellBackendSandbox, nil, sandbox.ErrSandboxUnavailable
	}
	subtaskID := stringFromMetadata(call.Metadata, "subtaskId")
	session, restored := restoredSandboxSession(call.Metadata, call.RunID, subtaskID)
	if session == nil {
		var err error
		session, err = t.config.Sandbox.Open(ctx, sandbox.OpenRequest{
			RunID:         call.RunID,
			SubtaskID:     subtaskID,
			EnvironmentID: t.config.EnvironmentID,
			WorkingDir:    t.config.Policy.WorkingDir,
			Env:           allowedEnvMap(t.config.Policy),
			Mounts:        append([]sandbox.Mount(nil), t.config.Mounts...),
			Metadata: map[string]any{
				"toolCallId": call.ToolCallID,
			},
		})
		if err != nil {
			return "", "", 1, ShellBackendSandbox, nil, err
		}
	}
	stateValue := sandbox.StateFromSession(session)
	state := &stateValue
	result, err := t.config.Sandbox.Execute(ctx, session, sandbox.ExecuteRequest{
		Command: command,
		CWD:     cwd,
		Timeout: timeout,
		Env:     allowedEnvMap(t.config.Policy),
		Metadata: map[string]any{
			"toolCallId": call.ToolCallID,
		},
	})
	if !t.config.KeepSessionOpen {
		closeErr := t.config.Sandbox.Close(ctx, session)
		if closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if restored && sandbox.Code(err) == sandbox.ErrClosed {
		return t.executeSandboxWithoutRestore(ctx, call, command, cwd, timeout)
	}
	return result.Stdout, result.Stderr, result.ExitCode, ShellBackendSandbox, state, err
}

func (t shellTool) executeSandboxWithoutRestore(ctx context.Context, call tool.Context, command, cwd string, timeout time.Duration) (string, string, int, ShellBackend, *sandbox.State, error) {
	metadata := cloneAnyMap(call.Metadata)
	delete(metadata, sandbox.MetadataStateKey)
	call.Metadata = metadata
	return t.executeSandbox(ctx, call, command, cwd, timeout)
}

func restoredSandboxSession(metadata map[string]any, runID, subtaskID string) (*sandbox.Session, bool) {
	state, ok := sandbox.StateFromMetadata(metadata)
	if !ok {
		return nil, false
	}
	session := sandbox.SessionFromState(state, runID, subtaskID)
	if session == nil {
		return nil, false
	}
	return session, true
}
func approvedByMetadata(metadata map[string]any, review policy.CommandReview) bool {
	if metadata == nil || !approval.IsApprovedAction(metadata[approval.MetadataDecisionAction]) {
		return false
	}
	if fingerprint, _ := metadata[approval.MetadataFingerprint].(string); fingerprint != "" && fingerprint == review.Fingerprint {
		return true
	}
	if ruleKey, _ := metadata[approval.MetadataRuleKey].(string); ruleKey != "" && ruleKey == review.RuleKey {
		return true
	}
	return false
}

func encodeOutput(out output, err error) (tool.Result, error) {
	data, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		return tool.Result{}, marshalErr
	}
	metadata := map[string]any{}
	if out.Sandbox != nil {
		metadata[sandbox.MetadataStateKey] = *out.Sandbox
	}
	var structured map[string]any
	if unmarshalErr := json.Unmarshal(data, &structured); unmarshalErr != nil {
		return tool.Result{Output: string(data), ExitCode: out.ExitCode, Metadata: metadata}, err
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return tool.Result{Output: string(data), Structured: structured, ExitCode: out.ExitCode, Metadata: metadata}, err
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

func allowedEnvMap(shellPolicy policy.ShellPolicy) map[string]string {
	allowed := policy.AllowedEnv(shellPolicy)
	if len(allowed) == 0 {
		return nil
	}
	env := make(map[string]string, len(allowed))
	for _, item := range allowed {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func stringFromMetadata(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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

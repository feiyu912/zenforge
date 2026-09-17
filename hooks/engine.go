package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Runner executes one hook command. It is injectable so the contract can be
// tested without spawning processes.
type Runner interface {
	// Run executes command with payload on stdin and returns stdout,
	// stderr, and the process error (nil on exit 0).
	Run(ctx context.Context, command string, payload []byte, cwd string, env []string) (string, string, error)
}

// Engine dispatches hook commands.
type Engine struct {
	config         Config
	runner         Runner
	cwd            string
	env            []string
	timeout        time.Duration
	maxOutputBytes int
	shell          string

	mu       sync.Mutex
	compiled map[string]*regexp.Regexp
}

// Config for the engine itself (not to be confused with Config, the hook
// set). Named Options to keep the two readable.
type Options struct {
	// Hooks is the configured set.
	Hooks Config
	// Runner replaces real process execution.
	Runner Runner
	// CWD is the working directory hooks run in.
	CWD string
	// Env is the environment hooks receive; empty inherits the engine's.
	Env []string
	// DefaultTimeout bounds a hook whose entry sets none.
	DefaultTimeout time.Duration
	// MaxOutputBytes caps one hook's captured output.
	MaxOutputBytes int
	// Shell runs the command line, "/bin/sh" by default.
	Shell string
}

// New returns an engine, validating the hook set.
func New(options Options) (*Engine, error) {
	if options.Hooks != nil {
		if err := options.Hooks.Validate(); err != nil {
			return nil, err
		}
	}
	timeout := options.DefaultTimeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOutput := options.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutputBytes
	}
	shell := strings.TrimSpace(options.Shell)
	if shell == "" {
		shell = "/bin/sh"
	}
	runner := options.Runner
	if runner == nil {
		runner = &execRunner{shell: shell}
	}
	return &Engine{
		config:         options.Hooks,
		runner:         runner,
		cwd:            options.CWD,
		env:            append([]string(nil), options.Env...),
		timeout:        timeout,
		maxOutputBytes: maxOutput,
		shell:          shell,
		compiled:       map[string]*regexp.Regexp{},
	}, nil
}

// Configured reports whether any hook is registered for an event. A nil
// engine has no hooks, so callers never need a nil check.
func (e *Engine) Configured(event Event) bool {
	if e == nil {
		return false
	}
	return len(e.config[event]) > 0
}

// Run dispatches every matching hook for the event and aggregates the
// decisions. A nil engine and an event with no hooks both return a
// zero-value outcome, so callers never need a nil check.
func (e *Engine) Run(ctx context.Context, request Request) (Outcome, error) {
	outcome := Outcome{Event: request.Event, Continue: true}
	if e == nil {
		return outcome, nil
	}
	commands := e.config[request.Event]
	if len(commands) == 0 {
		return outcome, nil
	}
	payload, err := e.payload(request)
	if err != nil {
		return outcome, err
	}
	for _, command := range commands {
		matched, err := e.match(request.Event, command, request.ToolName)
		if err != nil {
			return outcome, err
		}
		if !matched {
			outcome.Entries = append(outcome.Entries, Entry{
				Command: command.Command,
				Matcher: command.Matcher,
				Status:  StatusSkipped,
			})
			continue
		}
		entry, decision := e.runOne(ctx, request, command, payload)
		outcome.Entries = append(outcome.Entries, entry)
		e.merge(&outcome, entry, decision, command)
	}
	return outcome, nil
}

// match reports whether a hook applies to this dispatch.
func (e *Engine) match(event Event, command Command, toolName string) (bool, error) {
	if command.Matcher == "" || !hasMatcher(event) {
		return true, nil
	}
	pattern, err := e.matcher(command.Matcher)
	if err != nil {
		return false, err
	}
	return pattern.MatchString(toolName), nil
}

// matcher compiles and caches a matcher.
func (e *Engine) matcher(expression string) (*regexp.Regexp, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if compiled, ok := e.compiled[expression]; ok {
		return compiled, nil
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("invalid hook matcher %q: %w", expression, err)
	}
	e.compiled[expression] = compiled
	return compiled, nil
}

// payload is the JSON document the hook receives on stdin.
func (e *Engine) payload(request Request) ([]byte, error) {
	document := map[string]any{
		"hookEventName":   string(request.Event),
		"hook_event_name": string(request.Event),
		"sessionId":       request.SessionID,
		"session_id":      request.SessionID,
		"runId":           request.RunID,
		"cwd":             e.cwd,
		"event":           string(request.Event),
	}
	if request.ToolName != "" {
		document["toolName"] = request.ToolName
		document["tool_name"] = request.ToolName
	}
	if request.ToolInput != nil {
		document["toolInput"] = request.ToolInput
		document["tool_input"] = request.ToolInput
	}
	if request.ToolOutput != "" {
		document["toolOutput"] = request.ToolOutput
		document["tool_output"] = request.ToolOutput
	}
	if request.ToolError != "" {
		document["toolError"] = request.ToolError
		document["tool_error"] = request.ToolError
	}
	if request.Prompt != "" {
		document["prompt"] = request.Prompt
	}
	if request.StopReason != "" {
		document["stopReason"] = request.StopReason
		document["stop_reason"] = request.StopReason
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode hook payload: %w", err)
	}
	return encoded, nil
}

// runOne executes one hook and interprets its result.
func (e *Engine) runOne(ctx context.Context, request Request, command Command, payload []byte) (Entry, decision) {
	timeout := command.Timeout
	if timeout <= 0 {
		timeout = e.timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := nowFunc()
	stdout, stderr, err := e.runner.Run(runCtx, command.Command, payload, e.cwd, e.env)
	entry := Entry{
		Command:  command.Command,
		Matcher:  command.Matcher,
		Stderr:   truncate(stderr, e.maxOutputBytes),
		Duration: nowFunc().Sub(started),
	}
	if runCtx.Err() == context.DeadlineExceeded {
		entry.Status = StatusFailed
		entry.TimedOut = true
		entry.Reason = fmt.Sprintf("hook exceeded its %s timeout", timeout)
		return entry, decision{}
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		entry.ExitCode = intPtr(0)
	case errors.As(err, &exitErr):
		code := exitErr.ExitCode()
		entry.ExitCode = &code
	default:
		entry.Status = StatusFailed
		entry.Reason = fmt.Sprintf("hook could not run: %v", err)
		return entry, decision{}
	}

	switch {
	case entry.ExitCode != nil && *entry.ExitCode == 0:
		parsed, parseErr := parseDecision(stdout)
		if parseErr != nil {
			entry.Status = StatusFailed
			entry.Reason = parseErr.Error()
			return entry, decision{}
		}
		entry.Status = StatusSuccess
		entry.Feedback = truncate(strings.TrimSpace(stdout), e.maxOutputBytes)
		if parsed.Block {
			entry.Status = StatusBlocked
			entry.Reason = parsed.Reason
		}
		return entry, parsed
	case entry.ExitCode != nil && *entry.ExitCode == 2:
		// The reference's blocking convention: exit 2 blocks, and stderr is
		// the reason. A bare exit 2 with no reason is a failed hook, not a
		// silent block, because an unexplained refusal cannot be acted on.
		reason := strings.TrimSpace(stderr)
		if reason == "" {
			entry.Status = StatusFailed
			entry.Reason = fmt.Sprintf("%s hook exited with code 2 but wrote no reason to stderr", request.Event)
			return entry, decision{}
		}
		entry.Status = StatusBlocked
		entry.Reason = reason
		return entry, decision{Block: true, Reason: reason}
	default:
		code := 0
		if entry.ExitCode != nil {
			code = *entry.ExitCode
		}
		entry.Status = StatusFailed
		entry.Reason = fmt.Sprintf("hook exited with code %d", code)
		return entry, decision{}
	}
}

// decision is the parsed hook decision.
type decision struct {
	Block             bool
	Reason            string
	Continue          *bool
	StopReason        string
	SystemMessage     string
	SuppressOutput    bool
	AdditionalContext string
	UpdatedInput      map[string]any
}

// wireDecision is the JSON document a hook may print on stdout.
type wireDecision struct {
	Continue          *bool          `json:"continue,omitempty"`
	StopReason        string         `json:"stopReason,omitempty"`
	SystemMessage     string         `json:"systemMessage,omitempty"`
	SuppressOutput    bool           `json:"suppressOutput,omitempty"`
	Decision          string         `json:"decision,omitempty"`
	Reason            string         `json:"reason,omitempty"`
	AdditionalContext string         `json:"additionalContext,omitempty"`
	UpdatedInput      map[string]any `json:"updatedInput,omitempty"`
}

// parseDecision interprets a hook's stdout. Empty output is a valid
// "no opinion" decision; output that is not JSON is an error rather than a
// silently ignored opinion, because a hook that meant to block must not be
// ignored for a formatting mistake.
func parseDecision(stdout string) (decision, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return decision{}, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return decision{}, fmt.Errorf("hook wrote non-JSON output: %s", truncate(trimmed, 200))
	}
	var wire wireDecision
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return decision{}, fmt.Errorf("hook wrote invalid JSON output: %v", err)
	}
	parsed := decision{
		Reason:            wire.Reason,
		Continue:          wire.Continue,
		StopReason:        wire.StopReason,
		SystemMessage:     wire.SystemMessage,
		SuppressOutput:    wire.SuppressOutput,
		AdditionalContext: wire.AdditionalContext,
		UpdatedInput:      wire.UpdatedInput,
	}
	switch strings.ToLower(strings.TrimSpace(wire.Decision)) {
	case "", "allow":
	case "block", "deny":
		parsed.Block = true
		if strings.TrimSpace(parsed.Reason) == "" {
			return decision{}, fmt.Errorf("hook returned a block decision without a reason")
		}
	default:
		return decision{}, fmt.Errorf("hook returned unknown decision %q", wire.Decision)
	}
	return parsed, nil
}

// merge folds one hook's decision into the aggregate.
func (e *Engine) merge(outcome *Outcome, entry Entry, parsed decision, command Command) {
	if entry.Status == StatusBlocked {
		outcome.Blocked = true
		if outcome.Reason == "" {
			outcome.Reason = entry.Reason
		}
		return
	}
	if entry.Status == StatusFailed {
		if command.FailClosed {
			// Opt-in fail-closed: a hook that cannot run refuses the action
			// rather than quietly allowing it.
			outcome.Blocked = true
			if outcome.Reason == "" {
				outcome.Reason = fmt.Sprintf("%s hook failed: %s", outcome.Event, entry.Reason)
			}
		}
		return
	}
	if parsed.Continue != nil && !*parsed.Continue {
		outcome.Continue = false
		if outcome.StopReason == "" {
			outcome.StopReason = parsed.StopReason
		}
	}
	if parsed.SystemMessage != "" {
		outcome.SystemMessage = parsed.SystemMessage
	}
	if parsed.SuppressOutput {
		outcome.SuppressOutput = true
	}
	if parsed.AdditionalContext != "" {
		if outcome.AdditionalContext != "" {
			outcome.AdditionalContext += "\n"
		}
		outcome.AdditionalContext += parsed.AdditionalContext
	}
	if len(parsed.UpdatedInput) > 0 {
		outcome.UpdatedInput = parsed.UpdatedInput
	}
}

// execRunner runs real hook processes.
type execRunner struct {
	shell string
}

func (r *execRunner) Run(ctx context.Context, command string, payload []byte, cwd string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, r.shell, "-c", command)
	cmd.Dir = cwd
	if len(env) > 0 {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// limitedBuffer bounds what a hook may write into memory.
type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

// Global cap for hook output, independent of the engine's setting, so a
// runaway hook cannot exhaust memory before the engine truncates.
const hardOutputLimit = 1 << 20

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.limit = hardOutputLimit
	}
	if b.buffer.Len() < b.limit {
		remaining := b.limit - b.buffer.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

// truncate bounds a string and marks the truncation.
func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[truncated]"
}

func intPtr(value int) *int { return &value }

// nowFunc is replaceable in tests.
var nowFunc = time.Now

var _ io.Writer = (*limitedBuffer)(nil)

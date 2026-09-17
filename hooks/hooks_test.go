package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/tool"
)

// scriptedRunner runs a scripted response per hook command.
type scriptedRunner struct {
	responses map[string]scriptedResponse
	calls     []string
	payloads  [][]byte
}

type scriptedResponse struct {
	stdout string
	stderr string
	err    error
	delay  time.Duration
}

func (r *scriptedRunner) Run(ctx context.Context, command string, payload []byte, cwd string, env []string) (string, string, error) {
	r.calls = append(r.calls, command)
	r.payloads = append(r.payloads, payload)
	response, ok := r.responses[command]
	if !ok {
		return "", "", nil
	}
	if response.delay > 0 {
		select {
		case <-time.After(response.delay):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return response.stdout, response.stderr, response.err
}

// helperExitError produces a genuine *exec.ExitError, which is what the
// engine inspects to learn a hook's exit code.
func helperExitError(code int) (error, error) {
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil, fmt.Errorf("expected an exec.ExitError, got %v", err)
	}
	return exitErr, nil
}

// exitStatus returns a runner whose command exits with the given code.
func exitStatus(code int) *scriptedRunner {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{}}
	exitErr, _ := helperExitError(code)
	runner.responses[fmt.Sprintf("exit %d", code)] = scriptedResponse{err: exitErr}
	return runner
}

func newTestEngine(t *testing.T, config Config, runner Runner) *Engine {
	t.Helper()
	engine, err := New(Options{Hooks: config, Runner: runner, CWD: t.TempDir(), DefaultTimeout: time.Second})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return engine
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		config Config
	}{
		{name: "unknown event", config: Config{"NotAnEvent": {{Command: "true"}}}},
		{name: "no commands", config: Config{EventPreToolUse: {}}},
		{name: "empty command", config: Config{EventPreToolUse: {{Command: "  "}}}},
		{name: "matcher on a non-tool event", config: Config{EventStop: {{Command: "true", Matcher: "shell"}}}},
		{name: "negative timeout", config: Config{EventStop: {{Command: "true", Timeout: -time.Second}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.config.Validate(); err == nil {
				t.Fatalf("configuration %#v was accepted", testCase.config)
			}
		})
	}
	if err := (Config{EventPreToolUse: {{Command: "true", Matcher: "shell|bash"}}}).Validate(); err != nil {
		t.Fatalf("a valid configuration was rejected: %v", err)
	}
	if _, err := New(Options{Hooks: Config{"Nope": {{Command: "true"}}}}); err == nil {
		t.Fatal("New accepted an invalid hook set")
	}
}

func TestEventParsingAcceptsTheReferenceCasing(t *testing.T) {
	for _, name := range []string{"PreToolUse", "pretooluse", "pre_tool_use"} {
		event, err := ParseEvent(name)
		if err != nil {
			t.Fatalf("ParseEvent(%q) returned error: %v", name, err)
		}
		if event != EventPreToolUse {
			t.Fatalf("ParseEvent(%q) = %s", name, event)
		}
	}
	if _, err := ParseEvent("BeforeTool"); err == nil {
		t.Fatal("an unknown event was accepted")
	}
}

func TestMatcherSelectsHooks(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"echo shell-hook": {stdout: "shell"},
		"echo all-hook":   {stdout: "all"},
	}}
	engine := newTestEngine(t, Config{EventPreToolUse: {
		{Matcher: "^shell$", Command: "echo shell-hook"},
		{Command: "echo all-hook"},
	}}, runner)
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(outcome.Entries) != 2 || len(runner.calls) != 2 {
		t.Fatalf("outcome = %#v calls = %v", outcome.Entries, runner.calls)
	}
	// A non-matching hook is recorded as skipped and never runs.
	runner.calls = nil
	outcome, err = engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "apply_patch"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if outcome.Entries[0].Status != StatusSkipped || len(runner.calls) != 1 {
		t.Fatalf("outcome = %#v calls = %v", outcome.Entries, runner.calls)
	}
	// An invalid matcher is an error, not a silent skip: a hook the user
	// meant to run must not disappear.
	bad := newTestEngine(t, Config{EventPreToolUse: {{Matcher: "([", Command: "true"}}}, &scriptedRunner{})
	if _, err := bad.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"}); err == nil {
		t.Fatal("an invalid matcher was accepted")
	}
}

func TestExitZeroJSONDecisions(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"block":   {stdout: `{"decision":"block","reason":"no writes to /etc"}`},
		"deny":    {stdout: `{"decision":"deny","reason":"policy"}`},
		"allow":   {stdout: `{"decision":"allow"}`},
		"empty":   {},
		"context": {stdout: `{"additionalContext":"remember the ADR","systemMessage":"heads up","suppressOutput":true}`},
		"stop":    {stdout: `{"continue":false,"stopReason":"tests failed"}`},
		"rewrite": {stdout: `{"updatedInput":{"command":"ls","description":"rewritten"}}`},
	}}
	engine := newTestEngine(t, Config{EventPreToolUse: {
		{Command: "block"}, {Command: "deny"}, {Command: "allow"}, {Command: "empty"},
		{Command: "context"}, {Command: "stop"}, {Command: "rewrite"},
	}}, runner)
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !outcome.Blocked || outcome.Reason != "no writes to /etc" {
		t.Fatalf("outcome = %#v", outcome)
	}
	// A block with a reason is required; the reason of the first blocker is
	// the one reported, and Continue: false is honoured.
	if outcome.Continue {
		t.Fatalf("continue = true despite a stop decision: %#v", outcome)
	}
	if outcome.StopReason != "tests failed" {
		t.Fatalf("stop reason = %q", outcome.StopReason)
	}
	if outcome.AdditionalContext != "remember the ADR" || outcome.SystemMessage != "heads up" || !outcome.SuppressOutput {
		t.Fatalf("outcome = %#v", outcome)
	}
	if outcome.UpdatedInput["command"] != "ls" {
		t.Fatalf("updated input = %#v", outcome.UpdatedInput)
	}
	if outcome.Entries[2].Status != StatusSuccess || outcome.Entries[3].Status != StatusSuccess {
		t.Fatalf("entries = %#v", outcome.Entries)
	}
}

func TestBlockDecisionNeedsAReason(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"noreason":     {stdout: `{"decision":"block"}`},
		"unknown":      {stdout: `{"decision":"maybe"}`},
		"unknownfield": {stdout: `{"decision":"allow","somethingElse":true}`},
		"notjson":      {stdout: "I think this should be blocked"},
		"badjson":      {stdout: "{oops"},
	}}
	engine := newTestEngine(t, Config{EventPreToolUse: {
		{Command: "noreason"}, {Command: "unknown"}, {Command: "unknownfield"},
		{Command: "notjson"}, {Command: "badjson"},
	}}, runner)
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// Every one of these is a failed hook; none of them blocks, because a
	// malformed decision is not an opinion.
	if outcome.Blocked {
		t.Fatalf("a malformed decision blocked the call: %#v", outcome)
	}
	failed := outcome.Failed()
	if len(failed) != 5 {
		t.Fatalf("failed hooks = %#v", failed)
	}
	for _, entry := range failed {
		if entry.Reason == "" {
			t.Fatalf("a failure has no reason: %#v", entry)
		}
	}
}

func TestExitCodeTwoBlocksWithStderr(t *testing.T) {
	runner := exitStatus(2)
	runner.responses["exit 2"] = scriptedResponse{stderr: "denied by policy\n", err: runner.responses["exit 2"].err}
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "exit 2"}}}, runner)
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !outcome.Blocked || outcome.Reason != "denied by policy" {
		t.Fatalf("outcome = %#v", outcome)
	}
	// The same code with no stderr is a failure, not a silent block: an
	// unexplained refusal cannot be acted on.
	bare := exitStatus(2)
	bareEngine := newTestEngine(t, Config{EventPreToolUse: {{Command: "exit 2"}}}, bare)
	outcome, err = bareEngine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if outcome.Blocked {
		t.Fatalf("a bare exit 2 blocked the call: %#v", outcome)
	}
	if len(outcome.Failed()) != 1 {
		t.Fatalf("failed hooks = %#v", outcome.Entries)
	}
}

func TestOtherExitCodesFailAndFailClosedIsOptIn(t *testing.T) {
	runner := exitStatus(7)
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "exit 7"}}}, runner)
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if outcome.Blocked {
		t.Fatalf("the default failure policy blocked: %#v", outcome)
	}
	if len(outcome.Failed()) != 1 || !strings.Contains(outcome.Entries[0].Reason, "code 7") {
		t.Fatalf("outcome = %#v", outcome.Entries)
	}
	// Opt-in fail-closed turns the same failure into a refusal.
	closed := newTestEngine(t, Config{EventPreToolUse: {{Command: "exit 7", FailClosed: true}}}, runner)
	outcome, err = closed.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !outcome.Blocked || !strings.Contains(outcome.Reason, "hook failed") {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestTimeoutFailsAndClosesWhenAsked(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"slow": {delay: 200 * time.Millisecond, stdout: "{}"},
	}}
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "slow", Timeout: 20 * time.Millisecond}}}, runner)
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(outcome.Entries) != 1 || !outcome.Entries[0].TimedOut {
		t.Fatalf("outcome = %#v", outcome.Entries)
	}
	if !strings.Contains(outcome.Entries[0].Reason, "timeout") {
		t.Fatalf("reason = %q", outcome.Entries[0].Reason)
	}
	closed := newTestEngine(t, Config{EventPreToolUse: {{Command: "slow", Timeout: 20 * time.Millisecond, FailClosed: true}}}, runner)
	outcome, err = closed.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !outcome.Blocked {
		t.Fatalf("a fail-closed timeout did not block: %#v", outcome)
	}
}

func TestPayloadCarriesBothNamingStyles(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{"hook": {stdout: "{}"}}}
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "hook"}}}, runner)
	_, err := engine.Run(context.Background(), Request{
		Event: EventPreToolUse, SessionID: "run_1", RunID: "run_1",
		ToolName: "shell", ToolInput: map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(runner.payloads[0], &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	// Snake case and camel case are both present, so a hook written against
	// either convention works.
	if payload["hook_event_name"] != "PreToolUse" || payload["hookEventName"] != "PreToolUse" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["tool_name"] != "shell" || payload["tool_name"] != payload["toolName"] {
		t.Fatalf("payload = %#v", payload)
	}
	input, ok := payload["tool_input"].(map[string]any)
	if !ok || input["command"] != "ls" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestRealEngineRunsACommand(t *testing.T) {
	engine, err := New(Options{
		Hooks: Config{EventPreToolUse: {{Command: `printf '{"additionalContext":"real"}'`}}},
		CWD:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	outcome, err := engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell", ToolInput: map[string]any{"command": "ls"}})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if outcome.AdditionalContext != "real" || outcome.Blocked {
		t.Fatalf("outcome = %#v", outcome)
	}
	// The hook really receives the payload on stdin.
	engine, err = New(Options{
		Hooks: Config{EventPreToolUse: {{Command: `grep -q '"tool_name":"shell"'`}}},
		CWD:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	outcome, err = engine.Run(context.Background(), Request{Event: EventPreToolUse, ToolName: "shell"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if outcome.Entries[0].Status != StatusSuccess {
		t.Fatalf("the hook did not see the payload: %#v", outcome.Entries[0])
	}
}

// middlewareHarness wires the middleware around a stub tool.
type middlewareHarness struct {
	calls   int
	lastArg json.RawMessage
	result  tool.Result
	err     error
}

func (h *middlewareHarness) invoker() tool.Invoker {
	return tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		h.calls++
		h.lastArg = call.Arguments
		return h.result, h.err
	})
}

func TestMiddlewareBlocksBeforeTheToolRuns(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"block": {stdout: `{"decision":"block","reason":"no shell in this repo"}`},
	}}
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "block"}}}, runner)
	stub := &middlewareHarness{result: tool.Result{Output: "ran"}}
	invoker := Middleware(engine, "run_1")(stub.invoker())
	_, err := invoker.Invoke(context.Background(), tool.Call{Name: "shell", Arguments: json.RawMessage(`{"command":"ls"}`)})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v", err)
	}
	if stub.calls != 0 {
		t.Fatal("the tool ran despite a blocking hook")
	}
}

func TestMiddlewareRewritesArgumentsBeforeTheToolRuns(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"rewrite": {stdout: `{"updatedInput":{"command":"echo rewritten","description":"d"}}`},
	}}
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "rewrite"}}}, runner)
	stub := &middlewareHarness{result: tool.Result{Output: "ok"}}
	invoker := Middleware(engine, "run_1")(stub.invoker())
	if _, err := invoker.Invoke(context.Background(), tool.Call{Name: "shell", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)}); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !strings.Contains(string(stub.lastArg), "echo rewritten") {
		t.Fatalf("the tool received %s", stub.lastArg)
	}
}

func TestMiddlewareAnnotatesResultsAndBlocksPostHoc(t *testing.T) {
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		"context":   {stdout: `{"additionalContext":"consider the ADR"}`},
		"postblock": {stdout: `{"decision":"block","reason":"result not acceptable"}`},
	}}
	// Only a PreToolUse hook here: the same hook configured for both events
	// would run twice and legitimately accumulate its context twice.
	engine := newTestEngine(t, Config{EventPreToolUse: {{Command: "context"}}}, runner)
	stub := &middlewareHarness{result: tool.Result{Output: "ok"}}
	invoker := Middleware(engine, "run_1")(stub.invoker())
	result, err := invoker.Invoke(context.Background(), tool.Call{Name: "shell", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Metadata["hookContext"] != "consider the ADR" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	if result.Metadata["hookBlocked"] != false {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	// A PostToolUse block turns the call into a refusal after the tool ran.
	blocking := newTestEngine(t, Config{EventPostToolUse: {{Command: "postblock"}}}, runner)
	invoker = Middleware(blocking, "run_1")(stub.invoker())
	result, err = invoker.Invoke(context.Background(), tool.Call{Name: "shell", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v", err)
	}
	if result.Metadata["hookBlocked"] != true {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestMiddlewarePassesThroughWithoutHooks(t *testing.T) {
	stub := &middlewareHarness{result: tool.Result{Output: "ran"}}
	invoker := Middleware(nil, "run_1")(stub.invoker())
	result, err := invoker.Invoke(context.Background(), tool.Call{Name: "shell"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Output != "ran" || stub.calls != 1 {
		t.Fatalf("result = %#v calls = %d", result, stub.calls)
	}
	empty := newTestEngine(t, Config{}, &scriptedRunner{})
	invoker = Middleware(empty, "run_1")(stub.invoker())
	if _, err := invoker.Invoke(context.Background(), tool.Call{Name: "shell"}); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("calls = %d", stub.calls)
	}
}

func TestConfiguredReportsRegistration(t *testing.T) {
	engine := newTestEngine(t, Config{EventStop: {{Command: "true"}}}, &scriptedRunner{})
	if !engine.Configured(EventStop) || engine.Configured(EventPreToolUse) {
		t.Fatalf("Configured = %v/%v", engine.Configured(EventStop), engine.Configured(EventPreToolUse))
	}
	var nilEngine *Engine
	if nilEngine.Configured(EventStop) {
		t.Fatal("a nil engine reported a hook")
	}
	outcome, err := nilEngine.Run(context.Background(), Request{Event: EventStop})
	if err != nil || !outcome.Continue {
		t.Fatalf("nil engine outcome = %#v err = %v", outcome, err)
	}
}

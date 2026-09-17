package shell

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox"
	sandboxfake "github.com/feiyu912/zenforge/sandbox/fake"
	"github.com/feiyu912/zenforge/tool"
)

func TestShellAllowsAllowlistedCommand(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:      root,
		AllowCommands:   []string{"printf ok"},
		MaxTimeout:      time.Second,
		MaxOutputBytes:  1024,
		AllowedEnvKeys:  nil,
		RequireApproval: false,
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"test command"}`), tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Structured["output"] != "ok" {
		t.Fatalf("unexpected result: %#v", result.Structured)
	}
}

func TestShellBlocksDeniedAndNotAllowlistedCommands(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:    root,
		AllowCommands: []string{"printf ok"},
		DenyCommands:  []string{"rm"},
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"rm -rf tmp","description":"bad"}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected denied command error, got result=%#v err=%v", result, err)
	}
	result, err = shell.Call(context.Background(), json.RawMessage(`{"command":"git status","description":"not allowed"}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected not allowlisted command error, got result=%#v err=%v", result, err)
	}
}

func TestShellDoesNotAllowChainByFirstCommandPrefix(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:    root,
		AllowCommands: []string{"printf ok"},
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok && printf bad","description":"chained command"}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected shell control command error, got result=%#v err=%v", result, err)
	}
	if !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShellApprovalRequiredShape(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{WorkingDir: root, RequireApproval: true}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"git status","description":"needs approval"}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected approval error, got result=%#v err=%v", result, err)
	}
	if result.Error != approval.ErrorRequired {
		t.Fatalf("expected approval_required result, got %#v", result)
	}
	req, ok := approval.RequestFromResult(result)
	if !ok {
		t.Fatalf("expected structured approval request, got %#v", result.Structured)
	}
	if req.Operation != "shell.command" || req.ToolName != "shell" {
		t.Fatalf("unexpected approval request: %#v", req)
	}
}

func TestShellApprovalPlanFromReview(t *testing.T) {
	review := policy.ReviewCommand(policy.ShellPolicy{RequireApproval: true}, "git status")
	plan := shellApprovalPlan(tool.Context{RunID: "run_1", ToolCallID: "call_1"}, input{
		Command:     "git status",
		Description: "inspect repo",
	}, "/workspace", review)
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan Validate returned error: %v", err)
	}
	if !plan.Required || plan.Request.ToolName != "shell" || plan.Request.Operation != "shell.command" {
		t.Fatalf("unexpected approval plan: %#v", plan)
	}
	if plan.Request.Payload["fingerprint"] != review.Fingerprint || plan.Request.Payload["ruleKey"] != review.RuleKey {
		t.Fatalf("approval plan missing review payload: %#v", plan.Request.Payload)
	}
}

func TestShellRunsWithApprovalMetadata(t *testing.T) {
	root := t.TempDir()
	command := "printf ok"
	review := policy.ReviewCommand(policy.ShellPolicy{WorkingDir: root, RequireApproval: true}, command)
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:      root,
		RequireApproval: true,
		MaxTimeout:      time.Second,
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"approved command"}`), tool.Context{
		Metadata: map[string]any{
			approval.MetadataDecisionAction: string(approval.DecisionApprove),
			approval.MetadataFingerprint:    review.Fingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Structured["output"] != "ok" {
		t.Fatalf("unexpected result: %#v", result.Structured)
	}
}

func TestShellTimeoutAndOutputCap(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:     root,
		AllowCommands:  []string{"sleep", "printf"},
		MaxTimeout:     10 * time.Millisecond,
		MaxOutputBytes: 3,
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"sleep 1","description":"timeout"}`), tool.Context{})
	if !errors.Is(err, tool.ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got result=%#v err=%v", result, err)
	}

	// A generous budget: this case asserts the output cap and the
	// truncated flag, and a one-second budget made it fail under a fully
	// loaded parallel test run on a busy machine.
	result, err = shell.Call(context.Background(), json.RawMessage(`{"command":"printf abcdef","description":"cap output","timeoutMs":5000}`), tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Structured["output"] != "abc" || result.Structured["truncated"] != true {
		t.Fatalf("expected truncated output, got %#v", result.Structured)
	}
}

func TestShellOutputCapIsBoundedAndUTF8Safe(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:     root,
		AllowCommands:  []string{"printf"},
		MaxTimeout:     time.Second,
		MaxOutputBytes: 5,
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf '你好世界'","description":"cap utf8 output"}`), tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	output, _ := result.Structured["output"].(string)
	if output != "你" || !utf8.ValidString(output) || len(output) > 5 || result.Structured["truncated"] != true {
		t.Fatalf("unexpected UTF-8 truncation: output=%q result=%#v", output, result.Structured)
	}

	buffer := newBoundedBuffer(16)
	large := strings.Repeat("x", 1<<20)
	if _, err := buffer.Write([]byte(large)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if len(buffer.data) != 16 || !buffer.Truncated() {
		t.Fatalf("bounded buffer retained %d bytes, truncated=%v", len(buffer.data), buffer.Truncated())
	}
}

func TestShellBlocksCWDEscape(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{Policy: policy.ShellPolicy{
		WorkingDir:    root,
		AllowCommands: []string{"printf ok"},
		MaxTimeout:    time.Second,
	}})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","cwd":"..","description":"escape"}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected cwd escape error, got result=%#v err=%v", result, err)
	}
}

func TestShellRoutesCommandToSandboxBackend(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{Result: sandbox.ExecuteResult{ExitCode: 0, Stdout: "sandbox ok"}}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:     root,
			AllowCommands:  []string{"printf ok"},
			MaxTimeout:     time.Second,
			MaxOutputBytes: 1024,
			Env:            map[string]string{"ZEN": "forge", "DROP": "nope"},
			AllowedEnvKeys: []string{"ZEN"},
		},
		Backend:       ShellBackendSandbox,
		Sandbox:       fake,
		EnvironmentID: "go",
		Mounts:        []sandbox.Mount{{Source: root, Destination: "/workspace", Mode: "rw"}},
	})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"sandbox command"}`), tool.Context{
		RunID:      "run_1",
		ToolCallID: "call_1",
		Metadata:   map[string]any{"subtaskId": "task_1"},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Structured["output"] != "sandbox ok" || result.Structured["backend"] != string(ShellBackendSandbox) {
		t.Fatalf("unexpected sandbox result: %#v", result.Structured)
	}
	if len(fake.OpenCalls) != 1 || len(fake.ExecuteCalls) != 1 || len(fake.CloseCalls) != 1 {
		t.Fatalf("expected sandbox lifecycle calls, got %#v", fake)
	}
	if _, ok := sandbox.StateFromMetadata(result.Metadata); ok {
		t.Fatalf("closed sandbox session was returned for checkpoint reuse: %#v", result.Metadata)
	}
	if result.Metadata[sandbox.MetadataClearStateKey] != true {
		t.Fatalf("closed sandbox result did not clear checkpoint state: %#v", result.Metadata)
	}
	if fake.OpenCalls[0].SubtaskID != "task_1" || fake.OpenCalls[0].EnvironmentID != "go" {
		t.Fatalf("unexpected open request: %#v", fake.OpenCalls[0])
	}
	if got := fake.ExecuteCalls[0].Request.Env["ZEN"]; got != "forge" {
		t.Fatalf("expected allowed env propagated, got %q", got)
	}
}

func TestShellReusesSandboxSessionFromMetadata(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{Result: sandbox.ExecuteResult{ExitCode: 0, Stdout: "sandbox ok"}}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:    root,
			AllowCommands: []string{"printf ok"},
			MaxTimeout:    time.Second,
		},
		Backend:         ShellBackendSandbox,
		Sandbox:         fake,
		EnvironmentID:   "go",
		KeepSessionOpen: true,
	})
	first, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"first sandbox command"}`), tool.Context{
		RunID:      "run_1",
		ToolCallID: "call_1",
	})
	if err != nil {
		t.Fatalf("first Call returned error: %v", err)
	}
	second, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"second sandbox command"}`), tool.Context{
		RunID:      "run_1",
		ToolCallID: "call_2",
		Metadata:   first.Metadata,
	})
	if err != nil {
		t.Fatalf("second Call returned error: %v", err)
	}
	if second.Structured["output"] != "sandbox ok" {
		t.Fatalf("unexpected second result: %#v", second.Structured)
	}
	if len(fake.OpenCalls) != 1 || len(fake.ExecuteCalls) != 2 || len(fake.CloseCalls) != 0 {
		t.Fatalf("expected reused open session, got opens=%d executes=%d closes=%d", len(fake.OpenCalls), len(fake.ExecuteCalls), len(fake.CloseCalls))
	}
	if fake.ExecuteCalls[1].Session.ID != fake.ExecuteCalls[0].Session.ID {
		t.Fatalf("second call did not reuse session: first=%#v second=%#v", fake.ExecuteCalls[0].Session, fake.ExecuteCalls[1].Session)
	}
}

func TestShellSandboxCloseIsBestEffort(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{
		Result:     sandbox.ExecuteResult{ExitCode: 0, Stdout: "sandbox ok"},
		CloseError: sandbox.ErrSandboxUnavailable,
	}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:    root,
			AllowCommands: []string{"printf ok"},
			MaxTimeout:    time.Second,
		},
		Backend: ShellBackendSandbox,
		Sandbox: fake,
	})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"close best effort"}`), tool.Context{RunID: "run_1"})
	if err != nil {
		t.Fatalf("successful command was replaced by close error: result=%#v err=%v", result, err)
	}
	if result.Structured["output"] != "sandbox ok" || len(fake.CloseCalls) != 1 {
		t.Fatalf("unexpected close result: result=%#v closes=%d", result, len(fake.CloseCalls))
	}
	if _, ok := sandbox.StateFromMetadata(result.Metadata); ok {
		t.Fatalf("failed close leaked reusable state: %#v", result.Metadata)
	}
	if result.Metadata[sandbox.MetadataClearStateKey] != true {
		t.Fatalf("failed close did not clear checkpoint state: %#v", result.Metadata)
	}
}

func TestShellDoesNotRestoreSandboxSessionAcrossRunScope(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{Result: sandbox.ExecuteResult{ExitCode: 0, Stdout: "sandbox ok"}}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:    root,
			AllowCommands: []string{"printf ok"},
			MaxTimeout:    time.Second,
		},
		Backend:         ShellBackendSandbox,
		Sandbox:         fake,
		KeepSessionOpen: true,
	})
	first, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"first run"}`), tool.Context{RunID: "run_1"})
	if err != nil {
		t.Fatalf("first Call returned error: %v", err)
	}
	_, err = shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"second run"}`), tool.Context{
		RunID:    "run_2",
		Metadata: first.Metadata,
	})
	if err != nil {
		t.Fatalf("second Call returned error: %v", err)
	}
	if len(fake.OpenCalls) != 2 || fake.OpenCalls[1].RunID != "run_2" {
		t.Fatalf("cross-run session was restored: %#v", fake.OpenCalls)
	}
}

func TestShellSandboxUnavailableDoesNotFallback(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:    root,
			AllowCommands: []string{"printf ok"},
			MaxTimeout:    time.Second,
		},
		Backend: ShellBackendSandbox,
	})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"sandbox unavailable"}`), tool.Context{RunID: "run_1"})
	if !errors.Is(err, sandbox.ErrSandboxUnavailable) {
		t.Fatalf("expected sandbox unavailable, got result=%#v err=%v", result, err)
	}
	if result.ExitCode == 0 || result.Structured["backend"] != string(ShellBackendSandbox) {
		t.Fatalf("unexpected result/fallback: %#v", result)
	}
	if result.Structured["sandboxError"] != string(sandbox.ErrSandboxUnavailable) {
		t.Fatalf("expected structured sandbox error, got %#v", result.Structured)
	}
}

func TestShellSandboxTimeoutIncludesStructuredErrorCode(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{ExecuteError: sandbox.ErrTimeout}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:    root,
			AllowCommands: []string{"printf ok"},
			MaxTimeout:    time.Second,
		},
		Backend: ShellBackendSandbox,
		Sandbox: fake,
	})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"printf ok","description":"sandbox timeout"}`), tool.Context{RunID: "run_1"})
	if !errors.Is(err, tool.ErrTimeout) {
		t.Fatalf("expected tool timeout, got result=%#v err=%v", result, err)
	}
	if result.Structured["sandboxError"] != string(sandbox.ErrTimeout) || result.Structured["timedOut"] != true {
		t.Fatalf("expected structured timeout metadata, got %#v", result.Structured)
	}
}

func TestShellSandboxDenialAddsEscalationMarker(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{Result: sandbox.ExecuteResult{ExitCode: 1, Stderr: "mkdir: /x: Operation not permitted"}}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:     root,
			AllowCommands:  []string{"mkdir x"},
			MaxTimeout:     time.Second,
			MaxOutputBytes: 4096,
		},
		Backend: ShellBackendSandbox,
		Sandbox: fake,
	})
	result, err := shell.Call(context.Background(), json.RawMessage(`{"command":"mkdir x","description":"denied write"}`), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	output, _ := result.Structured["output"].(string)
	if !strings.Contains(output, "[sandbox: file access denied under sandbox mode]") {
		t.Fatalf("denial marker missing: %q", output)
	}
	if !strings.Contains(output, `sandboxPermissions="danger-full-access"`) {
		t.Fatalf("escalation hint missing: %q", output)
	}
	if escalated, _ := result.Structured["escalated"].(bool); escalated {
		t.Fatalf("plain sandbox call reported escalated: %#v", result.Structured)
	}
}

func TestShellEscalationValidation(t *testing.T) {
	root := t.TempDir()
	base := policy.ShellPolicy{WorkingDir: root, AllowCommands: []string{"printf ok"}, MaxTimeout: time.Second, MaxOutputBytes: 1024}
	confined := Must(Config{Policy: base, Backend: ShellBackendSandbox, Sandbox: &sandboxfake.Sandbox{}})
	unconfined := Must(Config{Policy: base})

	cases := []struct {
		name    string
		tool    tool.Tool
		args    string
		message string
	}{
		{"justification alone", confined, `{"command":"printf ok","description":"d","justification":"because"}`, "only valid together with sandboxPermissions"},
		{"mode without justification", confined, `{"command":"printf ok","description":"d","sandboxPermissions":"danger-full-access"}`, "requires a justification"},
		{"unknown mode", confined, `{"command":"printf ok","description":"d","sandboxPermissions":"workspace-write","justification":"j"}`, "unknown sandboxPermissions mode"},
		{"escalation while unconfined", unconfined, `{"command":"printf ok","description":"d","sandboxPermissions":"danger-full-access","justification":"j"}`, "only valid when the shell runs confined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.tool.Call(context.Background(), json.RawMessage(tc.args), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
			if !errors.Is(err, tool.ErrInvalidArguments) {
				t.Fatalf("Call error = %v, want ErrInvalidArguments", err)
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Call error = %q, want it to contain %q", err, tc.message)
			}
		})
	}
}

func TestShellEscalationRequiresApprovalThenRunsLocally(t *testing.T) {
	root := t.TempDir()
	fake := &sandboxfake.Sandbox{Result: sandbox.ExecuteResult{ExitCode: 1, Stderr: "Operation not permitted"}}
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:     root,
			AllowCommands:  []string{"printf ok"},
			MaxTimeout:     time.Second,
			MaxOutputBytes: 4096,
		},
		Backend: ShellBackendSandbox,
		Sandbox: fake,
	})
	args := json.RawMessage(`{"command":"printf ok","description":"escape hatch","sandboxPermissions":"danger-full-access","justification":"the sandbox denies the write"}`)
	call := tool.Context{RunID: "run_1", ToolCallID: "call_1"}

	result, err := shell.Call(context.Background(), args, call)
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("Call error = %v, want approval.ErrRequired", err)
	}
	request, ok := approval.RequestFromResult(result)
	if !ok {
		t.Fatalf("escalation result carried no approval request: %#v", result)
	}
	if request.Title != "Approve sandbox escalation" || request.Operation != "shell.escalate" {
		t.Fatalf("unexpected escalation request: %+v", request)
	}
	fingerprint, _ := request.Payload["fingerprint"].(string)
	if !strings.HasPrefix(fingerprint, "escalate\x00") {
		t.Fatalf("escalation fingerprint not namespaced: %q", fingerprint)
	}
	if !strings.Contains(request.Description, "the sandbox denies the write") {
		t.Fatalf("justification missing from request: %+v", request)
	}
	if len(fake.ExecuteCalls) != 0 {
		t.Fatalf("command executed before escalation approval: %#v", fake.ExecuteCalls)
	}

	metadata := approval.ApprovedMetadata(nil, request, approval.Decision{Action: approval.DecisionApprove, Scope: approval.ScopeRun})
	approvedCall := tool.Context{RunID: "run_1", ToolCallID: "call_1", Metadata: metadata}
	result, err = shell.Call(context.Background(), args, approvedCall)
	if err != nil {
		t.Fatalf("approved escalation Call returned error: %v", err)
	}
	if result.Structured["backend"] != string(ShellBackendLocal) {
		t.Fatalf("escalated call did not run locally: %#v", result.Structured)
	}
	if escalated, _ := result.Structured["escalated"].(bool); !escalated {
		t.Fatalf("escalated flag missing: %#v", result.Structured)
	}
	if result.Structured["output"] != "ok" {
		t.Fatalf("unexpected local output: %#v", result.Structured)
	}
	if len(fake.ExecuteCalls) != 0 {
		t.Fatalf("escalated call still routed through the sandbox: %#v", fake.ExecuteCalls)
	}
}

func TestShellEscalationDoesNotBypassBlockedCommand(t *testing.T) {
	root := t.TempDir()
	shell := Must(Config{
		Policy: policy.ShellPolicy{
			WorkingDir:      root,
			DenyCommands:    []string{"rm"},
			RequireApproval: true,
			MaxTimeout:      time.Second,
			MaxOutputBytes:  1024,
		},
		Backend: ShellBackendSandbox,
		Sandbox: &sandboxfake.Sandbox{},
	})
	_, err := shell.Call(context.Background(), json.RawMessage(`{"command":"rm -rf tmp","description":"d","sandboxPermissions":"danger-full-access","justification":"j"}`), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if err == nil || !strings.Contains(err.Error(), "command blocked") {
		t.Fatalf("escalation bypassed the block: %v", err)
	}
}

func TestShellSchemaHidesEscalationWhenUnconfined(t *testing.T) {
	base := policy.ShellPolicy{WorkingDir: t.TempDir(), MaxTimeout: time.Second, MaxOutputBytes: 1024}
	unconfined := Must(Config{Policy: base})
	props, _ := unconfined.Schema()["properties"].(map[string]any)
	if _, ok := props["sandboxPermissions"]; ok {
		t.Fatalf("unconfined schema exposes sandboxPermissions")
	}
	if _, ok := props["justification"]; ok {
		t.Fatalf("unconfined schema exposes justification")
	}
	confined := Must(Config{Policy: base, Backend: ShellBackendSandbox})
	props, _ = confined.Schema()["properties"].(map[string]any)
	if _, ok := props["sandboxPermissions"]; !ok {
		t.Fatalf("confined schema hides sandboxPermissions")
	}
	if _, ok := props["justification"]; !ok {
		t.Fatalf("confined schema hides justification")
	}
}

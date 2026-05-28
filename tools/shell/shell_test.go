package shell

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
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

	result, err = shell.Call(context.Background(), json.RawMessage(`{"command":"printf abcdef","description":"cap output","timeoutMs":1000}`), tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Structured["output"] != "abc" || result.Structured["truncated"] != true {
		t.Fatalf("expected truncated output, got %#v", result.Structured)
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

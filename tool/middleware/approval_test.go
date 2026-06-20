package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

func TestApprovalMiddlewareApprovesAndRetries(t *testing.T) {
	calls := 0
	req := testApprovalRequest()
	invoker := Approval(ApprovalConfig{Broker: approval.AlwaysAllow()})(tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		calls++
		if approval.IsApprovedAction(call.Metadata[approval.MetadataDecisionAction]) {
			return tool.Result{Output: "ran"}, nil
		}
		return approval.RequiredResult(req), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_1", Name: "shell", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Output != "ran" || calls != 2 {
		t.Fatalf("result=%#v calls=%d", result, calls)
	}
}

func TestApprovalMiddlewareRejects(t *testing.T) {
	invoker := Approval(ApprovalConfig{Broker: approval.AlwaysDeny("no")})(tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		return approval.RequiredResult(testApprovalRequest()), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_1", Name: "shell"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Error != approval.ErrorRejected {
		t.Fatalf("result = %#v", result)
	}
}

func TestApprovalMiddlewareRejectsMismatchedDecisionIdentity(t *testing.T) {
	calls := 0
	broker := approval.BrokerFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		return approval.Decision{RequestID: "approval_other", Action: approval.DecisionApprove, Scope: approval.ScopeOnce}, nil
	})
	invoker := Approval(ApprovalConfig{Broker: broker})(tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		calls++
		return approval.RequiredResult(testApprovalRequest()), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_1", Name: "shell"})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected identity mismatch, got result=%#v err=%v", result, err)
	}
	if calls != 1 {
		t.Fatalf("tool calls = %d, want no approved retry", calls)
	}
}

func TestApprovalMiddlewareRejectsMalformedRequestBeforeBroker(t *testing.T) {
	brokerCalls := 0
	broker := approval.BrokerFunc(func(context.Context, approval.Request) (approval.Decision, error) {
		brokerCalls++
		return approval.Decision{}, nil
	})
	invoker := Approval(ApprovalConfig{Broker: broker})(tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		return approval.RequiredResult(approval.Request{ID: "approval_1"}), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_1", Name: "shell"})
	if err == nil || !strings.Contains(err.Error(), "operation is required") {
		t.Fatalf("expected malformed request error, got result=%#v err=%v", result, err)
	}
	if brokerCalls != 0 {
		t.Fatalf("broker calls = %d, want zero", brokerCalls)
	}
}

func TestApprovalMiddlewareBindsRuntimeIdentity(t *testing.T) {
	var captured approval.Request
	broker := approval.BrokerFunc(func(_ context.Context, req approval.Request) (approval.Decision, error) {
		captured = req
		return approval.Decision{RequestID: req.ID, Action: approval.DecisionApprove}, nil
	})
	invoker := Approval(ApprovalConfig{Broker: broker})(tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if approval.IsApprovedAction(call.Metadata[approval.MetadataDecisionAction]) {
			return tool.Result{Output: "ran"}, nil
		}
		req := testApprovalRequest()
		req.ID = ""
		req.RunID = "forged_run"
		req.ToolCallID = "forged_call"
		req.ToolName = "forged_tool"
		return approval.RequiredResult(req), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_real", RunID: "run_real", Name: "shell"})
	if err != nil || result.Output != "ran" {
		t.Fatalf("Invoke result=%#v err=%v", result, err)
	}
	if captured.ID == "" || captured.RunID != "run_real" || captured.ToolCallID != "call_real" || captured.ToolName != "shell" {
		t.Fatalf("request identity was not bound: %#v", captured)
	}
}

func TestApprovalMiddlewareWithoutBrokerReturnsBoundRequiredRequest(t *testing.T) {
	invoker := Approval(ApprovalConfig{})(tool.InvokerFunc(func(context.Context, tool.Call) (tool.Result, error) {
		req := testApprovalRequest()
		req.RunID = "forged"
		return approval.RequiredResult(req), nil
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_real", Name: "shell"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("expected ErrRequired, got result=%#v err=%v", result, err)
	}
	req, ok := approval.RequestFromResult(result)
	if !ok || req.RunID != "run_real" || req.ToolCallID != "call_1" || req.ToolName != "shell" {
		t.Fatalf("required result was not bound: %#v ok=%v", req, ok)
	}
}

func TestApprovalMiddlewareReturnsExpiredCode(t *testing.T) {
	broker := approval.BrokerFunc(func(_ context.Context, req approval.Request) (approval.Decision, error) {
		return approval.Decision{RequestID: req.ID, Action: approval.DecisionReject, Reason: approval.ErrorExpired}, nil
	})
	invoker := Approval(ApprovalConfig{Broker: broker})(tool.InvokerFunc(func(context.Context, tool.Call) (tool.Result, error) {
		return approval.RequiredResult(testApprovalRequest()), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_1", Name: "shell"})
	if err != nil || result.Error != approval.ErrorExpired {
		t.Fatalf("Invoke result=%#v err=%v", result, err)
	}
}

func TestApprovalMiddlewareAbortSignalsCancellation(t *testing.T) {
	calls := 0
	broker := approval.BrokerFunc(func(_ context.Context, req approval.Request) (approval.Decision, error) {
		return approval.Decision{RequestID: req.ID, Action: approval.DecisionAbort, Scope: approval.ScopeOnce, Reason: "stop run"}, nil
	})
	invoker := Approval(ApprovalConfig{Broker: broker})(tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		calls++
		return approval.RequiredResult(testApprovalRequest()), approval.ErrRequired
	}))
	result, err := invoker.Invoke(context.Background(), tool.Call{ID: "call_1", RunID: "run_1", Name: "shell"})
	if !errors.Is(err, approval.ErrAborted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected abort cancellation, got result=%#v err=%v", result, err)
	}
	if calls != 1 || result.ExitCode != 1 {
		t.Fatalf("unexpected abort result=%#v calls=%d", result, calls)
	}
}

func testApprovalRequest() approval.Request {
	return approval.Request{
		ID:        "approval_1",
		RunID:     "run_1",
		Operation: "shell.command",
		Title:     "Approve command",
		Risk:      approval.RiskMedium,
		Options:   approval.DefaultOptions(),
		CreatedAt: time.Now().UTC(),
		Payload:   map[string]any{"fingerprint": "abc"},
	}
}

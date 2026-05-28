package middleware

import (
	"context"
	"encoding/json"
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

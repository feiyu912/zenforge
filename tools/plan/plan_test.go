package plan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

func TestExitPlanModeRequiresApprovalCarryingThePlan(t *testing.T) {
	instance, err := New()
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if !tool.ReadOnlyOf(instance) {
		t.Fatal("exit_plan_mode must declare itself read-only")
	}
	args, _ := json.Marshal(map[string]string{"plan": "# Plan\n\n1. do the thing"})
	result, err := instance.Call(context.Background(), args, tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("approval error = %v", err)
	}
	request, ok := result.Structured["approval"].(approval.Request)
	if !ok {
		t.Fatalf("structured approval = %#v", result.Structured)
	}
	if request.Operation != "plan.approve" || request.Payload["plan"] == nil || request.Payload["fingerprint"] == nil {
		t.Fatalf("request = %#v", request)
	}
	if !strings.Contains(request.Description, "plan: Plan") {
		t.Fatalf("description = %q", request.Description)
	}
	if planText, _ := request.Payload["plan"].(string); !strings.Contains(planText, "do the thing") {
		t.Fatalf("payload plan = %#v", request.Payload)
	}

	// An approved call returns the plan and the mode switch the agent
	// reacts to.
	approved, err := instance.Call(context.Background(), args, tool.Context{
		RunID:      "run_1",
		ToolCallID: "call_1",
		Metadata: approval.ApprovedMetadata(nil, request,
			approval.Decision{Action: approval.DecisionApprove, Scope: approval.ScopeOnce}),
	})
	if err != nil {
		t.Fatalf("approved call returned error: %v", err)
	}
	if approved.Structured["approved"] != true {
		t.Fatalf("approved result = %#v", approved.Structured)
	}

	// A different plan cannot reuse that approval.
	other, _ := json.Marshal(map[string]string{"plan": "a different plan"})
	_, err = instance.Call(context.Background(), other, tool.Context{
		RunID:      "run_1",
		ToolCallID: "call_1",
		Metadata: approval.ApprovedMetadata(nil, request,
			approval.Decision{Action: approval.DecisionApprove, Scope: approval.ScopeOnce}),
	})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("reused approval error = %v", err)
	}
}

func TestExitPlanModeRejectsAnEmptyPlan(t *testing.T) {
	instance, err := New()
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	_, err = instance.Call(context.Background(), json.RawMessage(`{"plan":"   "}`), tool.Context{})
	if !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("empty plan error = %v", err)
	}
}

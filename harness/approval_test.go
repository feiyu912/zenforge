package harness

import "testing"

func TestApprovalStateHelpers(t *testing.T) {
	state := RunState{Control: RunControlState{Status: RunStatusRunning}}
	state.SetWaitingApproval(ApprovalRequestState{ID: "approval_1", Title: "Approve", Risk: "medium"})
	if state.Phase != RunPhaseApproval || state.Control.Status != RunStatusWaitingSubmit {
		t.Fatalf("state not waiting for approval: %#v", state)
	}
	if state.Approval.Waiting == nil || state.Control.AwaitingIDs[0] != "approval_1" {
		t.Fatalf("approval request not recorded: %#v", state.Approval)
	}
	state.ResolveApproval(NewApprovalDecisionState("approval_1", "reject", "once", "no", nil))
	if state.Approval.Waiting != nil || len(state.Approval.Resolved) != 1 {
		t.Fatalf("approval decision not recorded: %#v", state.Approval)
	}
	if len(state.Control.AwaitingIDs) != 0 || state.Control.Status != RunStatusRunning {
		t.Fatalf("control not restored: %#v", state.Control)
	}
}

func TestApprovalGrantReplacesMatchingScopeKey(t *testing.T) {
	state := RunState{}
	state.AddApprovalGrant(ApprovalGrantState{
		RequestID:   "approval_1",
		Action:      "approve",
		Scope:       "run",
		Fingerprint: "fingerprint_1",
	})
	state.AddApprovalGrant(ApprovalGrantState{
		RequestID:   "approval_2",
		Action:      "always",
		Scope:       "run",
		Fingerprint: "fingerprint_1",
	})
	if len(state.Approval.Grants) != 1 || state.Approval.Grants[0].RequestID != "approval_2" {
		t.Fatalf("approval grants = %#v", state.Approval.Grants)
	}
}

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

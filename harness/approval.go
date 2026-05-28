package harness

import "time"

func (s *RunState) SetWaitingApproval(req ApprovalRequestState) {
	s.Phase = RunPhaseApproval
	s.Control.Status = RunStatusWaitingSubmit
	s.Approval.Waiting = &req
	if req.ID != "" {
		s.Control.AwaitingIDs = appendUniqueString(s.Control.AwaitingIDs, req.ID)
	}
}

func (s *RunState) ResolveApproval(decision ApprovalDecisionState) {
	s.Approval.Waiting = nil
	s.Approval.Resolved = append(s.Approval.Resolved, decision)
	s.Control.AwaitingIDs = removeString(s.Control.AwaitingIDs, decision.RequestID)
	if s.Control.Status == RunStatusWaitingSubmit {
		s.Control.Status = RunStatusRunning
	}
}

func NewApprovalDecisionState(requestID, action, scope, reason string, payload map[string]any) ApprovalDecisionState {
	return ApprovalDecisionState{
		RequestID: requestID,
		Action:    action,
		Scope:     scope,
		Reason:    reason,
		Payload:   payload,
		DecidedAt: time.Now().UTC(),
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func removeString(values []string, value string) []string {
	out := values[:0]
	for _, current := range values {
		if current != value {
			out = append(out, current)
		}
	}
	return out
}

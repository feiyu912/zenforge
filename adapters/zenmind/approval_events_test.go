package zenmind

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

func TestApprovalEventBridgeRealEventsAndRecovery(t *testing.T) {
	now := time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC)
	context := PlatformRequestContext{RequestID: "platform-request", ChatID: "chat-1", AgentKey: "agent-1"}
	allocate := func(_ zenforge.Event, req approval.Request) (string, error) {
		return "await-" + req.ID, nil
	}
	bridge, err := NewApprovalEventBridge(context, allocate, 45)
	if err != nil {
		t.Fatal(err)
	}
	req := approvalEventRequest(now, "approval-1")
	askValue, err := bridge.Handle(requestedApprovalEvent(req, now))
	if err != nil {
		t.Fatalf("requested: %v", err)
	}
	ask := askValue.(AwaitingAsk)
	if ask.AwaitingID != "await-approval-1" || ask.Timeout != 45 || ask.AgentKey != context.AgentKey {
		t.Fatalf("ask = %#v", ask)
	}

	data, err := json.Marshal(bridge.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot ApprovalEventBridgeSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	bridge, err = NewApprovalEventBridgeFromSnapshot(context, allocate, 45, snapshot)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	resolved := zenforge.NewEvent(zenforge.EventApprovalResolved, req.RunID, map[string]any{
		"requestId": req.ID, "toolCallId": req.ToolCallID, "toolName": req.ToolName,
		"action": string(approval.DecisionApprove), "scope": string(approval.ScopeRun), "reason": "",
	})
	resolved.Timestamp = now.Add(time.Minute).UnixMilli()
	answerValue, err := bridge.Handle(resolved)
	if err != nil {
		t.Fatalf("resolved: %v", err)
	}
	answer := answerValue.(AwaitingAnswer)
	if answer.AwaitingID != ask.AwaitingID || answer.Status != PlatformStatusAnswered ||
		len(answer.Approvals) != 1 || answer.Approvals[0].Decision != PlatformDecisionApprove {
		t.Fatalf("answer = %#v", answer)
	}
	if _, err := bridge.Handle(resolved); err == nil {
		t.Fatal("duplicate resolved event accepted")
	}
}

func TestApprovalEventBridgeExpiredAndTimeout(t *testing.T) {
	now := time.Now().UTC()
	bridge := newApprovalEventBridge(t, 9)
	req := approvalEventRequest(now, "approval-expired")
	if _, err := bridge.Handle(requestedApprovalEvent(req, now)); err != nil {
		t.Fatal(err)
	}
	expired := zenforge.NewEvent(zenforge.EventApprovalExpired, req.RunID, map[string]any{
		"requestId": req.ID, "toolCallId": req.ToolCallID, "toolName": req.ToolName,
		"action": string(approval.DecisionReject), "scope": string(approval.ScopeOnce),
		"reason": approval.ErrorExpired,
	})
	value, err := bridge.Handle(expired)
	if err != nil {
		t.Fatalf("expired: %v", err)
	}
	answer := value.(AwaitingAnswer)
	if answer.Status != PlatformStatusError || answer.Error == nil ||
		answer.Error.Code != PlatformErrorTimeout {
		t.Fatalf("expired answer = %#v", answer)
	}
}

func TestApprovalEventBridgeHandlesResumedAndReusedEvents(t *testing.T) {
	now := time.Now().UTC()
	bridge := newApprovalEventBridge(t, 9)
	req := approvalEventRequest(now, "approval-resumed")
	first := requestedApprovalEvent(req, now)
	value, err := bridge.Handle(first)
	if err != nil {
		t.Fatal(err)
	}
	ask := value.(AwaitingAsk)

	resumed := requestedApprovalEvent(req, now.Add(time.Second))
	resumed.Payload["resumed"] = true
	value, err = bridge.Handle(resumed)
	if err != nil {
		t.Fatalf("resumed request: %v", err)
	}
	if replayed := value.(AwaitingAsk); !reflect.DeepEqual(replayed, ask) {
		t.Fatalf("resumed ask = %#v, want %#v", replayed, ask)
	}

	reusedReq := approvalEventRequest(now, "approval-reused")
	reused := zenforge.NewEvent(zenforge.EventApprovalResolved, reusedReq.RunID, map[string]any{
		"requestId": reusedReq.ID, "toolCallId": reusedReq.ToolCallID, "toolName": reusedReq.ToolName,
		"action": string(approval.DecisionApprove), "scope": string(approval.ScopeRule),
		"reason": approval.ReasonReused, "reused": true,
	})
	value, err = bridge.Handle(reused)
	if err != nil || value != nil {
		t.Fatalf("reused resolution = %#v, %v; want no awaiting answer", value, err)
	}
	if _, err := bridge.Handle(reused); err == nil {
		t.Fatal("duplicate reused resolution accepted")
	}
}

func TestApprovalEventBridgeSnapshotIsIsolated(t *testing.T) {
	now := time.Now().UTC()
	bridge := newApprovalEventBridge(t, 9)
	req := approvalEventRequest(now, "approval-snapshot")
	if _, err := bridge.Handle(requestedApprovalEvent(req, now)); err != nil {
		t.Fatal(err)
	}
	snapshot := bridge.Snapshot()
	correlation := snapshot.Pending[req.ID]
	correlation.Ask.Approvals[0].Command = "mutated"
	correlation.Ask.Approvals[0].Options[0].Label = "mutated"
	snapshot.Pending[req.ID] = correlation
	fresh := bridge.Snapshot().Pending[req.ID].Ask
	if fresh.Approvals[0].Command == "mutated" || fresh.Approvals[0].Options[0].Label == "mutated" {
		t.Fatalf("snapshot mutation escaped into bridge: %#v", fresh)
	}
}

func TestApprovalEventBridgeFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name  string
		event func(approval.Request) zenforge.Event
	}{
		{"identity mismatch", func(req approval.Request) zenforge.Event {
			event := requestedApprovalEvent(req, now)
			event.Payload["requestId"] = "other"
			return event
		}},
		{"missing field", func(req approval.Request) zenforge.Event {
			event := requestedApprovalEvent(req, now)
			delete(event.Payload, "toolName")
			return event
		}},
		{"unknown field", func(req approval.Request) zenforge.Event {
			event := requestedApprovalEvent(req, now)
			event.Payload["surprise"] = true
			return event
		}},
		{"unknown event", func(req approval.Request) zenforge.Event {
			return zenforge.NewEvent(zenforge.EventRunDone, req.RunID, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge := newApprovalEventBridge(t, 3)
			if _, err := bridge.Handle(tt.event(approvalEventRequest(now, "approval-bad"))); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}

	bridge := newApprovalEventBridge(t, 3)
	req := approvalEventRequest(now, "approval-duplicate")
	event := requestedApprovalEvent(req, now)
	if _, err := bridge.Handle(event); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Handle(event); err == nil {
		t.Fatal("duplicate requested event accepted")
	}
	unknown := zenforge.NewEvent(zenforge.EventApprovalResolved, req.RunID, map[string]any{
		"requestId": "missing", "toolCallId": req.ToolCallID, "toolName": req.ToolName,
		"action": string(approval.DecisionApprove), "scope": string(approval.ScopeRun), "reason": "",
	})
	if _, err := bridge.Handle(unknown); err == nil {
		t.Fatal("uncorrelated resolved event accepted")
	}

	sameID, err := NewApprovalEventBridge(
		PlatformRequestContext{RequestID: "platform-request", ChatID: "chat-1", AgentKey: "agent-1"},
		func(zenforge.Event, approval.Request) (string, error) { return "same-awaiting", nil },
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sameID.Handle(requestedApprovalEvent(approvalEventRequest(now, "first"), now)); err != nil {
		t.Fatal(err)
	}
	if _, err := sameID.Handle(requestedApprovalEvent(approvalEventRequest(now, "second"), now)); err == nil {
		t.Fatal("duplicate awaiting id accepted")
	}
}

func newApprovalEventBridge(t *testing.T, timeout int64) *ApprovalEventBridge {
	t.Helper()
	bridge, err := NewApprovalEventBridge(
		PlatformRequestContext{RequestID: "platform-request", ChatID: "chat-1", AgentKey: "agent-1"},
		func(_ zenforge.Event, req approval.Request) (string, error) { return "await-" + req.ID, nil },
		timeout,
	)
	if err != nil {
		t.Fatal(err)
	}
	return bridge
}

func approvalEventRequest(now time.Time, id string) approval.Request {
	return approval.Request{
		ID: id, RunID: "run-1", ToolCallID: "tool-call-1", ToolName: "shell",
		Operation: "shell.execute", Title: "Run command", Risk: approval.RiskHigh,
		CreatedAt: now,
		Options: []approval.Option{
			{Action: approval.DecisionApprove, Scope: approval.ScopeRun, Label: "Approve"},
			{Action: approval.DecisionReject, Scope: approval.ScopeOnce, Label: "Reject"},
		},
	}
}

func requestedApprovalEvent(req approval.Request, now time.Time) zenforge.Event {
	event := zenforge.NewEvent(zenforge.EventApprovalRequested, req.RunID, map[string]any{
		"requestId": req.ID, "toolCallId": req.ToolCallID, "toolName": req.ToolName,
		"operation": req.Operation, "risk": string(req.Risk), "request": req,
	})
	event.Timestamp = now.UnixMilli()
	return event
}

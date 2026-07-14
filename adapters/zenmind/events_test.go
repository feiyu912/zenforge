package zenmind

import (
	"encoding/json"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

func TestMapEventUsesDefaultCompatibilityNames(t *testing.T) {
	tests := []struct {
		event zenforge.EventType
		want  string
	}{
		{event: zenforge.EventRunStarted, want: "run.start"},
		{event: zenforge.EventRunDone, want: "run.complete"},
		{event: zenforge.EventModelDelta, want: "content.delta"},
		{event: zenforge.EventToolCall, want: "tool.start"},
		{event: zenforge.EventTodoUpdated, want: "plan.update"},
		{event: zenforge.EventRequestSteer, want: "request.steer"},
		{event: zenforge.EventApprovalRequested, want: "awaiting.ask"},
		{event: zenforge.EventApprovalResolved, want: "awaiting.answer"},
		{event: zenforge.EventSubtaskStarted, want: "task.start"},
		{event: zenforge.EventSubtaskDone, want: "task.complete"},
	}

	for _, tt := range tests {
		event := zenforge.NewEvent(tt.event, "run_123", map[string]any{"value": "x"}).WithSeq(3)
		event.Timestamp = 42

		got := MapEvent(event)

		if got.Type != tt.want {
			t.Fatalf("MapEvent(%s).Type = %q, want %q", tt.event, got.Type, tt.want)
		}
		if got.Source != string(tt.event) {
			t.Fatalf("Source = %q, want %q", got.Source, tt.event)
		}
		if got.RunID != "run_123" || got.Seq != 3 || got.Timestamp != 42 {
			t.Fatalf("unexpected envelope: %#v", got)
		}
		if got.Payload["runId"] != "run_123" || got.Payload["value"] != "x" {
			t.Fatalf("payload did not preserve source event fields: %#v", got.Payload)
		}
	}
}

func TestMapperAllowsTypeOverrides(t *testing.T) {
	mapper := NewMapper()
	mapper.Types[zenforge.EventModelDelta] = "reasoning.delta"

	got := mapper.Map(zenforge.NewEvent(zenforge.EventModelDelta, "run_123", nil))

	if got.Type != "reasoning.delta" {
		t.Fatalf("mapped type = %q", got.Type)
	}
}

func TestMapEventFallsBackToSourceType(t *testing.T) {
	event := zenforge.NewEvent(zenforge.EventType("custom.event"), "run_123", nil)

	got := MapEvent(event)

	if got.Type != "custom.event" || got.Source != "custom.event" {
		t.Fatalf("unexpected custom event mapping: %#v", got)
	}
}

func TestDecisionFromSubmitDefaultsScope(t *testing.T) {
	decision, err := DecisionFromSubmit(SubmitPayload{
		RequestID: "approval_123",
		Action:    approval.DecisionApprove,
		Payload:   map[string]any{"note": "ok"},
	})
	if err != nil {
		t.Fatalf("DecisionFromSubmit returned error: %v", err)
	}
	if decision.RequestID != "approval_123" || decision.Action != approval.DecisionApprove {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	if decision.Scope != approval.ScopeOnce {
		t.Fatalf("scope = %q, want once", decision.Scope)
	}
	if decision.DecidedAt.IsZero() {
		t.Fatalf("DecidedAt was not set")
	}
	if decision.Payload["note"] != "ok" {
		t.Fatalf("payload was not preserved: %#v", decision.Payload)
	}
}

func TestDecisionFromJSONValidatesSubmitPayload(t *testing.T) {
	data, err := json.Marshal(SubmitPayload{
		RequestID: "approval_123",
		Action:    approval.DecisionReject,
		Reason:    "no",
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	decision, err := DecisionFromJSON(data)
	if err != nil {
		t.Fatalf("DecisionFromJSON returned error: %v", err)
	}
	if decision.Action != approval.DecisionReject || decision.Reason != "no" {
		t.Fatalf("unexpected decision: %#v", decision)
	}

	if _, err := DecisionFromJSON([]byte(`{"requestId":"approval_123"}`)); err == nil {
		t.Fatalf("expected validation error")
	}
}

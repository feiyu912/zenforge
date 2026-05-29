package zenmind

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

type StreamEvent struct {
	Seq       int64          `json:"seq,omitempty"`
	Type      string         `json:"type"`
	RunID     string         `json:"runId,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Source    string         `json:"source"`
}

// Mapper converts ZenForge public events into host-platform compatibility
// event names.
type Mapper struct {
	Types map[zenforge.EventType]string
}

// NewMapper returns a mapper with the default compatibility event names.
func NewMapper() Mapper {
	return Mapper{Types: DefaultEventTypes()}
}

// DefaultEventTypes returns the default ZenForge-to-platform event name map.
func DefaultEventTypes() map[zenforge.EventType]string {
	return map[zenforge.EventType]string{
		zenforge.EventRunStarted:        "run.start",
		zenforge.EventRunResumed:        "run.resume",
		zenforge.EventRunDone:           "run.complete",
		zenforge.EventRunError:          "run.error",
		zenforge.EventRunCancelled:      "run.cancel",
		zenforge.EventStepStarted:       "step.start",
		zenforge.EventStepDone:          "step.complete",
		zenforge.EventModelStarted:      "model.start",
		zenforge.EventModelDelta:        "content.delta",
		zenforge.EventModelDone:         "model.complete",
		zenforge.EventToolCall:          "tool.start",
		zenforge.EventToolResult:        "tool.result",
		zenforge.EventToolError:         "tool.error",
		zenforge.EventTodoUpdated:       "plan.update",
		zenforge.EventWorkspaceChanged:  "workspace.change",
		zenforge.EventApprovalRequested: "awaiting.ask",
		zenforge.EventApprovalResolved:  "awaiting.answer",
		zenforge.EventApprovalExpired:   "awaiting.expired",
		zenforge.EventSubtaskStarted:    "task.start",
		zenforge.EventSubtaskEvent:      "task.event",
		zenforge.EventSubtaskDone:       "task.complete",
		zenforge.EventSubtaskError:      "task.error",
		zenforge.EventTaskStarted:       "task.start",
		zenforge.EventTaskDone:          "task.complete",
		zenforge.EventTaskError:         "task.error",
		zenforge.EventTaskCancelled:     "task.cancel",
		zenforge.EventCheckpointCreated: "checkpoint.create",
	}
}

// Map projects one ZenForge event into a compatibility stream event.
func (m Mapper) Map(event zenforge.Event) StreamEvent {
	eventType := string(event.Type)
	if mapped, ok := m.Types[event.Type]; ok && mapped != "" {
		eventType = mapped
	}
	return StreamEvent{
		Seq:       event.Seq,
		Type:      eventType,
		RunID:     event.RunID(),
		Timestamp: event.Timestamp,
		Payload:   cloneMap(event.Map()),
		Source:    string(event.Type),
	}
}

// MapEvent projects one ZenForge event with the default mapper.
func MapEvent(event zenforge.Event) StreamEvent {
	return NewMapper().Map(event)
}

// MapEvents projects a batch of ZenForge events with the default mapper.
func MapEvents(events []zenforge.Event) []StreamEvent {
	mapper := NewMapper()
	out := make([]StreamEvent, 0, len(events))
	for _, event := range events {
		out = append(out, mapper.Map(event))
	}
	return out
}

// SubmitPayload is the neutral approval submit shape accepted by the adapter.
type SubmitPayload struct {
	RequestID string                  `json:"requestId"`
	Action    approval.DecisionAction `json:"action"`
	Scope     approval.DecisionScope  `json:"scope,omitempty"`
	Reason    string                  `json:"reason,omitempty"`
	Payload   map[string]any          `json:"payload,omitempty"`
}

// DecisionFromSubmit converts a neutral submit payload into an approval
// decision.
func DecisionFromSubmit(payload SubmitPayload) (approval.Decision, error) {
	decision := approval.Decision{
		RequestID: payload.RequestID,
		Action:    payload.Action,
		Scope:     payload.Scope,
		Reason:    payload.Reason,
		Payload:   cloneMap(payload.Payload),
		DecidedAt: time.Now().UTC(),
	}
	if decision.Scope == "" {
		decision.Scope = approval.ScopeOnce
	}
	if err := decision.Validate(); err != nil {
		return approval.Decision{}, err
	}
	return decision, nil
}

// DecisionFromJSON decodes a neutral submit payload and returns an approval
// decision.
func DecisionFromJSON(data []byte) (approval.Decision, error) {
	var payload SubmitPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return approval.Decision{}, err
	}
	decision, err := DecisionFromSubmit(payload)
	if err != nil {
		return approval.Decision{}, fmt.Errorf("invalid submit payload: %w", err)
	}
	return decision, nil
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

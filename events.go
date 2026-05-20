package zenforge

import "time"

type EventType string

const (
	EventRunStarted        EventType = "run.started"
	EventRunResumed        EventType = "run.resumed"
	EventRunDone           EventType = "run.done"
	EventRunError          EventType = "run.error"
	EventStepStarted       EventType = "step.started"
	EventStepDone          EventType = "step.done"
	EventModelStarted      EventType = "model.started"
	EventModelDelta        EventType = "model.delta"
	EventModelDone         EventType = "model.done"
	EventToolCall          EventType = "tool.call"
	EventToolResult        EventType = "tool.result"
	EventToolError         EventType = "tool.error"
	EventTodoUpdated       EventType = "todo.updated"
	EventWorkspaceChanged  EventType = "workspace.changed"
	EventApprovalRequested EventType = "approval.requested"
	EventApprovalResolved  EventType = "approval.resolved"
	EventSubtaskStarted    EventType = "subtask.started"
	EventSubtaskDone       EventType = "subtask.done"
	EventCheckpointCreated EventType = "checkpoint.created"
)

type Event struct {
	Type      EventType      `json:"type"`
	RunID     string         `json:"runId,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

func NewEvent(eventType EventType, runID string, data map[string]any) Event {
	return Event{
		Type:      eventType,
		RunID:     runID,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
}

func (e Event) String() string {
	return string(e.Type)
}


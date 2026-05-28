package zenforge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type EventType string

type EventData map[string]any

const (
	EventRunStarted        EventType = "run.started"
	EventRunResumed        EventType = "run.resumed"
	EventRunDone           EventType = "run.done"
	EventRunError          EventType = "run.error"
	EventRunCancelled      EventType = "run.cancelled"
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
	EventApprovalExpired   EventType = "approval.expired"
	EventSubtaskStarted    EventType = "subtask.started"
	EventSubtaskDone       EventType = "subtask.done"
	EventTaskStarted       EventType = "task.started"
	EventTaskDone          EventType = "task.done"
	EventTaskError         EventType = "task.error"
	EventTaskCancelled     EventType = "task.cancelled"
	EventCheckpointCreated EventType = "checkpoint.created"
)

type Event struct {
	Seq       int64
	Type      EventType
	Timestamp int64
	Payload   EventData
}

func NewEvent(eventType EventType, runID string, data map[string]any) Event {
	payload := cloneEventPayload(data)
	if payload == nil {
		payload = EventData{}
	}
	if runID != "" {
		payload["runId"] = runID
	}
	return Event{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func EventFromMap(data map[string]any) Event {
	payload := cloneEventPayload(data)
	if payload == nil {
		payload = EventData{}
	}
	seq, _ := int64Value(payload["seq"])
	timestamp, _ := int64Value(payload["timestamp"])
	eventType, _ := payload["type"].(string)
	delete(payload, "seq")
	delete(payload, "type")
	delete(payload, "timestamp")
	return Event{
		Seq:       seq,
		Type:      EventType(eventType),
		Timestamp: timestamp,
		Payload:   payload,
	}
}

func NextEventSeq(latestSeq int64) int64 {
	return latestSeq + 1
}

func (e Event) WithSeq(seq int64) Event {
	e.Seq = seq
	return e
}

func (e Event) RunID() string {
	runID, _ := e.Value("runId").(string)
	return runID
}

func (e Event) Value(key string) any {
	switch key {
	case "seq":
		return e.Seq
	case "type":
		return string(e.Type)
	case "timestamp":
		return e.Timestamp
	default:
		if e.Payload == nil {
			return nil
		}
		return e.Payload[key]
	}
}

func (e Event) Map() map[string]any {
	data := cloneEventPayload(e.Payload)
	if data == nil {
		data = EventData{}
	}
	data["seq"] = e.Seq
	data["type"] = string(e.Type)
	data["timestamp"] = e.Timestamp
	return map[string]any(data)
}

func (e Event) Validate() error {
	if e.RunID() == "" {
		return fmt.Errorf("event runId is required")
	}
	if e.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if e.Timestamp <= 0 {
		return fmt.Errorf("event timestamp is required")
	}
	return nil
}

func (e Event) ValidatePersisted() error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Seq <= 0 {
		return fmt.Errorf("event seq is required")
	}
	return nil
}

func (e Event) String() string {
	return string(e.Type)
}

func (e Event) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	writeField := func(key string, value any) error {
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return err
		}
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(valueJSON)
		return nil
	}
	if err := writeField("seq", e.Seq); err != nil {
		return nil, err
	}
	if err := writeField("type", string(e.Type)); err != nil {
		return nil, err
	}
	payload := cloneEventPayload(e.Payload)
	keys := make([]string, 0, len(payload))
	for key := range payload {
		if key == "seq" || key == "type" || key == "timestamp" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writeField(key, payload[key]); err != nil {
			return nil, err
		}
	}
	if err := writeField("timestamp", e.Timestamp); err != nil {
		return nil, err
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = EventFromMap(raw)
	return nil
}

func cloneEventPayload(payload map[string]any) EventData {
	if payload == nil {
		return nil
	}
	out := make(EventData, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func int64Value(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

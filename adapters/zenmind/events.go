package zenmind

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

// StreamEvent is a platform wire event. Payload and Source remain available to
// old Go callers, but MarshalJSON emits the platform's flat envelope.
type StreamEvent struct {
	Seq       int64          `json:"-"`
	Type      string         `json:"-"`
	RunID     string         `json:"-"`
	Timestamp int64          `json:"-"`
	Payload   map[string]any `json:"-"`
	Source    string         `json:"-"`
}

func (e StreamEvent) MarshalJSON() ([]byte, error) {
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
	if err := writeField("type", e.Type); err != nil {
		return nil, err
	}
	payload := cloneMap(e.Payload)
	if e.Type == "content.delta" {
		delete(payload, "textDelta")
	}
	for _, key := range platformPayloadKeyOrder(e.Type) {
		value, ok := payload[key]
		if !ok || omitPlatformField(e.Type, key, value) {
			delete(payload, key)
			continue
		}
		if err := writeField(key, value); err != nil {
			return nil, err
		}
		delete(payload, key)
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if payload[key] == nil {
			continue
		}
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

func platformPayloadKeyOrder(eventType string) []string {
	switch eventType {
	case "run.start":
		return []string{"runId", "chatId", "agentKey"}
	case "run.complete":
		return []string{"runId", "finishReason", "usage"}
	case "run.cancel":
		return []string{"runId", "usage"}
	case "run.error":
		return []string{"runId", "error", "usage"}
	case "content.start":
		return []string{"contentId", "runId", "taskId"}
	case "content.delta":
		return []string{"contentId", "delta"}
	case "content.end":
		return []string{"contentId"}
	case "content.snapshot":
		return []string{"contentId", "runId", "text", "taskId"}
	case "tool.start":
		return []string{"toolId", "runId", "taskId", "toolName", "toolLabel", "toolDescription"}
	case "tool.args":
		return []string{"toolId", "delta", "chunkIndex"}
	case "tool.end":
		return []string{"toolId"}
	case "tool.snapshot":
		return []string{"toolId", "runId", "toolName", "taskId", "toolLabel", "toolDescription", "arguments"}
	case "tool.result":
		return []string{"toolId", "result", "hitl"}
	default:
		return nil
	}
}

func omitPlatformField(eventType, key string, value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) != "" {
		return false
	}
	switch eventType {
	case "content.start", "content.snapshot":
		return key == "taskId"
	case "tool.start", "tool.snapshot":
		return key == "taskId" || key == "toolName" || key == "toolLabel" || key == "toolDescription" || key == "arguments"
	default:
		return false
	}
}

// Mapper preserves the historical one-input/one-output Go API. Use Projector
// for platform streams; Mapper does not synthesize block lifecycle events.
type Mapper struct {
	Types map[zenforge.EventType]string
}

func NewMapper() Mapper {
	return Mapper{Types: DefaultEventTypes()}
}

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

func (m Mapper) Map(event zenforge.Event) StreamEvent {
	eventType := string(event.Type)
	if mapped, ok := m.Types[event.Type]; ok && mapped != "" {
		eventType = mapped
	}
	payload := cloneMap(event.Map())
	delete(payload, "seq")
	delete(payload, "type")
	delete(payload, "timestamp")
	if event.Type == zenforge.EventModelDelta {
		payload["delta"] = payload["textDelta"]
	}
	return StreamEvent{Seq: event.Seq, Type: eventType, RunID: event.RunID(), Timestamp: event.Timestamp, Payload: payload, Source: string(event.Type)}
}

func MapEvent(event zenforge.Event) StreamEvent {
	return NewMapper().Map(event)
}

func MapEvents(events []zenforge.Event) []StreamEvent {
	projector := NewProjector()
	var out []StreamEvent
	for _, event := range events {
		out = append(out, projector.Project(event)...)
	}
	return out
}

type toolProjection struct {
	name      string
	arguments string
}

// ProjectorIdentity supplies the request-scoped identity that is not present
// on most ZenForge events but is required by the platform run.start wire event.
type ProjectorIdentity struct {
	ChatID   string
	AgentKey string
}

// Projector turns one ZenForge run's durable events into the platform's live
// block protocol. Bookkeeping events without a lossless projection are
// deliberately ignored.
type Projector struct {
	identity      ProjectorIdentity
	nextSeq       int64
	terminated    bool
	contentSeq    map[string]int64
	activeContent string
	contentRunID  string
	contentText   strings.Builder
	openTools     map[string]toolProjection
}

// NewProjector preserves the original minimal-identity behavior. Prefer
// NewProjectorWithIdentity when producing a complete platform lifecycle.
func NewProjector() *Projector {
	return NewProjectorWithIdentity(ProjectorIdentity{})

}

func NewProjectorWithIdentity(identity ProjectorIdentity) *Projector {
	return &Projector{identity: identity, contentSeq: map[string]int64{}, openTools: map[string]toolProjection{}}
}

func (p *Projector) Project(event zenforge.Event) []StreamEvent {
	if p == nil || p.terminated {
		return nil
	}
	runID := event.RunID()
	var out []StreamEvent
	switch event.Type {
	case zenforge.EventRunStarted:
		chatID := p.identity.ChatID
		if chatID == "" {
			chatID = stringValue(event.Payload["chatId"])
		}
		agentKey := p.identity.AgentKey
		if agentKey == "" {
			agentKey = stringValue(event.Payload["agentKey"])
		}
		payload := map[string]any{"runId": runID}
		if chatID != "" {
			payload["chatId"] = chatID
		}
		if agentKey != "" {
			payload["agentKey"] = agentKey
		}
		out = append(out, p.event(event, "run.start", payload))
	case zenforge.EventModelDelta:
		out = append(out, p.closeTools(event)...)
		if p.activeContent == "" {
			p.contentSeq[runID]++
			p.activeContent = fmt.Sprintf("%s_c_%d", runID, p.contentSeq[runID])
			p.contentRunID = runID
			p.contentText.Reset()
			out = append(out, p.event(event, "content.start", map[string]any{"contentId": p.activeContent, "runId": runID}))
		}
		delta := stringValue(event.Payload["textDelta"])
		p.contentText.WriteString(delta)
		out = append(out, p.event(event, "content.delta", map[string]any{"contentId": p.activeContent, "delta": delta}))
	case zenforge.EventModelDone:
		out = append(out, p.closeContent(event)...)
	case zenforge.EventToolCall:
		out = append(out, p.closeContent(event)...)
		toolID := stringValue(event.Payload["toolCallId"])
		if toolID == "" {
			return out
		}
		arguments := canonicalArguments(event.Payload["arguments"])
		name := stringValue(event.Payload["toolName"])
		tool, open := p.openTools[toolID]
		if !open {
			p.openTools[toolID] = toolProjection{name: name, arguments: arguments}
			out = append(out, p.event(event, "tool.start", map[string]any{"toolId": toolID, "runId": runID, "toolName": name}))
		} else {
			tool.arguments += arguments
			p.openTools[toolID] = tool
		}
		out = append(out, p.event(event, "tool.args", map[string]any{"toolId": toolID, "delta": arguments, "chunkIndex": 0}))
	case zenforge.EventToolResult, zenforge.EventToolError:
		toolID := stringValue(event.Payload["toolCallId"])
		out = append(out, p.closeTool(event, toolID)...)
		result := any(event.Payload["output"])
		if event.Type == zenforge.EventToolError || stringValue(event.Payload["error"]) != "" || intValue(event.Payload["exitCode"]) != 0 {
			value := map[string]any{"output": event.Payload["output"]}
			if code := intValue(event.Payload["exitCode"]); code != 0 {
				value["exitCode"] = code
			}
			if message := stringValue(event.Payload["error"]); message != "" {
				value["error"] = message
			}
			result = value
		}
		out = append(out, p.event(event, "tool.result", map[string]any{"toolId": toolID, "result": result}))
	case zenforge.EventRunDone:
		out = append(out, p.closeContent(event)...)
		out = append(out, p.closeTools(event)...)
		out = append(out, p.event(event, "run.complete", map[string]any{"runId": runID, "finishReason": "stop"}))
		p.terminated = true
	case zenforge.EventRunError:
		out = append(out, p.closeContent(event)...)
		out = append(out, p.closeTools(event)...)
		out = append(out, p.event(event, "run.error", map[string]any{"runId": runID, "error": platformError(event.Payload["error"])}))
		p.terminated = true
	case zenforge.EventRunCancelled:
		out = append(out, p.closeContent(event)...)
		out = append(out, p.closeTools(event)...)
		out = append(out, p.event(event, "run.cancel", map[string]any{"runId": runID}))
		p.terminated = true
		// run.resumed, step.*, workspace, checkpoint, and nested subtask events
		// have no direct platform wire equivalent. Plan, approval, and task
		// events are also ignored because ZenForge lacks the required platform
		// request context or typed payload; emitting their similarly named wire
		// events would be lossy.
	}
	return out
}

func (p *Projector) event(source zenforge.Event, eventType string, payload map[string]any) StreamEvent {
	p.nextSeq++
	return StreamEvent{Seq: p.nextSeq, Type: eventType, RunID: source.RunID(), Timestamp: source.Timestamp, Payload: payload, Source: string(source.Type)}
}

func (p *Projector) closeContent(source zenforge.Event) []StreamEvent {
	if p.activeContent == "" {
		return nil
	}
	id, runID, text := p.activeContent, p.contentRunID, p.contentText.String()
	p.activeContent, p.contentRunID = "", ""
	p.contentText.Reset()
	return []StreamEvent{
		p.event(source, "content.end", map[string]any{"contentId": id}),
		p.event(source, "content.snapshot", map[string]any{"contentId": id, "runId": runID, "text": text}),
	}
}

func (p *Projector) closeTool(source zenforge.Event, toolID string) []StreamEvent {
	tool, ok := p.openTools[toolID]
	if !ok {
		return nil
	}
	delete(p.openTools, toolID)
	return []StreamEvent{
		p.event(source, "tool.end", map[string]any{"toolId": toolID}),
		p.event(source, "tool.snapshot", map[string]any{"toolId": toolID, "runId": source.RunID(), "toolName": tool.name, "arguments": tool.arguments}),
	}
}

func (p *Projector) closeTools(source zenforge.Event) []StreamEvent {
	ids := make([]string, 0, len(p.openTools))
	for id := range p.openTools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []StreamEvent
	for _, id := range ids {
		out = append(out, p.closeTool(source, id)...)
	}
	return out
}

func canonicalArguments(value any) string {
	if value == nil {
		return ""
	}
	if raw, ok := value.(json.RawMessage); ok {
		var normalized any
		if json.Unmarshal(raw, &normalized) == nil {
			value = normalized
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func platformError(value any) map[string]any {
	if fields, ok := value.(map[string]any); ok {
		out := cloneMap(fields)
		if _, ok := out["code"]; !ok {
			out["code"] = "stream_failed"
		}
		if _, ok := out["message"]; !ok {
			out["message"] = ""
		}
		if _, ok := out["scope"]; !ok {
			out["scope"] = "run"
		}
		if _, ok := out["category"]; !ok {
			out["category"] = "runtime"
		}
		return out
	}
	return map[string]any{"code": "stream_failed", "message": stringValue(value), "scope": "run", "category": "runtime"}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

type SubmitPayload struct {
	RequestID string                  `json:"requestId"`
	Action    approval.DecisionAction `json:"action"`
	Scope     approval.DecisionScope  `json:"scope,omitempty"`
	Reason    string                  `json:"reason,omitempty"`
	Payload   map[string]any          `json:"payload,omitempty"`
}

func DecisionFromSubmit(payload SubmitPayload) (approval.Decision, error) {
	decision := approval.Decision{RequestID: payload.RequestID, Action: payload.Action, Scope: payload.Scope, Reason: payload.Reason, Payload: cloneMap(payload.Payload), DecidedAt: time.Now().UTC()}
	if decision.Scope == "" {
		decision.Scope = approval.ScopeOnce
	}
	if err := decision.Validate(); err != nil {
		return approval.Decision{}, err
	}
	return decision, nil
}

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

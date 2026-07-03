package zenmind

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

// AwaitingIDAllocator assigns the platform correlation ID for one real
// approval.requested event.
type AwaitingIDAllocator func(zenforge.Event, approval.Request) (string, error)

// ApprovalEventBridge translates ZenForge approval lifecycle events into
// ZenMind awaiting wire values. It owns correlation only, not transport or
// persistence.
type ApprovalEventBridge struct {
	context   PlatformRequestContext
	allocate  AwaitingIDAllocator
	timeout   int64
	pending   map[string]ApprovalEventCorrelation
	completed map[string]bool
}

type ApprovalEventCorrelation struct {
	Ask        AwaitingAsk     `json:"ask"`
	Binding    ApprovalBinding `json:"binding"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
}

// ApprovalEventBridgeSnapshot is serializable correlation state for process
// recovery. The trusted context and allocation policy must be supplied again.
type ApprovalEventBridgeSnapshot struct {
	Pending   map[string]ApprovalEventCorrelation `json:"pending,omitempty"`
	Completed map[string]bool                     `json:"completed,omitempty"`
}

func NewApprovalEventBridge(context PlatformRequestContext, allocate AwaitingIDAllocator, timeout int64) (*ApprovalEventBridge, error) {
	return NewApprovalEventBridgeFromSnapshot(context, allocate, timeout, ApprovalEventBridgeSnapshot{})
}

func NewApprovalEventBridgeFromSnapshot(context PlatformRequestContext, allocate AwaitingIDAllocator, timeout int64, snapshot ApprovalEventBridgeSnapshot) (*ApprovalEventBridge, error) {
	context = PlatformRequestContext{
		RequestID: strings.TrimSpace(context.RequestID),
		ChatID:    strings.TrimSpace(context.ChatID),
		AgentKey:  strings.TrimSpace(context.AgentKey),
	}
	if context.RequestID == "" || context.ChatID == "" || context.AgentKey == "" {
		return nil, fmt.Errorf("platform context requires requestId, chatId, and agentKey")
	}
	if allocate == nil {
		return nil, fmt.Errorf("awaiting id allocator is required")
	}
	if timeout < 0 {
		return nil, fmt.Errorf("timeout must be zero or greater")
	}
	b := &ApprovalEventBridge{
		context: context, allocate: allocate, timeout: timeout,
		pending:   make(map[string]ApprovalEventCorrelation, len(snapshot.Pending)),
		completed: make(map[string]bool, len(snapshot.Completed)),
	}
	awaitingIDs := make(map[string]string, len(snapshot.Pending))
	for requestID, correlation := range snapshot.Pending {
		if requestID == "" || len(correlation.Ask.Approvals) != 1 ||
			requestID != correlation.Ask.Approvals[0].ID ||
			strings.TrimSpace(correlation.ToolCallID) == "" ||
			strings.TrimSpace(correlation.ToolName) == "" {
			return nil, fmt.Errorf("invalid pending approval correlation %q", requestID)
		}
		ask, err := BindAwaitingAsk(correlation.Ask, correlation.Binding)
		if err != nil {
			return nil, fmt.Errorf("invalid pending approval correlation %q: %w", requestID, err)
		}
		if ask.Binding.RequestID != context.RequestID || ask.Binding.ChatID != context.ChatID ||
			ask.Binding.AgentKey != context.AgentKey {
			return nil, fmt.Errorf("pending approval correlation %q does not match platform context", requestID)
		}
		if prior, exists := awaitingIDs[ask.AwaitingID]; exists {
			return nil, fmt.Errorf("pending approvals %q and %q share awaiting id %q", prior, requestID, ask.AwaitingID)
		}
		awaitingIDs[ask.AwaitingID] = requestID
		b.pending[requestID] = ApprovalEventCorrelation{
			Ask: cloneAwaitingAsk(ask), Binding: ask.Binding,
			ToolCallID: correlation.ToolCallID, ToolName: correlation.ToolName,
		}
	}
	for requestID, done := range snapshot.Completed {
		if strings.TrimSpace(requestID) == "" || !done {
			return nil, fmt.Errorf("invalid completed approval correlation %q", requestID)
		}
		if _, exists := b.pending[requestID]; exists {
			return nil, fmt.Errorf("approval correlation %q is both pending and completed", requestID)
		}
		b.completed[requestID] = true
	}
	return b, nil
}

// Handle returns either AwaitingAsk or AwaitingAnswer.
func (b *ApprovalEventBridge) Handle(event zenforge.Event) (any, error) {
	if b == nil {
		return nil, fmt.Errorf("approval event bridge is nil")
	}
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("invalid approval event: %w", err)
	}
	switch event.Type {
	case zenforge.EventApprovalRequested:
		return b.handleRequested(event)
	case zenforge.EventApprovalResolved:
		return b.handleResolved(event)
	case zenforge.EventApprovalExpired:
		return b.handleExpired(event)
	default:
		return nil, fmt.Errorf("unsupported approval event type %q", event.Type)
	}
}

func (b *ApprovalEventBridge) Snapshot() ApprovalEventBridgeSnapshot {
	if b == nil {
		return ApprovalEventBridgeSnapshot{}
	}
	state := ApprovalEventBridgeSnapshot{
		Pending:   make(map[string]ApprovalEventCorrelation, len(b.pending)),
		Completed: make(map[string]bool, len(b.completed)),
	}
	for id, correlation := range b.pending {
		correlation.Ask = cloneAwaitingAsk(correlation.Ask)
		state.Pending[id] = correlation
	}
	for id := range b.completed {
		state.Completed[id] = true
	}
	return state
}

type approvalRequestedPayload struct {
	RunID      string             `json:"runId"`
	RequestID  string             `json:"requestId"`
	ToolCallID string             `json:"toolCallId"`
	ToolName   string             `json:"toolName"`
	Operation  string             `json:"operation"`
	Risk       approval.RiskLevel `json:"risk"`
	Request    approval.Request   `json:"request"`
	Resumed    bool               `json:"resumed,omitempty"`
}

type approvalTerminalPayload struct {
	RunID      string                  `json:"runId"`
	RequestID  string                  `json:"requestId"`
	ToolCallID string                  `json:"toolCallId"`
	ToolName   string                  `json:"toolName"`
	Action     approval.DecisionAction `json:"action"`
	Scope      approval.DecisionScope  `json:"scope"`
	Reason     string                  `json:"reason"`
	Resumed    bool                    `json:"resumed,omitempty"`
	Reused     bool                    `json:"reused,omitempty"`
}

func (b *ApprovalEventBridge) handleRequested(event zenforge.Event) (AwaitingAsk, error) {
	var payload approvalRequestedPayload
	if err := decodeApprovalPayload(event.Payload, &payload); err != nil {
		return AwaitingAsk{}, fmt.Errorf("invalid approval.requested payload: %w", err)
	}
	req := payload.Request
	if err := validateRequestedIdentity(event, payload); err != nil {
		return AwaitingAsk{}, err
	}
	if existing, exists := b.pending[req.ID]; exists {
		if !payload.Resumed {
			return AwaitingAsk{}, fmt.Errorf("duplicate approval request %q", req.ID)
		}
		replayed, err := AwaitingAskFromRequestContext(
			req, existing.Ask.AwaitingID, b.context, existing.Ask.Timeout)
		if err != nil {
			return AwaitingAsk{}, err
		}
		if existing.ToolCallID != req.ToolCallID || existing.ToolName != req.ToolName ||
			!reflect.DeepEqual(replayed, existing.Ask) {
			return AwaitingAsk{}, fmt.Errorf("resumed approval request %q does not match correlation", req.ID)
		}
		return cloneAwaitingAsk(existing.Ask), nil
	}
	if b.completed[req.ID] {
		return AwaitingAsk{}, fmt.Errorf("duplicate approval request %q", req.ID)
	}
	awaitingID, err := b.allocate(event, req)
	if err != nil {
		return AwaitingAsk{}, fmt.Errorf("allocate awaiting id: %w", err)
	}
	for requestID, correlation := range b.pending {
		if strings.TrimSpace(awaitingID) == correlation.Ask.AwaitingID {
			return AwaitingAsk{}, fmt.Errorf("awaiting id %q is already assigned to approval %q", awaitingID, requestID)
		}
	}
	ask, err := AwaitingAskFromRequestContext(req, awaitingID, b.context, b.timeout)
	if err != nil {
		return AwaitingAsk{}, err
	}
	b.pending[req.ID] = ApprovalEventCorrelation{
		Ask: cloneAwaitingAsk(ask), Binding: ask.Binding, ToolCallID: req.ToolCallID, ToolName: req.ToolName,
	}
	return cloneAwaitingAsk(ask), nil
}

func (b *ApprovalEventBridge) handleResolved(event zenforge.Event) (any, error) {
	var payload approvalTerminalPayload
	if err := decodeApprovalPayload(event.Payload, &payload); err != nil {
		return AwaitingAnswer{}, fmt.Errorf("invalid approval.resolved payload: %w", err)
	}
	if payload.Reused {
		if strings.TrimSpace(payload.RequestID) == "" || strings.TrimSpace(payload.ToolCallID) == "" ||
			strings.TrimSpace(payload.ToolName) == "" || payload.RunID != event.RunID() {
			return AwaitingAnswer{}, fmt.Errorf("reused approval.resolved payload has invalid identity")
		}
		if _, pending := b.pending[payload.RequestID]; pending {
			return AwaitingAnswer{}, fmt.Errorf("reused approval %q unexpectedly has pending correlation", payload.RequestID)
		}
		if b.completed[payload.RequestID] {
			return AwaitingAnswer{}, fmt.Errorf("duplicate terminal approval event for %q", payload.RequestID)
		}
		b.completed[payload.RequestID] = true
		return nil, nil
	}
	correlation, err := b.terminalCorrelation(event, payload)
	if err != nil {
		return AwaitingAnswer{}, err
	}
	decision := approval.Decision{
		RequestID: payload.RequestID, Action: payload.Action, Scope: payload.Scope,
		Reason: payload.Reason, DecidedAt: time.UnixMilli(event.Timestamp).UTC(),
	}
	param, err := DecisionToPlatform(decision)
	if err != nil {
		return AwaitingAnswer{}, fmt.Errorf("invalid approval.resolved decision: %w", err)
	}
	answer := AwaitingAnswer{
		Type: "awaiting.answer", AwaitingID: correlation.Ask.AwaitingID,
		Mode: PlatformApprovalMode, Status: PlatformStatusAnswered,
		Approvals: []ApprovalParam{param},
	}
	b.finish(payload.RequestID)
	return answer, nil
}

func (b *ApprovalEventBridge) handleExpired(event zenforge.Event) (AwaitingAnswer, error) {
	var payload approvalTerminalPayload
	if err := decodeApprovalPayload(event.Payload, &payload); err != nil {
		return AwaitingAnswer{}, fmt.Errorf("invalid approval.expired payload: %w", err)
	}
	correlation, err := b.terminalCorrelation(event, payload)
	if err != nil {
		return AwaitingAnswer{}, err
	}
	if payload.Action != approval.DecisionReject || payload.Reason != approval.ErrorExpired {
		return AwaitingAnswer{}, fmt.Errorf("approval.expired requires reject action and %q reason", approval.ErrorExpired)
	}
	answer, err := AwaitingErrorAnswer(correlation.Ask, "", PlatformErrorTimeout, approval.ErrorExpired)
	if err != nil {
		return AwaitingAnswer{}, err
	}
	b.finish(payload.RequestID)
	return answer, nil
}

func (b *ApprovalEventBridge) terminalCorrelation(event zenforge.Event, payload approvalTerminalPayload) (ApprovalEventCorrelation, error) {
	if strings.TrimSpace(payload.RequestID) == "" || strings.TrimSpace(payload.ToolCallID) == "" ||
		strings.TrimSpace(payload.ToolName) == "" {
		return ApprovalEventCorrelation{}, fmt.Errorf("%s payload requires requestId, toolCallId, and toolName", event.Type)
	}
	if payload.RunID != event.RunID() {
		return ApprovalEventCorrelation{}, fmt.Errorf("%s runId does not match event", event.Type)
	}
	if b.completed[payload.RequestID] {
		return ApprovalEventCorrelation{}, fmt.Errorf("duplicate terminal approval event for %q", payload.RequestID)
	}
	correlation, ok := b.pending[payload.RequestID]
	if !ok {
		return ApprovalEventCorrelation{}, fmt.Errorf("unknown approval request %q", payload.RequestID)
	}
	if correlation.Ask.RunID != payload.RunID {
		return ApprovalEventCorrelation{}, fmt.Errorf("approval %q runId does not match correlation", payload.RequestID)
	}
	if correlation.ToolCallID != payload.ToolCallID || correlation.ToolName != payload.ToolName {
		return ApprovalEventCorrelation{}, fmt.Errorf("approval %q tool identity does not match correlation", payload.RequestID)
	}
	return correlation, nil
}

func (b *ApprovalEventBridge) finish(requestID string) {
	delete(b.pending, requestID)
	b.completed[requestID] = true
}

func validateRequestedIdentity(event zenforge.Event, payload approvalRequestedPayload) error {
	req := payload.Request
	if strings.TrimSpace(payload.RequestID) == "" || strings.TrimSpace(payload.ToolCallID) == "" ||
		strings.TrimSpace(payload.ToolName) == "" || strings.TrimSpace(payload.Operation) == "" ||
		payload.Risk == "" {
		return fmt.Errorf("approval.requested payload is missing required identity fields")
	}
	if payload.RunID != event.RunID() || req.RunID != event.RunID() {
		return fmt.Errorf("approval.requested runId does not match event")
	}
	if payload.RequestID != req.ID || payload.ToolCallID != req.ToolCallID ||
		payload.ToolName != req.ToolName || payload.Operation != req.Operation || payload.Risk != req.Risk {
		return fmt.Errorf("approval.requested outer fields do not match request")
	}
	return nil
}

func decodeApprovalPayload(payload zenforge.EventData, dst any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("payload contains trailing JSON")
	}
	return nil
}

func cloneAwaitingAsk(in AwaitingAsk) AwaitingAsk {
	out := in
	out.Approvals = make([]ApprovalAsk, len(in.Approvals))
	for i, item := range in.Approvals {
		out.Approvals[i] = item
		out.Approvals[i].Options = append([]ApprovalOption(nil), item.Options...)
	}
	return out
}

package zenmind

import (
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

const (
	PlatformApprovalMode   = "approval"
	PlatformStatusAnswered = "answered"
	PlatformStatusError    = "error"
	PlatformErrorTimeout   = "timeout"
)

type PlatformApprovalDecision string

const (
	PlatformDecisionApprove        PlatformApprovalDecision = "approve"
	PlatformDecisionApproveRuleRun PlatformApprovalDecision = "approve_rule_run"
	PlatformDecisionReject         PlatformApprovalDecision = "reject"
)

// AwaitingAsk is the platform awaiting.ask wire payload for approval mode.
type AwaitingAsk struct {
	Type         string        `json:"type"`
	AwaitingID   string        `json:"awaitingId"`
	Mode         string        `json:"mode"`
	Timeout      int64         `json:"timeout"`
	RunID        string        `json:"runId"`
	AgentKey     string        `json:"agentKey,omitempty"`
	ViewportType string        `json:"viewportType,omitempty"`
	ViewportKey  string        `json:"viewportKey,omitempty"`
	Approvals    []ApprovalAsk `json:"approvals"`
}

type ApprovalAsk struct {
	ID                  string           `json:"id"`
	Command             string           `json:"command"`
	Description         string           `json:"description,omitempty"`
	Options             []ApprovalOption `json:"options"`
	AllowFreeText       bool             `json:"allowFreeText"`
	FreeTextPlaceholder string           `json:"freeTextPlaceholder,omitempty"`
}

type ApprovalOption struct {
	Label    string                   `json:"label"`
	Decision PlatformApprovalDecision `json:"decision"`
}

// RequestSubmit is the platform request.submit wire payload.
type RequestSubmit struct {
	Type       string          `json:"type"`
	RequestID  string          `json:"requestId"`
	ChatID     string          `json:"chatId"`
	RunID      string          `json:"runId"`
	AwaitingID string          `json:"awaitingId"`
	SubmitID   string          `json:"submitId"`
	Params     []ApprovalParam `json:"params"`
}

type ApprovalParam struct {
	ID       string                   `json:"id"`
	Decision PlatformApprovalDecision `json:"decision"`
	Reason   string                   `json:"reason,omitempty"`
}

// AwaitingAnswer is the platform awaiting.answer wire payload for approval mode.
type AwaitingAnswer struct {
	Type       string          `json:"type"`
	AwaitingID string          `json:"awaitingId"`
	Mode       string          `json:"mode"`
	SubmitID   string          `json:"submitId,omitempty"`
	Status     string          `json:"status"`
	Approvals  []ApprovalParam `json:"approvals,omitempty"`
	Error      *AwaitingError  `json:"error,omitempty"`
}

type AwaitingError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PlatformRequestContext carries identity supplied by the platform stream
// dispatcher rather than by an individual AwaitAsk input.
type PlatformRequestContext struct {
	AgentKey string
}

// AwaitingAskFromRequest is retained for callers that do not yet have platform
// dispatcher context. The returned legacy payload must be completed with an
// AgentKey before Validate or any submit/answer translation is attempted.
func AwaitingAskFromRequest(req approval.Request, awaitingID string, timeout int64) (AwaitingAsk, error) {
	return awaitingAskFromRequest(req, awaitingID, PlatformRequestContext{}, timeout, false)
}

// AwaitingAskFromRequestContext constructs a complete platform awaiting.ask
// payload, including identity injected from the dispatcher request context.
func AwaitingAskFromRequestContext(req approval.Request, awaitingID string, context PlatformRequestContext, timeout int64) (AwaitingAsk, error) {
	return awaitingAskFromRequest(req, awaitingID, context, timeout, true)
}

func awaitingAskFromRequest(req approval.Request, awaitingID string, context PlatformRequestContext, timeout int64, requireContext bool) (AwaitingAsk, error) {
	if err := req.Validate(); err != nil {
		return AwaitingAsk{}, fmt.Errorf("invalid approval request: %w", err)
	}
	if strings.TrimSpace(awaitingID) == "" {
		return AwaitingAsk{}, fmt.Errorf("awaiting id is required")
	}
	options := make([]ApprovalOption, 0, len(req.Options))
	for i, option := range req.Options {
		decision, err := platformDecision(option.Action, option.Scope)
		if err != nil {
			return AwaitingAsk{}, fmt.Errorf("approval option %d: %w", i, err)
		}
		options = append(options, ApprovalOption{Label: option.Label, Decision: decision})
	}
	ask := AwaitingAsk{
		Type: "awaiting.ask", AwaitingID: awaitingID, Mode: PlatformApprovalMode,
		Timeout: timeout, RunID: req.RunID, AgentKey: strings.TrimSpace(context.AgentKey),
		ViewportType: "builtin", ViewportKey: "approval",
		Approvals: []ApprovalAsk{{
			ID: req.ID, Command: req.Title, Description: req.Description, Options: options,
			AllowFreeText: true, FreeTextPlaceholder: "拒绝，请告知如何调整",
		}},
	}
	if requireContext {
		return ask, ask.Validate()
	}
	return ask, ask.validate(false)
}

func (a AwaitingAsk) Validate() error {
	return a.validate(true)
}

func (a AwaitingAsk) validate(requireAgentKey bool) error {
	if a.Type != "awaiting.ask" || a.Mode != PlatformApprovalMode {
		return fmt.Errorf("awaiting ask must be type %q and mode %q", "awaiting.ask", PlatformApprovalMode)
	}
	if strings.TrimSpace(a.AwaitingID) == "" || strings.TrimSpace(a.RunID) == "" {
		return fmt.Errorf("awaitingId and runId are required")
	}
	if requireAgentKey && strings.TrimSpace(a.AgentKey) == "" {
		return fmt.Errorf("agentKey is required")
	}
	if a.Timeout < 0 {
		return fmt.Errorf("timeout must be zero or greater")
	}
	if len(a.Approvals) == 0 {
		return fmt.Errorf("at least one approval is required")
	}
	seen := make(map[string]struct{}, len(a.Approvals))
	for i, item := range a.Approvals {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return fmt.Errorf("approvals[%d]: id is required", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("approvals[%d]: duplicate id %q", i, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// DecisionFromRequestSubmit binds one platform submit to its ask. It does not
// use DecisionFromJSON, whose neutral payload is retained only for legacy users.
func DecisionFromRequestSubmit(ask AwaitingAsk, submit RequestSubmit, decidedAt time.Time) (approval.Decision, error) {
	if err := validateSubmitIdentity(ask, submit); err != nil {
		return approval.Decision{}, err
	}
	if len(ask.Approvals) != 1 {
		return approval.Decision{}, fmt.Errorf("single decision requires exactly one asked approval, got %d", len(ask.Approvals))
	}
	if err := validateApprovalParams(ask.Approvals, submit.Params); err != nil {
		return approval.Decision{}, err
	}
	return DecisionFromPlatform(submit.Params[0], decidedAt)
}

func DecisionFromPlatform(param ApprovalParam, decidedAt time.Time) (approval.Decision, error) {
	action, scope, err := zenforgeDecision(param.Decision)
	if err != nil {
		return approval.Decision{}, err
	}
	decision := approval.Decision{
		RequestID: strings.TrimSpace(param.ID), Action: action, Scope: scope,
		Reason: param.Reason, DecidedAt: decidedAt.UTC(),
	}
	if err := decision.Validate(); err != nil {
		return approval.Decision{}, fmt.Errorf("invalid platform approval param: %w", err)
	}
	return decision, nil
}

func DecisionToPlatform(decision approval.Decision) (ApprovalParam, error) {
	if err := decision.Validate(); err != nil {
		return ApprovalParam{}, err
	}
	if len(decision.Payload) != 0 {
		return ApprovalParam{}, fmt.Errorf("platform approval param cannot carry decision payload")
	}
	translated, err := platformDecision(decision.Action, decision.Scope)
	if err != nil {
		return ApprovalParam{}, err
	}
	return ApprovalParam{ID: decision.RequestID, Decision: translated, Reason: decision.Reason}, nil
}

func AwaitingAnswerFromDecision(ask AwaitingAsk, submit RequestSubmit, decision approval.Decision) (AwaitingAnswer, error) {
	if _, err := DecisionFromRequestSubmit(ask, submit, decision.DecidedAt); err != nil {
		return AwaitingAnswer{}, err
	}
	param, err := DecisionToPlatform(decision)
	if err != nil {
		return AwaitingAnswer{}, err
	}
	if param != submit.Params[0] {
		return AwaitingAnswer{}, fmt.Errorf("decision does not match submitted approval")
	}
	return AwaitingAnswer{
		Type: "awaiting.answer", AwaitingID: ask.AwaitingID, Mode: PlatformApprovalMode,
		SubmitID: submit.SubmitID, Status: PlatformStatusAnswered, Approvals: []ApprovalParam{param},
	}, nil
}

func AwaitingErrorAnswer(ask AwaitingAsk, submitID, code, message string) (AwaitingAnswer, error) {
	if err := ask.Validate(); err != nil {
		return AwaitingAnswer{}, err
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(message) == "" {
		return AwaitingAnswer{}, fmt.Errorf("awaiting error code and message are required")
	}
	return AwaitingAnswer{
		Type: "awaiting.answer", AwaitingID: ask.AwaitingID, Mode: PlatformApprovalMode,
		SubmitID: strings.TrimSpace(submitID), Status: PlatformStatusError,
		Error: &AwaitingError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)},
	}, nil
}

func validateSubmitIdentity(ask AwaitingAsk, submit RequestSubmit) error {
	if err := ask.Validate(); err != nil {
		return err
	}
	if submit.Type != "request.submit" {
		return fmt.Errorf("request submit type must be %q", "request.submit")
	}
	if strings.TrimSpace(submit.RequestID) == "" || strings.TrimSpace(submit.ChatID) == "" ||
		strings.TrimSpace(submit.RunID) == "" || strings.TrimSpace(submit.AwaitingID) == "" ||
		strings.TrimSpace(submit.SubmitID) == "" {
		return fmt.Errorf("requestId, chatId, runId, awaitingId, and submitId are required")
	}
	if submit.RunID != ask.RunID {
		return fmt.Errorf("submit runId %q does not match ask runId %q", submit.RunID, ask.RunID)
	}
	if submit.AwaitingID != ask.AwaitingID {
		return fmt.Errorf("submit awaitingId %q does not match ask awaitingId %q", submit.AwaitingID, ask.AwaitingID)
	}
	return nil
}

func validateApprovalParams(asked []ApprovalAsk, params []ApprovalParam) error {
	if len(params) != len(asked) {
		return fmt.Errorf("expected %d approval params, got %d", len(asked), len(params))
	}
	want := make(map[string]struct{}, len(asked))
	for _, item := range asked {
		want[item.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(params))
	for i, param := range params {
		id := strings.TrimSpace(param.ID)
		if id == "" {
			return fmt.Errorf("params[%d]: approval id is required", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("params[%d]: duplicate approval id %q", i, id)
		}
		seen[id] = struct{}{}
		if _, ok := want[id]; !ok {
			return fmt.Errorf("params[%d]: unknown approval id %q", i, id)
		}
		if _, _, err := zenforgeDecision(param.Decision); err != nil {
			return fmt.Errorf("params[%d]: %w", i, err)
		}
	}
	return nil
}

func platformDecision(action approval.DecisionAction, scope approval.DecisionScope) (PlatformApprovalDecision, error) {
	switch action {
	case approval.DecisionApprove:
		switch scope {
		case approval.ScopeRun:
			return PlatformDecisionApprove, nil
		case approval.ScopeRule:
			return PlatformDecisionApproveRuleRun, nil
		default:
			return "", fmt.Errorf("platform approve requires zenforge scope run or rule, got %q", scope)
		}
	case approval.DecisionReject:
		if scope == "" || scope == approval.ScopeOnce {
			return PlatformDecisionReject, nil
		}
		return "", fmt.Errorf("platform reject requires zenforge scope once, got %q", scope)
	default:
		return "", fmt.Errorf("unsupported zenforge approval action %q", action)
	}
}

func zenforgeDecision(decision PlatformApprovalDecision) (approval.DecisionAction, approval.DecisionScope, error) {
	switch decision {
	case PlatformDecisionApprove:
		return approval.DecisionApprove, approval.ScopeRun, nil
	case PlatformDecisionApproveRuleRun:
		return approval.DecisionApprove, approval.ScopeRule, nil
	case PlatformDecisionReject:
		return approval.DecisionReject, approval.ScopeOnce, nil
	default:
		return "", "", fmt.Errorf("unsupported platform approval decision %q", decision)
	}
}

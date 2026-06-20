package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/tool"
)

const (
	ErrorRequired = "approval_required"
	ErrorRejected = "approval_rejected"
	ErrorExpired  = "approval_expired"
	ReasonReused  = "approval_scope_reused"
)

var (
	ErrRequired = errors.New(ErrorRequired)
	ErrAborted  = errors.New("approval aborted")
)

// AbortError signals that an operator aborted the run rather than merely
// rejecting one operation.
type AbortError struct {
	Reason string
}

func (e *AbortError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrAborted.Error()
	}
	return ErrAborted.Error() + ": " + e.Reason
}

func (e *AbortError) Unwrap() error {
	return context.Canceled
}

func (e *AbortError) Is(target error) bool {
	return target == ErrAborted
}

func NewAbortError(reason string) error {
	return &AbortError{Reason: reason}
}

const (
	MetadataDecisionAction = "approval.decisionAction"
	MetadataFingerprint    = "approval.fingerprint"
	MetadataRequestID      = "approval.requestId"
	MetadataRuleKey        = "approval.ruleKey"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type DecisionAction string

const (
	DecisionApprove DecisionAction = "approve"
	DecisionReject  DecisionAction = "reject"
	DecisionAlways  DecisionAction = "always"
	DecisionAbort   DecisionAction = "abort"
)

type DecisionScope string

const (
	ScopeOnce DecisionScope = "once"
	ScopeRun  DecisionScope = "run"
	ScopeRule DecisionScope = "rule"
)

type Option struct {
	Action      DecisionAction `json:"action"`
	Label       string         `json:"label"`
	Description string         `json:"description,omitempty"`
	Scope       DecisionScope  `json:"scope,omitempty"`
}

type Request struct {
	ID          string         `json:"id"`
	RunID       string         `json:"runId"`
	ToolCallID  string         `json:"toolCallId,omitempty"`
	ToolName    string         `json:"toolName,omitempty"`
	Operation   string         `json:"operation"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Risk        RiskLevel      `json:"risk"`
	Options     []Option       `json:"options"`
	Payload     map[string]any `json:"payload,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
}

type Decision struct {
	RequestID string         `json:"requestId"`
	Action    DecisionAction `json:"action"`
	Scope     DecisionScope  `json:"scope,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	DecidedAt time.Time      `json:"decidedAt"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("approval request id is required")
	}
	if strings.TrimSpace(r.RunID) == "" {
		return fmt.Errorf("approval request run id is required")
	}
	if strings.TrimSpace(r.Operation) == "" {
		return fmt.Errorf("approval request operation is required")
	}
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("approval request title is required")
	}
	if !ValidRisk(r.Risk) {
		if r.Risk == "" {
			return fmt.Errorf("approval request risk is required")
		}
		return fmt.Errorf("unsupported approval request risk %q", r.Risk)
	}
	if len(r.Options) == 0 {
		return fmt.Errorf("approval request options are required")
	}
	for i, option := range r.Options {
		if strings.TrimSpace(option.Label) == "" {
			return fmt.Errorf("approval request option %d label is required", i)
		}
		if err := (Decision{RequestID: r.ID, Action: option.Action, Scope: option.Scope}).Validate(); err != nil {
			return fmt.Errorf("approval request option %d: %w", i, err)
		}
	}
	if r.ExpiresAt != nil && !r.CreatedAt.IsZero() && r.ExpiresAt.Before(r.CreatedAt) {
		return fmt.Errorf("approval request expiresAt cannot be before createdAt")
	}
	return nil
}

func ValidRisk(risk RiskLevel) bool {
	switch risk {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

func (d Decision) Validate() error {
	if strings.TrimSpace(d.RequestID) == "" {
		return fmt.Errorf("approval decision request id is required")
	}
	switch d.Action {
	case DecisionApprove, DecisionReject, DecisionAlways, DecisionAbort:
	default:
		if d.Action == "" {
			return fmt.Errorf("approval decision action is required")
		}
		return fmt.Errorf("unsupported approval decision action %q", d.Action)
	}
	switch d.Scope {
	case "", ScopeOnce, ScopeRun, ScopeRule:
	default:
		return fmt.Errorf("unsupported approval decision scope %q", d.Scope)
	}
	return nil
}

// ValidateDecisionForRequest binds a broker decision to the exact request and
// validates any requested reusable scope before execution can continue.
func ValidateDecisionForRequest(req Request, decision Decision) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	if decision.RequestID != req.ID {
		return fmt.Errorf("approval decision request id %q does not match %q", decision.RequestID, req.ID)
	}
	if IsApprovedAction(decision.Action) && decision.Scope != "" && decision.Scope != ScopeOnce {
		if _, err := ScopeKey(req, decision.Scope); err != nil {
			return err
		}
	}
	return nil
}

func DefaultOptions() []Option {
	return []Option{
		{Action: DecisionApprove, Label: "Approve", Scope: ScopeOnce},
		{Action: DecisionReject, Label: "Reject", Scope: ScopeOnce},
	}
}

// BindRequest replaces tool-provided routing fields with the active runtime
// identity before the request crosses a persistence or broker boundary.
func BindRequest(req Request, runID, toolCallID, toolName string) Request {
	req = cloneRequest(req)
	req.RunID = runID
	req.ToolCallID = toolCallID
	req.ToolName = toolName
	if strings.TrimSpace(req.ID) == "" {
		req.ID = NewRequestID(runID, toolCallID, req.Operation)
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	return req
}

func NewRequestID(runID, toolCallID, operation string) string {
	base := runID
	if toolCallID != "" {
		base += "_" + toolCallID
	}
	if operation != "" {
		base += "_" + operation
	}
	if base == "" {
		base = "request"
	}
	return fmt.Sprintf("approval_%s_%d", base, time.Now().UTC().UnixNano())
}

func RequiredResult(req Request) tool.Result {
	return tool.Result{
		Error:    ErrorRequired,
		ExitCode: 1,
		Structured: map[string]any{
			"approval": req,
		},
	}
}

func ApprovedMetadata(metadata map[string]any, req Request, decision Decision) map[string]any {
	out := cloneMetadata(metadata)
	out[MetadataRequestID] = req.ID
	out[MetadataDecisionAction] = string(decision.Action)
	if fingerprint, ok := stringFromPayload(req.Payload, "fingerprint"); ok {
		out[MetadataFingerprint] = fingerprint
	}
	if ruleKey, ok := stringFromPayload(req.Payload, "ruleKey"); ok {
		out[MetadataRuleKey] = ruleKey
	}
	return out
}

func ScopeKey(req Request, scope DecisionScope) (string, error) {
	switch scope {
	case "", ScopeOnce:
		return "", nil
	case ScopeRun:
		if fingerprint, ok := stringFromPayload(req.Payload, "fingerprint"); ok && strings.TrimSpace(fingerprint) != "" {
			return fingerprint, nil
		}
		return "", fmt.Errorf("approval run scope requires request fingerprint")
	case ScopeRule:
		if ruleKey, ok := stringFromPayload(req.Payload, "ruleKey"); ok && strings.TrimSpace(ruleKey) != "" {
			return ruleKey, nil
		}
		return "", fmt.Errorf("approval rule scope requires request ruleKey")
	default:
		return "", fmt.Errorf("unsupported approval scope %q", scope)
	}
}

func IsApprovedAction(action any) bool {
	switch action {
	case string(DecisionApprove), string(DecisionAlways), DecisionApprove, DecisionAlways:
		return true
	default:
		return false
	}
}

func MatchesApprovedMetadata(metadata map[string]any, fingerprint, ruleKey string) bool {
	if metadata == nil || !IsApprovedAction(metadata[MetadataDecisionAction]) {
		return false
	}
	if approved, _ := metadata[MetadataFingerprint].(string); fingerprint != "" && approved == fingerprint {
		return true
	}
	if approved, _ := metadata[MetadataRuleKey].(string); ruleKey != "" && approved == ruleKey {
		return true
	}
	return false
}

func RequestFromResult(result tool.Result) (Request, bool) {
	if result.Error != ErrorRequired || result.Structured == nil {
		return Request{}, false
	}
	value, ok := result.Structured["approval"]
	if !ok {
		return Request{}, false
	}
	switch req := value.(type) {
	case Request:
		return cloneRequest(req), true
	case *Request:
		if req == nil {
			return Request{}, false
		}
		return cloneRequest(*req), true
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return Request{}, false
		}
		var decoded Request
		if err := json.Unmarshal(data, &decoded); err != nil {
			return Request{}, false
		}
		return cloneRequest(decoded), true
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata)+4)
	for key, value := range metadata {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneRequest(req Request) Request {
	req.Options = append([]Option(nil), req.Options...)
	if req.Payload != nil {
		req.Payload = cloneMetadata(req.Payload)
	}
	if req.ExpiresAt != nil {
		expiresAt := *req.ExpiresAt
		req.ExpiresAt = &expiresAt
	}
	return req
}

func cloneDecision(decision Decision) Decision {
	if decision.Payload != nil {
		decision.Payload = cloneMetadata(decision.Payload)
	}
	return decision
}

func cloneValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMetadata(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), value...)
	case []map[string]any:
		out := make([]map[string]any, len(value))
		for i, item := range value {
			out[i] = cloneMetadata(item)
		}
		return out
	default:
		return value
	}
}

func stringFromPayload(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge/tool"
)

const (
	ErrorRequired = "approval_required"
	ErrorRejected = "approval_rejected"
	ErrorExpired  = "approval_expired"
	ReasonReused  = "approval_scope_reused"
)

var ErrRequired = errors.New(ErrorRequired)

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
	if r.ID == "" {
		return fmt.Errorf("approval request id is required")
	}
	if r.RunID == "" {
		return fmt.Errorf("approval request run id is required")
	}
	if r.Operation == "" {
		return fmt.Errorf("approval request operation is required")
	}
	if r.Title == "" {
		return fmt.Errorf("approval request title is required")
	}
	if r.Risk == "" {
		return fmt.Errorf("approval request risk is required")
	}
	if len(r.Options) == 0 {
		return fmt.Errorf("approval request options are required")
	}
	return nil
}

func (d Decision) Validate() error {
	if d.RequestID == "" {
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

func DefaultOptions() []Option {
	return []Option{
		{Action: DecisionApprove, Label: "Approve", Scope: ScopeOnce},
		{Action: DecisionReject, Label: "Reject", Scope: ScopeOnce},
	}
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
		if fingerprint, ok := stringFromPayload(req.Payload, "fingerprint"); ok && fingerprint != "" {
			return fingerprint, nil
		}
		return "", fmt.Errorf("approval run scope requires request fingerprint")
	case ScopeRule:
		if ruleKey, ok := stringFromPayload(req.Payload, "ruleKey"); ok && ruleKey != "" {
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
		return req, true
	case *Request:
		if req == nil {
			return Request{}, false
		}
		return *req, true
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return Request{}, false
		}
		var decoded Request
		if err := json.Unmarshal(data, &decoded); err != nil {
			return Request{}, false
		}
		return decoded, true
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata)+4)
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func stringFromPayload(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

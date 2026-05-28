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
)

var ErrRequired = errors.New(ErrorRequired)

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
	if d.Action == "" {
		return fmt.Errorf("approval decision action is required")
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

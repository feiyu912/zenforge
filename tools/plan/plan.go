// Package plan exposes the `exit_plan_mode` tool: in plan mode the model
// investigates read-only, then presents a plan whose approval ends the
// phase. The approval request carries the plan text, so a broker decision
// is made against exactly the plan the model wrote.
package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

// Name is the tool name.
const Name = "exit_plan_mode"

// Description is the model-facing description.
const Description = "Present the finished plan and request approval to leave plan mode. While plan mode is active, mutating tools (file writes, patches, shell, subagents) are refused; call this tool once the plan is complete."

type input struct {
	Plan string `json:"plan" jsonschema:"required,description=The complete implementation plan in markdown"`
}

type output struct {
	Plan     string `json:"plan"`
	Approved bool   `json:"approved"`
	Message  string `json:"message"`
}

// New builds the tool. It is read-only in the policy sense (it changes
// no workspace state) but is itself gated by the approval broker.
func New() (tool.Tool, error) {
	base, err := tools.New(Name, Description, func(ctx context.Context, in input, call tool.Context) (output, error) {
		planText := strings.TrimSpace(in.Plan)
		if planText == "" {
			return output{}, fmt.Errorf("%w: plan is required", tool.ErrInvalidArguments)
		}
		fingerprint := planFingerprint(planText)
		if planApproved(call.Metadata, fingerprint) {
			return output{
				Plan:     planText,
				Approved: true,
				Message:  "Plan approved. Plan mode is off; implement the plan and verify the result.",
			}, nil
		}
		payload := map[string]any{
			"operation":   "plan.approve",
			"plan":        planText,
			"fingerprint": fingerprint,
			"ruleKey":     Name,
		}
		request := approval.Request{
			ID:          approval.NewRequestID(call.RunID, call.ToolCallID, Name),
			RunID:       call.RunID,
			ToolCallID:  call.ToolCallID,
			ToolName:    Name,
			Operation:   "plan.approve",
			Title:       "Approve plan",
			Description: planSummary(planText),
			Risk:        approval.RiskMedium,
			Options:     approval.DefaultOptions(),
			Payload:     payload,
			CreatedAt:   time.Now().UTC(),
		}
		return output{}, requiredError(request)
	})
	if err != nil {
		return nil, err
	}
	return toolWithReadOnly{base: base}, nil
}

// toolWithReadOnly declares the tool read-only for plan-mode purposes.
type toolWithReadOnly struct{ base tool.Tool }

func (t toolWithReadOnly) Name() string           { return t.base.Name() }
func (t toolWithReadOnly) Description() string    { return t.base.Description() }
func (t toolWithReadOnly) Schema() map[string]any { return t.base.Schema() }

// ReadOnly reports that the tool mutates no workspace state.
func (t toolWithReadOnly) ReadOnly() bool { return true }

func (t toolWithReadOnly) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	result, err := t.base.Call(ctx, raw, call)
	if err == nil {
		return result, nil
	}
	var required *requiredApprovalError
	if errors.As(err, &required) {
		return approval.RequiredResult(required.request), approval.ErrRequired
	}
	return result, err
}

// requiredApprovalError carries the approval request out of the tools.New
// handler, which cannot return a tool.Result itself.
type requiredApprovalError struct{ request approval.Request }

func (e *requiredApprovalError) Error() string { return approval.ErrorRequired }

func requiredError(request approval.Request) error {
	return &requiredApprovalError{request: request}
}

// planApproved requires the approval to name this exact plan. The
// generic MatchesApprovedMetadata falls back to a rule-key match, which
// would let one approved plan authorize a different one; a plan approval
// is per-plan.
func planApproved(metadata map[string]any, fingerprint string) bool {
	if metadata == nil || !approval.IsApprovedAction(metadata[approval.MetadataDecisionAction]) {
		return false
	}
	approved, _ := metadata[approval.MetadataFingerprint].(string)
	return approved != "" && approved == fingerprint
}

// planFingerprint binds an approval to the exact plan text.
func planFingerprint(planText string) string {
	sum := sha256.Sum256([]byte(planText))
	return hex.EncodeToString(sum[:])
}

// planSummary is the one-line approval description: the plan's first
// non-empty line, bounded so a broker card stays readable.
func planSummary(planText string) string {
	for _, line := range strings.Split(planText, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#*-"))
		if line == "" {
			continue
		}
		if len(line) > 160 {
			line = line[:160] + "…"
		}
		return "plan: " + line
	}
	return "plan review"
}

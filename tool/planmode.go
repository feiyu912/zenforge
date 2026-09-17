package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Plan mode: a per-run collaboration phase in which mutating tools are
// refused until a plan is approved. The flag rides the run-state Meta
// map, so it is checkpointed with the run: a resumed run keeps whatever
// phase it reached, and approving a plan switches the run to executing
// durably.
const (
	// PlanModeMetadataKey is the tool-call metadata key carrying the
	// run's collaboration phase.
	PlanModeMetadataKey = "zenforge.plan.mode"
	// PlanModePlanning refuses mutating tools.
	PlanModePlanning = "planning"
	// PlanModeExecuting allows every tool.
	PlanModeExecuting = "executing"
	// PlanModeCode is the structured result code of a refusal.
	PlanModeCode = "PLAN_MODE_READ_ONLY"
)

// PlanModeGuidance tells the model how to leave plan mode.
const PlanModeGuidance = "Plan mode is active, so mutating tools are refused. Investigate read-only, then call exit_plan_mode with the finished plan to request approval before making changes."

// ErrPlanModeReadOnly is returned when a mutating tool is called during
// plan mode.
var ErrPlanModeReadOnly = errors.New("plan mode: mutating tools are refused until the plan is approved")

// ReadOnlyDeclarer marks a tool that cannot mutate anything. Plan mode
// allows only declared read-only tools, so an undeclared tool is refused:
// classification fails closed, exactly like the shell and file policies.
type ReadOnlyDeclarer interface {
	ReadOnly() bool
}

// ReadOnlyOf reports whether a tool declares itself read-only. Unknown
// tools are not read-only.
func ReadOnlyOf(t Tool) bool {
	if t == nil {
		return false
	}
	declarer, ok := t.(ReadOnlyDeclarer)
	return ok && declarer.ReadOnly()
}

// PlanModeActive reports whether a run is in the planning phase.
func PlanModeActive(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	value, _ := metadata[PlanModeMetadataKey].(string)
	return value == PlanModePlanning
}

// PlanMode builds the middleware that refuses mutating tools while the
// run is planning. The resolver supplies the tool definitions so the
// middleware can consult ReadOnlyDeclarer; a tool the resolver cannot
// find is treated as mutating.
func PlanMode(resolve func(name string) (Tool, bool)) Middleware {
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			if !PlanModeActive(call.Metadata) {
				return next.Invoke(ctx, call)
			}
			readOnly := false
			if resolve != nil {
				if instance, ok := resolve(call.Name); ok {
					readOnly = ReadOnlyOf(instance)
				}
			}
			if readOnly {
				return next.Invoke(ctx, call)
			}
			payload := map[string]any{
				"code":      PlanModeCode,
				"tool":      call.Name,
				"mode":      PlanModePlanning,
				"message":   PlanModeGuidance,
				"arguments": json.RawMessage(call.Arguments),
			}
			return Result{
				Error:      fmt.Sprintf("%s: %s", PlanModeCode, ErrPlanModeReadOnly.Error()),
				ExitCode:   1,
				Structured: payload,
				Metadata:   map[string]any{"code": PlanModeCode},
			}, ErrPlanModeReadOnly
		})
	}
}

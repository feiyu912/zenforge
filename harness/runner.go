package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge/model"
)

type RuntimeEvent string

const (
	RuntimeRunStarted       RuntimeEvent = "run.started"
	RuntimeRunResumed       RuntimeEvent = "run.resumed"
	RuntimeRunDone          RuntimeEvent = "run.done"
	RuntimeRunError         RuntimeEvent = "run.error"
	RuntimeRunCancelled     RuntimeEvent = "run.cancelled"
	RuntimeStepStarted      RuntimeEvent = "step.started"
	RuntimeModelStarted     RuntimeEvent = "model.started"
	RuntimeModelInterrupted RuntimeEvent = "model.interrupted"
	RuntimeModelSuperseded  RuntimeEvent = "model.superseded"
	RuntimeModelRestarted   RuntimeEvent = "model.restarted"
	RuntimeModelDone        RuntimeEvent = "model.done"
	RuntimeCheckpoint       RuntimeEvent = "checkpoint.created"
)

type Terminal struct {
	Type RuntimeEvent
	Data map[string]any
	Err  error
}

type Runner struct {
	MaxSteps int
	Mode     string

	Emit                  func(RuntimeEvent, map[string]any) error
	Checkpoint            func(context.Context, RunState) error
	CallModel             func(context.Context, RunState, model.ToolChoice) (MessageState, model.Usage, error)
	DurableCallModel      func(context.Context, *RunState, model.ToolChoice) (MessageState, model.Usage, error)
	RunPendingTools       func(context.Context, *RunState) error
	DrainSteers           func(context.Context, *RunState) error
	ResumeWaitingApproval func(context.Context, *RunState) error
	ResumeTerminal        func(RunState) bool
	IsPause               func(error) bool
	IsPersistenceError    func(error) bool
}

func (r Runner) Run(ctx context.Context, state RunState, resumed bool) (terminal Terminal) {
	emit := func(eventType RuntimeEvent, data map[string]any) bool {
		if r.Emit == nil {
			return true
		}
		if err := r.Emit(eventType, data); err != nil {
			terminal = Terminal{Type: RuntimeRunError, Data: map[string]any{"error": err.Error()}, Err: err}
			return false
		}
		return true
	}
	checkpointState := func() error {
		if r.Checkpoint == nil {
			return nil
		}
		return r.Checkpoint(ctx, state)
	}
	isPersistenceError := func(err error) bool {
		return err != nil && r.IsPersistenceError != nil && r.IsPersistenceError(err)
	}
	isPause := func(err error) bool {
		return err != nil && r.IsPause != nil && r.IsPause(err)
	}
	cancelRun := func(err error) {
		if state.Meta == nil {
			state.Meta = map[string]any{}
		}
		state.Meta["error"] = err.Error()
		state.Phase = RunPhaseCancelled
		state.Control.Status = RunStatusCancelled
		if saveErr := checkpointState(); saveErr != nil {
			if isPersistenceError(saveErr) {
				terminal = Terminal{Type: RuntimeRunError, Data: map[string]any{"error": saveErr.Error()}, Err: saveErr}
				return
			}
			emit(RuntimeRunError, map[string]any{"error": saveErr.Error()})
			return
		}
		terminal = Terminal{Type: RuntimeRunCancelled, Data: map[string]any{"error": err.Error()}}
		emit(RuntimeRunCancelled, terminal.Data)
	}
	fail := func(err error) {
		if isPersistenceError(err) {
			terminal = Terminal{Type: RuntimeRunError, Data: map[string]any{"error": err.Error()}, Err: err}
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cancelRun(err)
			return
		}
		if state.Meta == nil {
			state.Meta = map[string]any{}
		}
		state.Meta["error"] = err.Error()
		state.Phase = RunPhaseFailed
		state.Control.Status = RunStatusFailed
		if saveErr := checkpointState(); saveErr != nil {
			if isPersistenceError(saveErr) {
				terminal = Terminal{Type: RuntimeRunError, Data: map[string]any{"error": saveErr.Error()}, Err: saveErr}
				return
			}
			emit(RuntimeRunError, map[string]any{"error": saveErr.Error()})
			return
		}
		terminal = Terminal{Type: RuntimeRunError, Data: map[string]any{"error": err.Error()}}
		emit(RuntimeRunError, terminal.Data)
	}

	if err := ctx.Err(); err != nil {
		cancelRun(err)
		return terminal
	}
	mode := state.Mode
	if mode == "" {
		mode = r.Mode
	}
	resumeFinalizing := resumed && state.Phase == RunPhaseFinalizing
	if resumed {
		if !emit(RuntimeRunResumed, map[string]any{"input": state.Input, "mode": mode}) {
			return terminal
		}
		if r.ResumeTerminal != nil && r.ResumeTerminal(state) {
			return terminal
		}
		if state.Approval.Waiting != nil && r.ResumeWaitingApproval != nil {
			if err := r.ResumeWaitingApproval(ctx, &state); err != nil {
				if isPause(err) {
					return terminal
				}
				fail(err)
				return terminal
			}
		}
		if state.Tool.Active != nil {
			call := *state.Tool.Active
			call.Status = ToolCallPending
			call.StartedAt = nil
			state.Tool.Pending = append([]ToolCallState{call}, state.Tool.Pending...)
			state.Tool.Active = nil
			state.Control.Status = RunStatusRunning
			if err := checkpointState(); err != nil {
				fail(err)
				return terminal
			}
		}
		if state.Model.Active != nil {
			if state.Model.Active.Status == ModelAttemptStarted || state.Model.Active.Status == ModelAttemptStreaming {
				now := time.Now().UTC()
				state.Model.Active.Status = ModelAttemptInterrupted
				state.Model.Active.CompletedAt = &now
				if err := checkpointState(); err != nil {
					fail(err)
					return terminal
				}
				if !emit(RuntimeModelInterrupted, modelAttemptData(state.Model.Active)) {
					return terminal
				}
			}
			if state.Model.Active.Status == ModelAttemptInterrupted {
				state.Model.Active.Status = ModelAttemptSuperseded
				if err := checkpointState(); err != nil {
					fail(err)
					return terminal
				}
				if !emit(RuntimeModelSuperseded, modelAttemptData(state.Model.Active)) {
					return terminal
				}
			}
		}
		if state.Phase == RunPhaseModel && state.Control.Status == RunStatusRunning &&
			state.Model.Active == nil && len(state.Tool.Pending) == 0 &&
			len(state.Messages) > 0 {
			assistant := state.Messages[len(state.Messages)-1]
			if assistant.Role == "assistant" && len(assistant.ToolCalls) == 0 {
				state.Phase = RunPhaseCompleted
				state.Control.Status = RunStatusCompleted
				if err := checkpointState(); err != nil {
					fail(err)
					return terminal
				}
				terminal = Terminal{Type: RuntimeRunDone, Data: map[string]any{"output": assistant.Content}}
				emit(RuntimeRunDone, terminal.Data)
				return terminal
			}
		}
	} else if !emit(RuntimeRunStarted, map[string]any{"input": state.Input, "mode": mode}) {
		return terminal
	}

	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 8
	}
	if mode == "oneshot" && maxSteps > 2 {
		maxSteps = 2
	}
	for !resumeFinalizing {
		if err := ctx.Err(); err != nil {
			cancelRun(err)
			return terminal
		}
		if len(state.Tool.Pending) > 0 {
			if r.RunPendingTools == nil {
				fail(fmt.Errorf("pending tool runner is not configured"))
				return terminal
			}
			if err := r.RunPendingTools(ctx, &state); err != nil {
				if isPause(err) {
					return terminal
				}
				fail(err)
				return terminal
			}
			continue
		}
		if r.DrainSteers != nil {
			if err := r.DrainSteers(ctx, &state); err != nil {
				fail(err)
				return terminal
			}
		}
		replacing := state.Model.Active != nil && state.Model.Active.Status == ModelAttemptSuperseded
		if state.Step >= maxSteps && !replacing {
			break
		}
		if r.CallModel == nil && r.DurableCallModel == nil {
			fail(fmt.Errorf("model caller is not configured"))
			return terminal
		}

		if !replacing {
			state.Step++
		}
		state.Phase = RunPhaseModel
		state.Control.Status = RunStatusModelStreaming
		if !emit(RuntimeStepStarted, map[string]any{"step": state.Step}) {
			return terminal
		}
		if err := checkpointState(); err != nil {
			fail(err)
			return terminal
		}
		if r.DurableCallModel == nil {
			if !emit(RuntimeModelStarted, map[string]any{"step": state.Step}) {
				return terminal
			}
		}
		var assistant MessageState
		var usage model.Usage
		var err error
		if r.DurableCallModel != nil {
			assistant, usage, err = r.DurableCallModel(ctx, &state, model.ToolChoiceAuto)
		} else {
			assistant, usage, err = r.CallModel(ctx, state, model.ToolChoiceAuto)
		}
		if err != nil {
			fail(err)
			return terminal
		}
		state.Messages = append(state.Messages, assistant)
		ApplyUsage(&state, usage)
		commitActiveAttempt(&state)
		state.Phase = RunPhaseModel
		state.Control.Status = RunStatusRunning
		state.Tool.Pending = ToolCallsToState(assistant.ToolCalls)
		if err := checkpointState(); err != nil {
			fail(err)
			return terminal
		}
		if !emit(RuntimeModelDone, map[string]any{"step": state.Step, "toolCallCount": len(assistant.ToolCalls)}) {
			return terminal
		}
		if len(state.Tool.Pending) == 0 {
			state.Phase = RunPhaseCompleted
			state.Control.Status = RunStatusCompleted
			if err := checkpointState(); err != nil {
				fail(err)
				return terminal
			}
			terminal = Terminal{Type: RuntimeRunDone, Data: map[string]any{"output": assistant.Content}}
			emit(RuntimeRunDone, terminal.Data)
			return terminal
		}
	}

	if !resumeFinalizing {
		state.Phase = RunPhaseFinalizing
		state.Control.Status = RunStatusModelStreaming
		state.Messages = append(state.Messages, MessageState{
			Role:    "user",
			Content: "You have reached the tool-use limit. Provide the best final answer using the available context.",
		})
		if err := checkpointState(); err != nil {
			fail(err)
			return terminal
		}
	}
	var assistant MessageState
	var usage model.Usage
	var err error
	if r.DurableCallModel != nil {
		assistant, usage, err = r.DurableCallModel(ctx, &state, model.ToolChoiceNone)
	} else {
		assistant, usage, err = r.CallModel(ctx, state, model.ToolChoiceNone)
	}
	if err != nil {
		fail(err)
		return terminal
	}
	if len(assistant.ToolCalls) > 0 {
		fail(fmt.Errorf("final no-tool model turn returned tool calls"))
		return terminal
	}
	state.Messages = append(state.Messages, assistant)
	ApplyUsage(&state, usage)
	commitActiveAttempt(&state)
	state.Phase = RunPhaseCompleted
	state.Control.Status = RunStatusCompleted
	if err := checkpointState(); err != nil {
		fail(err)
		return terminal
	}
	terminal = Terminal{Type: RuntimeRunDone, Data: map[string]any{"output": assistant.Content}}
	emit(RuntimeRunDone, terminal.Data)
	return terminal
}

func modelAttemptData(attempt *ModelAttempt) map[string]any {
	return map[string]any{
		"attemptId":  attempt.ID,
		"step":       attempt.LogicalStep,
		"chunkSeq":   attempt.ChunkSeq,
		"textOffset": len(attempt.TextDraft),
	}
}

func commitActiveAttempt(state *RunState) {
	if state.Model.Active == nil {
		return
	}
	now := time.Now().UTC()
	state.Model.Active.Status = ModelAttemptCommitted
	state.Model.Active.CompletedAt = &now
	state.Model.AppendAttempt(*state.Model.Active)
	state.Model.Active = nil
}

func ApplyUsage(state *RunState, usage model.Usage) {
	state.Usage.InputTokens += usage.PromptTokens
	state.Usage.OutputTokens += usage.CompletionTokens
	state.Usage.TotalTokens += usage.TotalTokens
	if state.Usage.TotalTokens == 0 {
		state.Usage.TotalTokens = state.Usage.InputTokens + state.Usage.OutputTokens
	}
}

func ToolCallsToState(calls []ToolCallSpec) []ToolCallState {
	out := make([]ToolCallState, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCallState{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: append([]byte(nil), call.Arguments...),
			Status:    ToolCallPending,
		})
	}
	return out
}

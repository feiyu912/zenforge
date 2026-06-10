package zenforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
	tasktool "github.com/feiyu912/zenforge/tools/task"
	todotools "github.com/feiyu912/zenforge/tools/todo"
	"github.com/feiyu912/zenforge/trace"
)

// Agent is the high-level batteries-included runtime entrypoint.
type Agent struct {
	config Config
	todos  planner.Manager
}

// New creates an Agent with the provided runtime configuration.
func New(config Config) *Agent {
	todos := config.Todos
	if todos == nil && (config.Planning == PlanningEnabled || config.Planning == PlanningPlanExecute) {
		todos = planner.NewMemoryManager(planner.MemoryConfig{})
	}
	return &Agent{config: config, todos: todos}
}

// Run executes a task and returns the final result.
func (a *Agent) Run(ctx context.Context, task Task) (*Result, error) {
	events, err := a.Stream(ctx, task)
	if err != nil {
		return nil, err
	}
	var result Result
	for event := range events {
		if event.Type == EventRunDone {
			result.RunID = event.RunID()
			result.Output = stringValue(event.Payload["output"])
		}
		if event.Type == EventRunError {
			result.RunID = event.RunID()
			return &result, fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
	}
	return &result, nil
}

// Stream executes a task and returns a stream of runtime events.
func (a *Agent) Stream(ctx context.Context, task Task) (<-chan Event, error) {
	runID := task.RunID
	if runID == "" {
		runID = newRunID()
	}
	if a.config.Model == nil {
		return a.streamNoop(ctx, runID, task.Input), nil
	}
	if a.config.Planning == PlanningPlanExecute {
		events := make(chan Event, 64)
		go func() {
			defer close(events)
			a.runPlanExecute(ctx, events, runID, task, nil)
		}()
		return events, nil
	}

	state := newRunState(runID, task.Input, task.Meta)
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		a.runLoop(ctx, events, state, false)
	}()
	return events, nil
}

// Resume resumes a run from the configured checkpoint store.
func (a *Agent) Resume(ctx context.Context, runID string) (<-chan Event, error) {
	if a.config.Checkpoints == nil {
		return nil, fmt.Errorf("checkpoint store is not configured")
	}
	checkpoint, err := a.config.Checkpoints.Load(ctx, runID)
	if err != nil {
		return nil, err
	}
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		if a.config.Planning == PlanningPlanExecute && isPlanExecuteState(checkpoint.State) {
			a.runPlanExecute(ctx, events, runID, Task{
				Input: planExecuteOriginalInput(checkpoint.State),
				Meta:  planExecuteUserMeta(checkpoint.State.Meta),
			}, &checkpoint.State)
			return
		}
		a.runLoop(ctx, events, checkpoint.State, true)
	}()
	return events, nil
}

func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func (a *Agent) emit(ctx context.Context, out chan<- Event, eventType EventType, runID string, data map[string]any) {
	event := NewEvent(eventType, runID, data)
	if a.config.Events != nil {
		if event.Seq == 0 {
			if latest, err := a.config.Events.LatestSeq(ctx, event.RunID()); err == nil {
				event = event.WithSeq(NextEventSeq(latest))
			}
		}
		_ = a.config.Events.Append(ctx, event)
	}
	if a.config.Trace != nil {
		_ = a.config.Trace.Emit(ctx, traceEvent(event))
	}
	select {
	case out <- event:
		return
	default:
	}
	select {
	case out <- event:
	case <-ctx.Done():
	}
}

func traceEvent(event Event) trace.Event {
	return trace.Event{
		Type:      string(event.Type),
		RunID:     event.RunID(),
		Seq:       event.Seq,
		Timestamp: event.Timestamp,
		Data:      event.Map(),
	}
}

func (a *Agent) streamNoop(ctx context.Context, runID, input string) <-chan Event {
	events := make(chan Event, 2)
	go func() {
		defer close(events)
		select {
		case <-ctx.Done():
			a.emit(ctx, events, EventRunError, runID, map[string]any{"error": ctx.Err().Error()})
			return
		default:
		}
		a.emit(ctx, events, EventRunStarted, runID, map[string]any{"input": input})
		a.emit(ctx, events, EventRunDone, runID, map[string]any{"output": ""})
	}()
	return events
}

const (
	planExecutePresetMetaKey = "planning.preset"
	planExecuteInputMetaKey  = "planning.input"
	planExecuteStageMetaKey  = "planning.stage"
	planExecutePresetValue   = "plan_execute"
	planExecuteStagePlan     = "plan"
	planExecuteStageExecute  = "execute"
)

func (a *Agent) runPlanExecute(ctx context.Context, out chan<- Event, runID string, task Task, resumeState *harness.RunState) {
	emit := func(eventType EventType, data map[string]any) {
		a.emit(ctx, out, eventType, runID, data)
	}
	fail := func(err error) {
		emit(EventRunError, map[string]any{"error": err.Error()})
	}
	if a.todos == nil {
		fail(fmt.Errorf("todo manager is not configured"))
		return
	}
	if resumeState != nil {
		emit(EventRunResumed, map[string]any{"input": task.Input, "preset": string(PlanningPlanExecute)})
	} else {
		emit(EventRunStarted, map[string]any{"input": task.Input, "preset": string(PlanningPlanExecute)})
	}

	todos, err := planExecuteTodos(ctx, a.todos, runID, resumeState)
	if err != nil {
		fail(err)
		return
	}
	if len(todos) == 0 {
		planInput := task.Input + "\n\n" + planner.PlanPrompt
		planState := newRunState(runID, planInput, planExecuteMeta(task.Meta, task.Input, planExecuteStagePlan))
		a.runLoop(ctx, out, planState, resumeState != nil && planExecuteStage(resumeState.Meta) == planExecuteStagePlan)
		todos, err = a.todos.List(ctx, runID)
		if err != nil {
			fail(err)
			return
		}
	}
	if len(todos) == 0 {
		fail(fmt.Errorf("plan_not_created"))
		return
	}

	for {
		current, ok := planner.FirstNonTerminal(todos)
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			emit(EventTaskCancelled, map[string]any{"todoId": current.ID, "error": err.Error()})
			emit(EventRunCancelled, map[string]any{"error": err.Error()})
			return
		}
		emit(EventTaskStarted, map[string]any{"todoId": current.ID, "content": current.Content})
		inProgress := planner.TodoInProgress
		todos, err = a.todos.Update(ctx, runID, current.ID, planner.Patch{Status: &inProgress})
		if err != nil {
			fail(err)
			return
		}
		emit(EventTodoUpdated, map[string]any{"todos": todos})

		executeState := newRunState(runID, taskPrompt(todos, current), planExecuteMeta(task.Meta, task.Input, planExecuteStageExecute))
		if resumeState != nil && planExecuteStage(resumeState.Meta) == planExecuteStageExecute {
			executeState = *resumeState
			resumeState = nil
		}
		a.runLoop(ctx, out, executeState, true)
		todos, err = a.todos.List(ctx, runID)
		if err != nil {
			fail(err)
			return
		}
		updated, ok := findTodo(todos, current.ID)
		if !ok {
			fail(fmt.Errorf("todo %q disappeared", current.ID))
			return
		}
		if !planner.TerminalStatus(updated.Status) {
			failed := planner.TodoFailed
			notes := "task ended without terminal todo_update"
			todos, _ = a.todos.Update(ctx, runID, current.ID, planner.Patch{Status: &failed, Notes: &notes})
			emit(EventTodoUpdated, map[string]any{"todos": todos})
			emit(EventTaskError, map[string]any{"todoId": current.ID, "error": notes})
			fail(fmt.Errorf(notes))
			return
		}
		emit(EventTaskDone, map[string]any{"todoId": current.ID, "status": string(updated.Status)})
		if updated.Status == planner.TodoFailed {
			fail(fmt.Errorf("todo %q failed", current.ID))
			return
		}
	}

	summaryState := newRunState(runID, summaryPrompt(task.Input, todos), planExecuteMeta(task.Meta, task.Input, "summary"))
	summaryState.Phase = harness.RunPhaseFinalizing
	assistant, _, err := a.callModel(ctx, emit, summaryState, model.ToolChoiceNone)
	if err != nil {
		fail(err)
		return
	}
	emit(EventRunDone, map[string]any{"output": assistant.Content, "todos": todos})
}

func planExecuteTodos(ctx context.Context, manager planner.Manager, runID string, resumeState *harness.RunState) ([]planner.Todo, error) {
	if resumeState != nil && len(resumeState.Todos) > 0 {
		return manager.Replace(ctx, runID, plannerTodosFromState(resumeState.Todos))
	}
	return manager.List(ctx, runID)
}

func planExecuteMeta(meta map[string]any, input, stage string) map[string]any {
	out := cloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	out[planExecutePresetMetaKey] = planExecutePresetValue
	out[planExecuteInputMetaKey] = input
	out[planExecuteStageMetaKey] = stage
	return out
}

func isPlanExecuteState(state harness.RunState) bool {
	return stringValue(state.Meta[planExecutePresetMetaKey]) == planExecutePresetValue
}

func planExecuteOriginalInput(state harness.RunState) string {
	if input := stringValue(state.Meta[planExecuteInputMetaKey]); input != "" {
		return input
	}
	return state.Input
}

func planExecuteStage(meta map[string]any) string {
	return stringValue(meta[planExecuteStageMetaKey])
}

func planExecuteUserMeta(meta map[string]any) map[string]any {
	out := cloneMap(meta)
	delete(out, planExecutePresetMetaKey)
	delete(out, planExecuteInputMetaKey)
	delete(out, planExecuteStageMetaKey)
	return out
}

func plannerTodosFromState(todos []harness.TodoState) []planner.Todo {
	out := make([]planner.Todo, 0, len(todos))
	for _, todo := range todos {
		out = append(out, planner.Todo{
			ID:      todo.ID,
			Content: todo.Content,
			Status:  planner.TodoStatus(todo.Status),
		})
	}
	return out
}

func newRunState(runID, input string, meta map[string]any) harness.RunState {
	now := time.Now().UTC()
	return harness.RunState{
		Version:   harness.RunStateVersion,
		RunID:     runID,
		Input:     input,
		Phase:     harness.RunPhaseCreated,
		CreatedAt: now,
		UpdatedAt: now,
		Messages: []harness.MessageState{
			{Role: "user", Content: input},
		},
		Control: harness.RunControlState{Status: harness.RunStatusRunning},
		Meta:    cloneMap(meta),
	}
}

func (a *Agent) runLoop(ctx context.Context, out chan<- Event, state harness.RunState, resumed bool) {
	runID := state.RunID
	emit := func(eventType EventType, data map[string]any) {
		a.emit(ctx, out, eventType, runID, data)
	}
	checkpointSeq := int64(0)
	checkpointState := func() {
		if a.config.Checkpoints == nil {
			return
		}
		checkpointSeq++
		state.UpdatedAt = time.Now().UTC()
		_ = a.config.Checkpoints.Save(ctx, checkpoint.Checkpoint{
			Version: checkpoint.CheckpointVersion,
			RunID:   runID,
			Seq:     checkpointSeq,
			State:   state,
			SavedAt: time.Now().UTC(),
		})
		emit(EventCheckpointCreated, map[string]any{
			"checkpointSeq": checkpointSeq,
			"phase":         string(state.Phase),
		})
	}
	fail := func(err error) {
		state.Phase = harness.RunPhaseFailed
		state.Control.Status = harness.RunStatusFailed
		checkpointState()
		emit(EventRunError, map[string]any{"error": err.Error()})
	}

	if err := ctx.Err(); err != nil {
		state.Phase = harness.RunPhaseCancelled
		state.Control.Status = harness.RunStatusCancelled
		checkpointState()
		emit(EventRunCancelled, map[string]any{"error": err.Error()})
		return
	}
	if resumed {
		emit(EventRunResumed, map[string]any{"input": state.Input})
		if a.resumeTerminal(emit, state) {
			return
		}
		if state.Approval.Waiting != nil {
			if err := a.resumeWaitingApproval(ctx, emit, checkpointState, &state); err != nil {
				fail(err)
				return
			}
		}
		if state.Tool.Active != nil {
			call := *state.Tool.Active
			call.Status = harness.ToolCallPending
			call.StartedAt = nil
			state.Tool.Pending = append([]harness.ToolCallState{call}, state.Tool.Pending...)
			state.Tool.Active = nil
			state.Control.Status = harness.RunStatusRunning
			checkpointState()
		}
	} else {
		emit(EventRunStarted, map[string]any{"input": state.Input})
	}

	maxSteps := a.config.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 8
	}
	for {
		if err := ctx.Err(); err != nil {
			state.Phase = harness.RunPhaseCancelled
			state.Control.Status = harness.RunStatusCancelled
			checkpointState()
			emit(EventRunCancelled, map[string]any{"error": err.Error()})
			return
		}
		if len(state.Tool.Pending) > 0 {
			if err := a.runPendingTools(ctx, emit, checkpointState, &state); err != nil {
				fail(err)
				return
			}
			continue
		}
		if state.Step >= maxSteps {
			break
		}

		state.Step++
		state.Phase = harness.RunPhaseModel
		state.Control.Status = harness.RunStatusModelStreaming
		emit(EventStepStarted, map[string]any{"step": state.Step})
		checkpointState()
		emit(EventModelStarted, map[string]any{"step": state.Step})

		assistant, usage, err := a.callModel(ctx, emit, state, model.ToolChoiceAuto)
		if err != nil {
			fail(err)
			return
		}
		state.Messages = append(state.Messages, assistant)
		applyUsage(&state, usage)
		state.Phase = harness.RunPhaseModel
		state.Control.Status = harness.RunStatusRunning
		state.Tool.Pending = toolCallsToState(assistant.ToolCalls)
		checkpointState()
		emit(EventModelDone, map[string]any{"step": state.Step, "toolCallCount": len(assistant.ToolCalls)})

		if len(state.Tool.Pending) == 0 {
			state.Phase = harness.RunPhaseCompleted
			state.Control.Status = harness.RunStatusCompleted
			checkpointState()
			emit(EventRunDone, map[string]any{"output": assistant.Content})
			return
		}
	}

	state.Phase = harness.RunPhaseFinalizing
	state.Control.Status = harness.RunStatusModelStreaming
	state.Messages = append(state.Messages, harness.MessageState{
		Role:    "user",
		Content: "You have reached the tool-use limit. Provide the best final answer using the available context.",
	})
	checkpointState()
	assistant, usage, err := a.callModel(ctx, emit, state, model.ToolChoiceNone)
	if err != nil {
		fail(err)
		return
	}
	state.Messages = append(state.Messages, assistant)
	applyUsage(&state, usage)
	state.Phase = harness.RunPhaseCompleted
	state.Control.Status = harness.RunStatusCompleted
	checkpointState()
	emit(EventRunDone, map[string]any{"output": assistant.Content})
}

func (a *Agent) resumeTerminal(emit func(EventType, map[string]any), state harness.RunState) bool {
	switch state.Phase {
	case harness.RunPhaseCompleted:
		emit(EventRunDone, map[string]any{"output": lastAssistantContent(state)})
		return true
	case harness.RunPhaseFailed:
		emit(EventRunError, map[string]any{"error": terminalError(state, "run failed before resume")})
		return true
	case harness.RunPhaseCancelled:
		emit(EventRunCancelled, map[string]any{"error": terminalError(state, "run cancelled before resume")})
		return true
	default:
		return false
	}
}

func (a *Agent) resumeWaitingApproval(ctx context.Context, emit func(EventType, map[string]any), checkpointState func(), state *harness.RunState) error {
	if state.Approval.Waiting == nil {
		return nil
	}
	if state.Tool.Active == nil {
		return fmt.Errorf("resume waiting approval requires active tool call")
	}
	if a.config.Approval == nil {
		return fmt.Errorf("resume waiting approval requires approval broker")
	}
	call := *state.Tool.Active
	req := approvalRequestFromState(*state.Approval.Waiting, state.RunID, call)
	emit(EventApprovalRequested, map[string]any{
		"requestId":  req.ID,
		"toolCallId": call.ID,
		"toolName":   call.Name,
		"operation":  req.Operation,
		"risk":       string(req.Risk),
		"request":    req,
		"resumed":    true,
	})
	decision, err := a.config.Approval.Request(ctx, req)
	if err != nil {
		return err
	}
	state.ResolveApproval(approvalDecisionState(decision))
	checkpointState()
	resolvedType := EventApprovalResolved
	if decision.Action == approval.DecisionReject && decision.Reason == approval.ErrorExpired {
		resolvedType = EventApprovalExpired
	}
	emit(resolvedType, map[string]any{
		"requestId":  decision.RequestID,
		"toolCallId": call.ID,
		"toolName":   call.Name,
		"action":     string(decision.Action),
		"scope":      string(decision.Scope),
		"reason":     decision.Reason,
		"resumed":    true,
	})
	if decision.Action == approval.DecisionAbort {
		return fmt.Errorf("approval aborted: %s", decision.Reason)
	}
	if approval.IsApprovedAction(decision.Action) {
		call.Meta = approval.ApprovedMetadata(call.Meta, req, decision)
		call.Status = harness.ToolCallPending
		call.StartedAt = nil
		state.Tool.Pending = append([]harness.ToolCallState{call}, state.Tool.Pending...)
		state.Tool.Active = nil
		state.Control.Status = harness.RunStatusRunning
		checkpointState()
		return nil
	}
	result := tool.Result{Error: approval.ErrorRejected, ExitCode: 1, Structured: map[string]any{
		"approval": req,
		"decision": decision,
	}}
	call.Status = harness.ToolCallFailed
	state.Tool.Active = nil
	state.Tool.Last = &harness.ToolResultState{
		ToolCallID: call.ID,
		Structured: result.Structured,
		Error:      result.Error,
		ExitCode:   result.ExitCode,
	}
	state.Messages = append(state.Messages, harness.MessageState{
		Role:       "tool",
		Content:    toolResultContent(result),
		ToolCallID: call.ID,
		Name:       call.Name,
	})
	state.Control.Status = harness.RunStatusRunning
	checkpointState()
	emit(EventToolError, map[string]any{
		"toolCallId": call.ID,
		"toolName":   call.Name,
		"error":      result.Error,
		"exitCode":   result.ExitCode,
		"resumed":    true,
	})
	return nil
}

func (a *Agent) callModel(ctx context.Context, emit func(EventType, map[string]any), state harness.RunState, choice model.ToolChoice) (harness.MessageState, model.Usage, error) {
	stream, err := a.config.Model.Stream(ctx, model.Request{
		Messages:   a.modelMessages(state),
		Tools:      a.toolSpecs(),
		ToolChoice: choice,
		Meta:       cloneMap(state.Meta),
	})
	if err != nil {
		return harness.MessageState{}, model.Usage{}, err
	}
	var content strings.Builder
	var message *model.Message
	var calls []model.ToolCallSpec
	var usage model.Usage
	for event := range stream {
		if event.Error != nil {
			return harness.MessageState{}, model.Usage{}, event.Error
		}
		if event.Delta != "" {
			content.WriteString(event.Delta)
			emit(EventModelDelta, map[string]any{"textDelta": event.Delta, "step": state.Step})
		}
		if event.Message != nil {
			message = event.Message
		}
		if len(event.ToolCalls) > 0 {
			calls = append(calls, event.ToolCalls...)
		}
		if event.Usage.TotalTokens > 0 || event.Usage.PromptTokens > 0 || event.Usage.CompletionTokens > 0 {
			usage = event.Usage
		}
	}
	if message != nil {
		if message.Content != "" {
			content.Reset()
			content.WriteString(message.Content)
		}
		calls = append(calls, message.ToolCalls...)
	}
	return harness.MessageState{
		Role:      "assistant",
		Content:   content.String(),
		ToolCalls: modelToolCallsToHarness(calls),
	}, usage, nil
}

func (a *Agent) runPendingTools(ctx context.Context, emit func(EventType, map[string]any), checkpointState func(), state *harness.RunState) error {
	for len(state.Tool.Pending) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		call := state.Tool.Pending[0]
		state.Tool.Pending = state.Tool.Pending[1:]
		now := time.Now().UTC()
		call.Status = harness.ToolCallRunning
		call.StartedAt = &now
		state.Tool.Active = &call
		state.Phase = harness.RunPhaseTool
		state.Control.Status = harness.RunStatusToolExecuting
		checkpointState()
		emit(EventToolCall, map[string]any{
			"toolCallId": call.ID,
			"toolName":   call.Name,
			"arguments":  rawJSONValue(call.Arguments),
		})

		result, err := a.invokeToolOrRuntime(ctx, emit, checkpointState, state, call)
		if err != nil && result.Error == "" {
			result = tool.Result{Error: err.Error(), ExitCode: 1}
		}
		if result.Error != "" && result.ExitCode == 0 {
			result.ExitCode = 1
		}
		applySandboxResultState(state, result)
		if req, ok := approval.RequestFromResult(result); ok {
			state.SetWaitingApproval(approvalRequestState(req, call))
			checkpointState()
			emit(EventApprovalRequested, map[string]any{
				"requestId":  req.ID,
				"toolCallId": call.ID,
				"toolName":   call.Name,
				"operation":  req.Operation,
				"risk":       string(req.Risk),
				"request":    req,
			})
			if a.config.Approval != nil {
				decision, approvalErr := a.config.Approval.Request(ctx, req)
				if approvalErr != nil {
					return approvalErr
				}
				state.ResolveApproval(approvalDecisionState(decision))
				checkpointState()
				resolvedType := EventApprovalResolved
				if decision.Action == approval.DecisionReject && decision.Reason == approval.ErrorExpired {
					resolvedType = EventApprovalExpired
				}
				emit(resolvedType, map[string]any{
					"requestId":  decision.RequestID,
					"toolCallId": call.ID,
					"toolName":   call.Name,
					"action":     string(decision.Action),
					"scope":      string(decision.Scope),
					"reason":     decision.Reason,
				})
				if decision.Action == approval.DecisionAbort {
					return fmt.Errorf("approval aborted: %s", decision.Reason)
				}
				if approval.IsApprovedAction(decision.Action) {
					approvedCall := call
					approvedCall.Meta = approval.ApprovedMetadata(call.Meta, req, decision)
					result, err = a.invokeTool(ctx, *state, approvedCall)
					if err != nil && result.Error == "" {
						result = tool.Result{Error: err.Error(), ExitCode: 1}
					}
					if result.Error != "" && result.ExitCode == 0 {
						result.ExitCode = 1
					}
					applySandboxResultState(state, result)
				} else {
					result = tool.Result{Error: approval.ErrorRejected, ExitCode: 1, Structured: map[string]any{
						"approval": req,
						"decision": decision,
					}}
				}
			}
		}
		status := harness.ToolCallDone
		eventType := EventToolResult
		if result.Error != "" || result.ExitCode != 0 {
			status = harness.ToolCallFailed
			eventType = EventToolError
		}
		call.Status = status
		state.Tool.Active = nil
		state.Tool.Last = &harness.ToolResultState{
			ToolCallID: call.ID,
			Output:     result.Output,
			Structured: result.Structured,
			Error:      result.Error,
			ExitCode:   result.ExitCode,
		}
		if todos, ok := plannerTodos(result.Structured["todos"]); ok {
			state.Todos = todos
			emit(EventTodoUpdated, map[string]any{
				"todos": result.Structured["todos"],
			})
		}
		state.Messages = append(state.Messages, harness.MessageState{
			Role:       "tool",
			Content:    toolResultContent(result),
			ToolCallID: call.ID,
			Name:       call.Name,
		})
		state.Control.Status = harness.RunStatusRunning
		checkpointState()
		emit(eventType, map[string]any{
			"toolCallId": call.ID,
			"toolName":   call.Name,
			"output":     result.Output,
			"error":      result.Error,
			"exitCode":   result.ExitCode,
		})
	}
	return nil
}

func (a *Agent) invokeToolOrRuntime(ctx context.Context, emit func(EventType, map[string]any), checkpointState func(), state *harness.RunState, call harness.ToolCallState) (tool.Result, error) {
	if tasktool.IsTaskTool(call.Name) {
		return a.invokeSubAgentTool(ctx, emit, checkpointState, state, call)
	}
	return a.invokeTool(ctx, *state, call)
}

func (a *Agent) invokeSubAgentTool(ctx context.Context, emit func(EventType, map[string]any), checkpointState func(), state *harness.RunState, call harness.ToolCallState) (tool.Result, error) {
	orchestrator, err := a.subAgentOrchestrator()
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	req, err := tasktool.Decode(call.Arguments)
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	req.RunID = state.RunID
	req.ParentStep = state.Step
	req.ToolCallID = call.ID
	if req.ParentTaskID == "" {
		req.ParentTaskID = call.ID
	}
	if depth, ok := intFromMeta(state.Meta["subagent.depth"]); ok {
		req.Depth = depth
	}
	startSubtasks(state, req)
	checkpointState()
	skipped, runnable := splitSubtasksForRun(*state, req)
	for _, task := range runnable {
		emit(EventSubtaskStarted, map[string]any{
			"subtaskId": task.ID,
			"agentName": task.NormalizedAgentName(),
			"name":      task.Name,
		})
	}
	result := subagent.Result{Tasks: skipped}
	var invokeErr error
	if len(runnable) > 0 {
		runReq := req
		runReq.Tasks = runnable
		runningResult, err := orchestrator.Invoke(ctx, runReq)
		invokeErr = err
		result.Tasks = mergeSubtaskResults(req.Tasks, skipped, runningResult.Tasks)
	}
	applySubtaskResults(state, req.ParentTaskID, result)
	checkpointState()
	for _, task := range result.Tasks {
		for _, childEvent := range task.Events {
			emit(EventSubtaskEvent, map[string]any{
				"subtaskId":      task.ID,
				"agentName":      task.AgentName,
				"childRunId":     task.RunID,
				"childEventType": childEvent.Type,
				"childEvent":     childEvent,
			})
		}
		eventType := EventSubtaskDone
		if task.Status == subagent.StatusFailed {
			eventType = EventSubtaskError
		}
		emit(eventType, map[string]any{
			"subtaskId": task.ID,
			"agentName": task.AgentName,
			"status":    task.Status,
			"output":    task.Output,
			"error":     task.Error,
			"runId":     task.RunID,
		})
	}
	output, structured, err := result.ToolResultJSON()
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	toolResult := tool.Result{Output: output, Structured: structured}
	if invokeErr != nil {
		toolResult.Error = invokeErr.Error()
		toolResult.ExitCode = 1
	}
	for _, task := range result.Tasks {
		if task.Status == subagent.StatusFailed && toolResult.ExitCode == 0 {
			toolResult.ExitCode = 1
		}
	}
	return toolResult, invokeErr
}

func approvalRequestState(req approval.Request, call harness.ToolCallState) harness.ApprovalRequestState {
	options := make([]string, 0, len(req.Options))
	for _, option := range req.Options {
		if option.Label != "" {
			options = append(options, option.Label)
			continue
		}
		options = append(options, string(option.Action))
	}
	toolCallID := req.ToolCallID
	if toolCallID == "" {
		toolCallID = call.ID
	}
	return harness.ApprovalRequestState{
		ID:          req.ID,
		RunID:       req.RunID,
		ToolCallID:  toolCallID,
		ToolName:    req.ToolName,
		Operation:   req.Operation,
		Title:       req.Title,
		Description: req.Description,
		Risk:        string(req.Risk),
		Options:     options,
		Payload:     req.Payload,
		ExpiresAt:   req.ExpiresAt,
	}
}

func approvalRequestFromState(state harness.ApprovalRequestState, runID string, call harness.ToolCallState) approval.Request {
	requestRunID := state.RunID
	if requestRunID == "" {
		requestRunID = runID
	}
	toolCallID := state.ToolCallID
	if toolCallID == "" {
		toolCallID = call.ID
	}
	toolName := state.ToolName
	if toolName == "" {
		toolName = call.Name
	}
	operation := state.Operation
	if operation == "" {
		operation = "approval.resume"
	}
	risk := approval.RiskLevel(state.Risk)
	if risk == "" {
		risk = approval.RiskMedium
	}
	return approval.Request{
		ID:          state.ID,
		RunID:       requestRunID,
		ToolCallID:  toolCallID,
		ToolName:    toolName,
		Operation:   operation,
		Title:       state.Title,
		Description: state.Description,
		Risk:        risk,
		Options:     approval.DefaultOptions(),
		Payload:     cloneMap(state.Payload),
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   state.ExpiresAt,
	}
}

func approvalDecisionState(decision approval.Decision) harness.ApprovalDecisionState {
	return harness.ApprovalDecisionState{
		RequestID: decision.RequestID,
		Action:    string(decision.Action),
		Scope:     string(decision.Scope),
		Reason:    decision.Reason,
		Payload:   decision.Payload,
		DecidedAt: decision.DecidedAt,
	}
}

func plannerTodos(value any) ([]harness.TodoState, bool) {
	todos, ok := value.([]planner.Todo)
	if !ok {
		return nil, false
	}
	out := make([]harness.TodoState, 0, len(todos))
	for _, todo := range todos {
		out = append(out, harness.TodoState{
			ID:      todo.ID,
			Content: todo.Content,
			Status:  harness.TodoStatus(todo.Status),
		})
	}
	return out, true
}

func (a *Agent) invokeTool(ctx context.Context, state harness.RunState, call harness.ToolCallState) (tool.Result, error) {
	invoker := a.config.ToolInvoker
	if invoker == nil {
		configuredTools, err := a.configuredTools()
		if err != nil {
			return tool.Result{Error: err.Error(), ExitCode: 1}, err
		}
		registry, err := tool.NewRegistry(configuredTools...)
		if err != nil {
			return tool.Result{Error: err.Error(), ExitCode: 1}, err
		}
		invoker = tool.NewInvoker(registry, a.config.ToolRuntime...)
	}
	return invoker.Invoke(ctx, tool.Call{
		ID:        call.ID,
		RunID:     state.RunID,
		Name:      call.Name,
		Arguments: call.Arguments,
		Metadata:  toolCallMetadata(state, call.Meta),
	})
}

func (a *Agent) subAgentOrchestrator() (subagent.Orchestrator, error) {
	if a.config.SubAgentOrchestrator != nil {
		return a.config.SubAgentOrchestrator, nil
	}
	registry := a.config.SubAgentRegistry
	var err error
	if registry == nil && len(a.config.SubAgentSpecs) > 0 {
		registry, err = subagent.NewRegistry(a.config.SubAgentSpecs...)
		if err != nil {
			return nil, err
		}
	}
	runner := a.config.SubAgentRunner
	if runner == nil {
		runner = subagent.RunnerFunc(a.runChildSubAgent)
	}
	return subagent.NewOrchestrator(subagent.OrchestratorConfig{
		Registry: registry,
		Runner:   runner,
		Options:  subagent.Options{MaxTasks: 8, Parallel: true},
	}), nil
}

func (a *Agent) runChildSubAgent(ctx context.Context, spec subagent.SubAgentSpec, task subagent.TaskSpec, req subagent.Request) (subagent.TaskResult, error) {
	childModel := spec.Model
	if childModel == nil {
		childModel = a.config.Model
	}
	childInstructions := spec.Instructions
	if childInstructions == "" {
		childInstructions = a.config.Instructions
	}
	maxSteps := spec.MaxSteps
	if maxSteps <= 0 {
		maxSteps = a.config.MaxSteps
	}
	childRunID := fmt.Sprintf("%s_sub_%s", req.RunID, task.ID)
	childMeta := cloneMap(spec.Metadata)
	if childMeta == nil {
		childMeta = map[string]any{}
	}
	childMeta["parentRunId"] = req.RunID
	childMeta["subtaskId"] = task.ID
	childMeta["subagent.depth"] = req.Depth + 1
	child := New(Config{
		Model:        childModel,
		Instructions: childInstructions,
		Tools:        spec.Tools,
		Approval:     a.config.Approval,
		Checkpoints:  a.config.Checkpoints,
		Events:       a.config.Events,
		Trace:        a.config.Trace,
		MaxSteps:     maxSteps,
		Planning:     PlanningDisabled,
	})
	events, err := childSubAgentEvents(ctx, child, a.config.Checkpoints, childRunID, task.Input, childMeta)
	if err != nil {
		return subagent.TaskResult{
			ID:        task.ID,
			AgentName: spec.Name,
			Name:      task.Name,
			RunID:     childRunID,
			Status:    subagent.StatusFailed,
			Error:     err.Error(),
			Metadata:  cloneMap(task.Metadata),
		}, err
	}
	var output string
	var childEvents []subagent.Event
	var runErr error
	for event := range events {
		childEvents = append(childEvents, subagent.Event{
			Seq:       event.Seq,
			Type:      string(event.Type),
			Timestamp: event.Timestamp,
			Payload:   cloneMap(event.Payload),
		})
		if event.Type == EventRunDone {
			output = stringValue(event.Payload["output"])
		}
		if event.Type == EventRunError {
			runErr = fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
	}
	taskResult := subagent.TaskResult{
		ID:        task.ID,
		AgentName: spec.Name,
		Name:      task.Name,
		Output:    output,
		RunID:     childRunID,
		Status:    subagent.StatusCompleted,
		Metadata:  cloneMap(task.Metadata),
		Events:    childEvents,
	}
	if runErr != nil {
		taskResult.Status = subagent.StatusFailed
		taskResult.Error = runErr.Error()
	}
	return taskResult, runErr
}

func childSubAgentEvents(ctx context.Context, child *Agent, checkpoints checkpoint.Store, childRunID, input string, meta map[string]any) (<-chan Event, error) {
	if checkpoints != nil {
		if _, err := checkpoints.Load(ctx, childRunID); err == nil {
			return child.Resume(ctx, childRunID)
		} else if !errors.Is(err, checkpoint.ErrNotFound) {
			return nil, err
		}
	}
	return child.Stream(ctx, Task{RunID: childRunID, Input: input, Meta: meta})
}

func startSubtasks(state *harness.RunState, req subagent.Request) {
	for i, task := range req.Tasks {
		if task.ID == "" {
			task.ID = fmt.Sprintf("subtask_%d", i+1)
			req.Tasks[i].ID = task.ID
		}
		subtask := harness.SubtaskState{
			ID:        task.ID,
			ParentID:  req.ParentTaskID,
			AgentName: task.NormalizedAgentName(),
			Input:     task.Input,
			Status:    harness.SubtaskRunning,
			Meta:      cloneMap(task.Metadata),
		}
		if idx := findSubtask(state.Subtasks, subtask.ParentID, subtask.ID); idx >= 0 {
			if state.Subtasks[idx].Status != harness.SubtaskCompleted && state.Subtasks[idx].Status != harness.SubtaskFailed {
				state.Subtasks[idx].AgentName = subtask.AgentName
				state.Subtasks[idx].Input = subtask.Input
				state.Subtasks[idx].Status = subtask.Status
				state.Subtasks[idx].Meta = subtask.Meta
			}
			continue
		}
		state.Subtasks = append(state.Subtasks, subtask)
	}
	state.Phase = harness.RunPhaseSubtask
}

func findSubtask(subtasks []harness.SubtaskState, parentID, id string) int {
	for i, subtask := range subtasks {
		if subtask.ParentID == parentID && subtask.ID == id {
			return i
		}
	}
	return -1
}

func splitSubtasksForRun(state harness.RunState, req subagent.Request) ([]subagent.TaskResult, []subagent.TaskSpec) {
	skipped := make([]subagent.TaskResult, 0, len(req.Tasks))
	runnable := make([]subagent.TaskSpec, 0, len(req.Tasks))
	for _, task := range req.Tasks {
		idx := findSubtask(state.Subtasks, req.ParentTaskID, task.ID)
		if idx < 0 {
			runnable = append(runnable, task)
			continue
		}
		result, ok := terminalSubtaskResult(state.Subtasks[idx], task)
		if ok {
			skipped = append(skipped, result)
			continue
		}
		runnable = append(runnable, task)
	}
	return skipped, runnable
}

func terminalSubtaskResult(state harness.SubtaskState, task subagent.TaskSpec) (subagent.TaskResult, bool) {
	status := ""
	switch state.Status {
	case harness.SubtaskCompleted:
		status = subagent.StatusCompleted
	case harness.SubtaskFailed:
		status = subagent.StatusFailed
	default:
		return subagent.TaskResult{}, false
	}
	return subagent.TaskResult{
		ID:        state.ID,
		AgentName: state.AgentName,
		Name:      task.Name,
		Status:    status,
		Output:    state.Output,
		Error:     state.Error,
		RunID:     state.RunID,
		Metadata:  cloneMap(state.Meta),
	}, true
}

func mergeSubtaskResults(tasks []subagent.TaskSpec, skipped, running []subagent.TaskResult) []subagent.TaskResult {
	byID := make(map[string]subagent.TaskResult, len(skipped)+len(running))
	for _, result := range skipped {
		byID[result.ID] = result
	}
	for _, result := range running {
		byID[result.ID] = result
	}
	out := make([]subagent.TaskResult, 0, len(tasks))
	for _, task := range tasks {
		if result, ok := byID[task.ID]; ok {
			out = append(out, result)
		}
	}
	return out
}

func applySubtaskResults(state *harness.RunState, parentID string, result subagent.Result) {
	for _, task := range result.Tasks {
		for i := range state.Subtasks {
			if state.Subtasks[i].ParentID != parentID || state.Subtasks[i].ID != task.ID {
				continue
			}
			state.Subtasks[i].RunID = task.RunID
			state.Subtasks[i].Output = task.Output
			state.Subtasks[i].Error = task.Error
			state.Subtasks[i].Status = harness.SubtaskCompleted
			if task.Status == subagent.StatusFailed {
				state.Subtasks[i].Status = harness.SubtaskFailed
			}
			break
		}
	}
}

func intFromMeta(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func toolCallMetadata(state harness.RunState, callMeta map[string]any) map[string]any {
	out := cloneMap(state.Meta)
	if state.Sandbox.SessionID != "" {
		if out == nil {
			out = map[string]any{}
		}
		out[sandbox.MetadataStateKey] = sandbox.State{
			SessionID:     state.Sandbox.SessionID,
			EnvironmentID: state.Sandbox.EnvironmentID,
			WorkingDir:    state.Sandbox.WorkingDir,
			Metadata:      cloneMap(state.Sandbox.Meta),
		}
	}
	if len(callMeta) == 0 {
		return out
	}
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range callMeta {
		out[key] = value
	}
	return out
}

func applySandboxResultState(state *harness.RunState, result tool.Result) {
	sandboxState, ok := sandbox.StateFromMetadata(result.Metadata)
	if !ok {
		sandboxState, ok = sandbox.StateFromMetadata(result.Meta)
	}
	if !ok {
		return
	}
	state.Sandbox = harness.SandboxState{
		SessionID:     sandboxState.SessionID,
		EnvironmentID: sandboxState.EnvironmentID,
		WorkingDir:    sandboxState.WorkingDir,
		Meta:          cloneMap(sandboxState.Metadata),
	}
}

func (a *Agent) modelMessages(state harness.RunState) []model.Message {
	messages := make([]model.Message, 0, len(state.Messages)+1)
	if a.config.Instructions != "" {
		messages = append(messages, model.Message{Role: "system", Content: a.config.Instructions})
	}
	for _, message := range state.Messages {
		messages = append(messages, model.Message{
			Role:       message.Role,
			Content:    message.Content,
			Name:       message.Name,
			ToolCallID: message.ToolCallID,
			ToolCalls:  harnessToolCallsToModel(message.ToolCalls),
		})
	}
	return messages
}

func (a *Agent) toolSpecs() []model.ToolSpec {
	configuredTools, err := a.configuredTools()
	if err != nil {
		configuredTools = a.config.Tools
	}
	specs := make([]model.ToolSpec, 0, len(configuredTools))
	for _, tool := range configuredTools {
		specs = append(specs, model.ToolSpec{
			Name:        tool.Name(),
			Description: tool.Description(),
			Schema:      tool.Schema(),
		})
	}
	return specs
}

func (a *Agent) configuredTools() ([]tool.Tool, error) {
	configuredTools := append([]tool.Tool(nil), a.config.Tools...)
	if a.todos == nil {
		return configuredTools, nil
	}
	todoTools, err := todotools.Tools(todotools.Config{Manager: a.todos})
	if err != nil {
		return nil, err
	}
	existing := make(map[string]struct{}, len(configuredTools))
	for _, current := range configuredTools {
		existing[strings.ToLower(current.Name())] = struct{}{}
	}
	for _, current := range todoTools {
		if _, ok := existing[strings.ToLower(current.Name())]; ok {
			continue
		}
		configuredTools = append(configuredTools, current)
	}
	if a.config.SubAgents == SubAgentsEnabled || a.config.SubAgentOrchestrator != nil || a.config.SubAgentRegistry != nil || len(a.config.SubAgentSpecs) > 0 {
		taskTools, err := tasktool.Tools(tasktool.Config{})
		if err != nil {
			return nil, err
		}
		for _, current := range taskTools {
			if _, ok := existing[strings.ToLower(current.Name())]; ok {
				continue
			}
			configuredTools = append(configuredTools, current)
		}
	}
	return configuredTools, nil
}

func modelToolCallsToHarness(calls []model.ToolCallSpec) []harness.ToolCallSpec {
	out := make([]harness.ToolCallSpec, 0, len(calls))
	for _, call := range calls {
		out = append(out, harness.ToolCallSpec{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return out
}

func harnessToolCallsToModel(calls []harness.ToolCallSpec) []model.ToolCallSpec {
	out := make([]model.ToolCallSpec, 0, len(calls))
	for _, call := range calls {
		out = append(out, model.ToolCallSpec{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return out
}

func toolCallsToState(calls []harness.ToolCallSpec) []harness.ToolCallState {
	out := make([]harness.ToolCallState, 0, len(calls))
	for _, call := range calls {
		out = append(out, harness.ToolCallState{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
			Status:    harness.ToolCallPending,
		})
	}
	return out
}

func applyUsage(state *harness.RunState, usage model.Usage) {
	state.Usage.InputTokens += usage.PromptTokens
	state.Usage.OutputTokens += usage.CompletionTokens
	state.Usage.TotalTokens += usage.TotalTokens
}

func lastAssistantContent(state harness.RunState) string {
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == "assistant" {
			return state.Messages[i].Content
		}
	}
	return ""
}

func terminalError(state harness.RunState, fallback string) string {
	if state.Tool.Last != nil && state.Tool.Last.Error != "" {
		return state.Tool.Last.Error
	}
	if value, ok := state.Meta["error"].(string); ok && value != "" {
		return value
	}
	return fallback
}

func toolResultContent(result tool.Result) string {
	if result.Error != "" {
		return result.Error
	}
	if result.Output != "" {
		return result.Output
	}
	if len(result.Structured) == 0 {
		return ""
	}
	data, err := json.Marshal(result.Structured)
	if err != nil {
		return ""
	}
	return string(data)
}

func rawJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
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

func taskPrompt(todos []planner.Todo, current planner.Todo) string {
	prompt := strings.ReplaceAll(planner.TaskPromptTemplate, "{{todo_list}}", planner.FormatTodos(todos))
	prompt = strings.ReplaceAll(prompt, "{{todo_id}}", current.ID)
	prompt = strings.ReplaceAll(prompt, "{{todo_content}}", current.Content)
	return prompt
}

func summaryPrompt(input string, todos []planner.Todo) string {
	prompt := strings.ReplaceAll(planner.SummaryPromptTemplate, "{{input}}", input)
	prompt = strings.ReplaceAll(prompt, "{{todo_list}}", planner.FormatTodos(todos))
	return prompt
}

func findTodo(todos []planner.Todo, id string) (planner.Todo, bool) {
	for _, todo := range todos {
		if todo.ID == id {
			return todo, true
		}
	}
	return planner.Todo{}, false
}

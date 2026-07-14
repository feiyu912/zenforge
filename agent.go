package zenforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
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
	config             Config
	todos              planner.Manager
	skillCatalogPrompt string
	configErr          error
}

// New creates an Agent with the provided runtime configuration.
func New(config Config) *Agent {
	config = cloneConfig(config)
	var configErr error
	var skillCatalogPrompt string
	if config.Skills != nil {
		loadSkill := config.Skills.LoadSkillTool()
		if loadSkill == nil {
			configErr = fmt.Errorf("configure skills: load_skill tool is nil")
		} else {
			for _, current := range config.Tools {
				if current != nil && strings.EqualFold(strings.TrimSpace(current.Name()), strings.TrimSpace(loadSkill.Name())) {
					configErr = fmt.Errorf("configure skills: tool name %q conflicts with the skill loader", loadSkill.Name())
					break
				}
			}
			if configErr == nil {
				config.Tools = append(config.Tools, loadSkill)
				skillCatalogPrompt = config.Skills.CatalogPrompt()
			}
		}
	}
	todos := config.Todos
	if todos == nil && (config.Planning == PlanningEnabled || agentModeForConfig(config) == ModePlanExecute) {
		todos = planner.NewMemoryManager(planner.MemoryConfig{})
	}
	return &Agent{
		config:             config,
		todos:              todos,
		skillCatalogPrompt: skillCatalogPrompt,
		configErr:          configErr,
	}
}

func cloneConfig(config Config) Config {
	config.Tools = append([]tool.Tool(nil), config.Tools...)
	config.ToolRuntime = append([]tool.Middleware(nil), config.ToolRuntime...)
	config.ToolArgumentRedaction = append([]string(nil), config.ToolArgumentRedaction...)
	config.SubAgentSpecs = append([]subagent.SubAgentSpec(nil), config.SubAgentSpecs...)
	return config
}

// Run executes a task and returns the final result.
func (a *Agent) Run(ctx context.Context, task Task) (*Result, error) {
	events, err := a.Stream(ctx, task)
	if err != nil {
		return nil, err
	}
	var result Result
	var approvalPending bool
	for event := range events {
		if result.RunID == "" {
			result.RunID = event.RunID()
		}
		if event.Type == EventApprovalRequested {
			approvalPending = true
		}
		if event.Type == EventApprovalResolved || event.Type == EventApprovalExpired {
			approvalPending = false
		}
		if event.Type == EventRunDone {
			result.RunID = event.RunID()
			result.Output = stringValue(event.Payload["output"])
		}
		if event.Type == EventRunError {
			result.RunID = event.RunID()
			return &result, fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
		if event.Type == EventRunCancelled {
			result.RunID = event.RunID()
			return &result, runCancellationError(stringValue(event.Payload["error"]))
		}
	}
	if approvalPending {
		return &result, approval.ErrRequired
	}
	return &result, nil
}

// Stream executes a task and returns a stream of runtime events.
func (a *Agent) Stream(ctx context.Context, task Task) (<-chan Event, error) {
	if a.configErr != nil {
		return nil, a.configErr
	}
	runID := task.RunID
	if runID == "" {
		runID = newRunID()
	}
	if a.config.Model == nil {
		return a.streamNoop(ctx, runID, task.Input), nil
	}
	if err := a.openRunControl(runID); err != nil {
		return nil, err
	}
	if grantStoreConfigured(a.config.ApprovalGrants) {
		task.Meta = approvalNamespaceMeta(task.Meta, a.approvalNamespace(task.ApprovalNamespace))
	}
	mode := agentModeForConfig(a.config)
	if mode == ModePlanExecute {
		events := make(chan Event, 64)
		go func() {
			defer close(events)
			defer a.closeRunControl(runID)
			a.runPlanExecute(ctx, events, runID, task, nil)
		}()
		return events, nil
	}

	state := newTaskRunState(runID, task.Input, task.InitialMessages, task.Meta)
	state.Mode = string(mode)
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer a.closeRunControl(runID)
		a.runLoop(ctx, events, state, false)
	}()
	return events, nil
}

// Resume resumes a run from the configured checkpoint store.
func (a *Agent) Resume(ctx context.Context, runID string) (<-chan Event, error) {
	if a.configErr != nil {
		return nil, a.configErr
	}
	if a.config.Checkpoints == nil {
		return nil, fmt.Errorf("checkpoint store is not configured")
	}
	cp, err := a.config.Checkpoints.Load(ctx, runID)
	if err != nil {
		return nil, err
	}
	if cp == nil {
		return nil, fmt.Errorf("checkpoint store returned nil checkpoint for runId %q", runID)
	}
	if err := checkpoint.ValidateForLoad(*cp); err != nil {
		return nil, err
	}
	if cp.RunID != runID {
		return nil, fmt.Errorf("checkpoint runId %q does not match requested runId %q", cp.RunID, runID)
	}
	if err := a.validateCheckpointSkills(cp.State); err != nil {
		return nil, err
	}
	if err := a.openRunControl(runID); err != nil {
		return nil, err
	}
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer a.closeRunControl(runID)
		if AgentMode(cp.State.Mode) == ModePlanExecute || isPlanExecuteState(cp.State) {
			a.runPlanExecute(ctx, events, runID, Task{
				Input: planExecuteOriginalInput(cp.State),
				Meta:  planExecuteUserMeta(cp.State.Meta),
			}, &cp.State)
			return
		}
		a.runLoop(ctx, events, cp.State, true)
	}()
	return events, nil
}

// Steer queues a user message for an active run. The message becomes a normal
// user turn after outstanding tools finish and before the next model request.
// It returns false when this Agent has no controller or the run is terminal.
func (a *Agent) Steer(runID, steerID, message string) (harness.SteerState, bool) {
	if a == nil || a.config.RunController == nil {
		return harness.SteerState{}, false
	}
	return a.config.RunController.EnqueueSteer(runID, steerID, message)
}

func (a *Agent) openRunControl(runID string) error {
	if a == nil || a.config.RunController == nil {
		return nil
	}
	return a.config.RunController.Open(runID)
}

func (a *Agent) closeRunControl(runID string) {
	if a != nil && a.config.RunController != nil {
		a.config.RunController.Close(runID)
	}
}

func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}

func agentModeForConfig(config Config) AgentMode {
	switch config.Mode {
	case ModeReact, ModeOneshot, ModePlanExecute:
		return config.Mode
	case "":
		if config.Planning == PlanningPlanExecute {
			return ModePlanExecute
		}
	}
	return ModeReact
}

func runStateMode(state harness.RunState, config Config) AgentMode {
	switch AgentMode(state.Mode) {
	case ModeReact, ModeOneshot, ModePlanExecute:
		return AgentMode(state.Mode)
	default:
		return agentModeForConfig(config)
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func runCancellationError(message string) error {
	switch message {
	case "", context.Canceled.Error():
		return context.Canceled
	case context.DeadlineExceeded.Error():
		return context.DeadlineExceeded
	default:
		return fmt.Errorf("%s: %w", message, context.Canceled)
	}
}

type eventEmitter func(EventType, map[string]any) error

type eventPersistenceError struct {
	err error
}

var errApprovalPending = errors.New("approval pending")

func (e *eventPersistenceError) Error() string {
	return e.err.Error()
}

func (e *eventPersistenceError) Unwrap() error {
	return e.err
}

func isEventPersistenceError(err error) bool {
	var persistenceErr *eventPersistenceError
	return errors.As(err, &persistenceErr)
}

func (a *Agent) drainSteers(ctx context.Context, emit eventEmitter, checkpointState func() error, state *harness.RunState) error {
	if a.config.RunController == nil {
		return nil
	}
	steers := a.config.RunController.DrainSteers(state.RunID)
	if len(steers) == 0 {
		return nil
	}
	state.Control.Steers = append(state.Control.Steers, steers...)
	for _, steer := range steers {
		state.Messages = append(state.Messages, harness.MessageState{Role: "user", Content: steer.Message})
	}
	// The message is now part of durable conversation state. Keep no duplicate
	// pending queue in the checkpoint after this safe-boundary commit.
	state.Control.Steers = nil
	if err := checkpointState(); err != nil {
		return err
	}
	for _, steer := range steers {
		if err := emit(EventRequestSteer, map[string]any{
			"steerId": steer.ID,
			"message": steer.Message,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) emit(ctx context.Context, out chan<- Event, eventType EventType, runID string, data map[string]any) error {
	event := NewEvent(eventType, runID, data)
	persistCtx := ctx
	if ctx.Err() != nil {
		persistCtx = context.WithoutCancel(ctx)
	}
	if a.config.Events != nil {
		if event.Seq == 0 {
			latest, err := a.config.Events.LatestSeq(persistCtx, event.RunID())
			if err != nil {
				return a.reportEventPersistenceError(ctx, out, runID, fmt.Errorf("load latest event sequence: %w", err))
			}
			event = event.WithSeq(NextEventSeq(latest))
		}
		if err := a.config.Events.Append(persistCtx, event); err != nil {
			return a.reportEventPersistenceError(ctx, out, runID, fmt.Errorf("append event %s: %w", eventType, err))
		}
	}
	if a.config.Trace != nil {
		_ = a.config.Trace.Emit(persistCtx, traceEvent(event))
	}
	sendEvent(ctx, out, event)
	return nil
}

func (a *Agent) reportEventPersistenceError(ctx context.Context, out chan<- Event, runID string, err error) error {
	sendEvent(ctx, out, NewEvent(EventRunError, runID, map[string]any{"error": err.Error()}))
	return &eventPersistenceError{err: err}
}

func sendEvent(ctx context.Context, out chan<- Event, event Event) {
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
			_ = a.emit(ctx, events, EventRunError, runID, map[string]any{"error": ctx.Err().Error()})
			return
		default:
		}
		if err := a.emit(ctx, events, EventRunStarted, runID, map[string]any{"input": input}); err != nil {
			return
		}
		_ = a.emit(ctx, events, EventRunDone, runID, map[string]any{"output": ""})
	}()
	return events
}

const (
	planExecutePresetMetaKey = "planning.preset"
	planExecuteInputMetaKey  = "planning.input"
	planExecuteStageMetaKey  = "planning.stage"
	planExecuteTerminalKey   = "planning.terminal"
	planExecutePresetValue   = "plan_execute"
	planExecuteStagePlan     = "plan"
	planExecuteStageExecute  = "execute"
	planExecuteStageSummary  = "summary"
)

func (a *Agent) runPlanExecute(ctx context.Context, out chan<- Event, runID string, task Task, resumeState *harness.RunState) {
	emit := eventEmitter(func(eventType EventType, data map[string]any) error {
		return a.emit(ctx, out, eventType, runID, data)
	})
	finish := func(stage string, todos []planner.Todo, eventType EventType, err error) error {
		state := newTaskRunState(runID, task.Input, task.InitialMessages, planExecuteMeta(task.Meta, task.Input, stage))
		state.Mode = string(ModePlanExecute)
		loadCtx := ctx
		if ctx.Err() != nil {
			loadCtx = context.WithoutCancel(ctx)
		}
		if a.config.Checkpoints != nil {
			cp, loadErr := a.config.Checkpoints.Load(loadCtx, runID)
			if loadErr != nil && !errors.Is(loadErr, checkpoint.ErrNotFound) {
				return emit(EventRunError, map[string]any{
					"error": fmt.Sprintf("load latest checkpoint: %v", loadErr),
				})
			}
			if loadErr == nil && isPlanExecuteState(cp.State) {
				state = cp.State
				state.Meta = cloneMap(cp.State.Meta)
			}
		}
		state.Mode = string(ModePlanExecute)
		if state.Meta == nil {
			state.Meta = map[string]any{}
		}
		state.Meta[planExecutePresetMetaKey] = planExecutePresetValue
		state.Meta[planExecuteInputMetaKey] = task.Input
		state.Meta[planExecuteStageMetaKey] = stage
		state.Meta[planExecuteTerminalKey] = true
		state.Meta["error"] = err.Error()
		state.Todos, _ = plannerTodos(todos)
		switch eventType {
		case EventRunCancelled:
			state.Phase = harness.RunPhaseCancelled
			state.Control.Status = harness.RunStatusCancelled
		default:
			state.Phase = harness.RunPhaseFailed
			state.Control.Status = harness.RunStatusFailed
		}
		cp, saveErr := a.saveCheckpointAfterLatest(ctx, state)
		if saveErr != nil {
			return emit(EventRunError, map[string]any{
				"error": fmt.Sprintf("save checkpoint: %v", saveErr),
			})
		}
		if cp.Seq > 0 {
			if emitErr := emit(EventCheckpointCreated, checkpointCreatedPayload(cp)); emitErr != nil {
				return emitErr
			}
		}
		return emit(eventType, map[string]any{"error": err.Error()})
	}
	fail := func(stage string, todos []planner.Todo, err error) {
		if isEventPersistenceError(err) {
			return
		}
		_ = finish(stage, todos, EventRunError, err)
	}
	if a.todos == nil {
		fail(planExecuteStagePlan, nil, fmt.Errorf("todo manager is not configured"))
		return
	}
	if resumeState != nil {
		if err := emit(EventRunResumed, map[string]any{"input": task.Input, "mode": string(ModePlanExecute), "preset": string(PlanningPlanExecute)}); err != nil {
			return
		}
		if planExecuteTerminal(*resumeState) && a.resumeTerminal(emit, *resumeState) {
			return
		}
	} else {
		if err := emit(EventRunStarted, map[string]any{"input": task.Input, "mode": string(ModePlanExecute), "preset": string(PlanningPlanExecute)}); err != nil {
			return
		}
	}

	todos, err := planExecuteTodos(ctx, a.todos, runID, resumeState)
	if err != nil {
		fail(planExecuteStagePlan, nil, err)
		return
	}
	if len(todos) == 0 {
		planInput := task.Input + "\n\n" + planner.PlanPrompt
		planState := newTaskRunState(runID, planInput, task.InitialMessages, planExecuteMeta(task.Meta, task.Input, planExecuteStagePlan))
		resumedPlan := resumeState != nil && planExecuteStage(resumeState.Meta) == planExecuteStagePlan
		if resumedPlan {
			planState = *resumeState
			resumeState = nil
		}
		planState.Mode = string(ModePlanExecute)
		terminal := a.runInternalLoop(ctx, out, planState, resumedPlan)
		if terminal.Type == EventRunError || terminal.Type == EventRunCancelled {
			if terminal.Err != nil {
				return
			}
			_ = finish(planExecuteStagePlan, todos, terminal.Type, errors.New(stringValue(terminal.Data["error"])))
			return
		}
		todos, err = a.todos.List(ctx, runID)
		if err != nil {
			fail(planExecuteStagePlan, todos, err)
			return
		}
	}
	if len(todos) == 0 {
		fail(planExecuteStagePlan, todos, fmt.Errorf("plan_not_created"))
		return
	}

	for {
		current, ok := planner.FirstNonTerminal(todos)
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			if emitErr := emit(EventTaskCancelled, map[string]any{"todoId": current.ID, "error": err.Error()}); emitErr != nil {
				return
			}
			_ = finish(planExecuteStageExecute, todos, EventRunCancelled, err)
			return
		}
		if err := emit(EventTaskStarted, map[string]any{"todoId": current.ID, "content": current.Content}); err != nil {
			return
		}
		inProgress := planner.TodoInProgress
		todos, err = a.todos.Update(ctx, runID, current.ID, planner.Patch{Status: &inProgress})
		if err != nil {
			fail(planExecuteStageExecute, todos, err)
			return
		}
		if err := emit(EventTodoUpdated, map[string]any{"todos": todos}); err != nil {
			return
		}

		executeState := newRunState(runID, taskPrompt(todos, current), planExecuteMeta(task.Meta, task.Input, planExecuteStageExecute))
		executeState.Mode = string(ModePlanExecute)
		if resumeState != nil && planExecuteStage(resumeState.Meta) == planExecuteStageExecute {
			executeState = *resumeState
			resumeState = nil
		}
		executeState.Mode = string(ModePlanExecute)
		terminal := a.runInternalLoop(ctx, out, executeState, true)
		if terminal.Type == EventRunError || terminal.Type == EventRunCancelled {
			if terminal.Err != nil {
				return
			}
			_ = finish(planExecuteStageExecute, todos, terminal.Type, errors.New(stringValue(terminal.Data["error"])))
			return
		}
		todos, err = a.todos.List(ctx, runID)
		if err != nil {
			fail(planExecuteStageExecute, todos, err)
			return
		}
		updated, ok := findTodo(todos, current.ID)
		if !ok {
			fail(planExecuteStageExecute, todos, fmt.Errorf("todo %q disappeared", current.ID))
			return
		}
		if !planner.TerminalStatus(updated.Status) {
			failed := planner.TodoFailed
			notes := "task ended without terminal todo_update"
			todos, err = a.todos.Update(ctx, runID, current.ID, planner.Patch{Status: &failed, Notes: &notes})
			if err != nil {
				fail(planExecuteStageExecute, todos, fmt.Errorf("mark todo %q failed: %w", current.ID, err))
				return
			}
			if err := emit(EventTodoUpdated, map[string]any{"todos": todos}); err != nil {
				return
			}
			if err := emit(EventTaskError, map[string]any{"todoId": current.ID, "error": notes}); err != nil {
				return
			}
			fail(planExecuteStageExecute, todos, errors.New(notes))
			return
		}
		if err := emit(EventTaskDone, map[string]any{"todoId": current.ID, "status": string(updated.Status)}); err != nil {
			return
		}
		if updated.Status == planner.TodoFailed {
			fail(planExecuteStageExecute, todos, fmt.Errorf("todo %q failed", current.ID))
			return
		}
	}

	summaryState := newRunState(runID, summaryPrompt(task.Input, todos), planExecuteMeta(task.Meta, task.Input, planExecuteStageSummary))
	summaryState.Mode = string(ModePlanExecute)
	summaryState.Todos, _ = plannerTodos(todos)
	summaryState.Phase = harness.RunPhaseFinalizing
	summaryState.Control.Status = harness.RunStatusModelStreaming
	if resumeState != nil && planExecuteStage(resumeState.Meta) == planExecuteStageSummary {
		summaryState = *resumeState
		resumeState = nil
	}
	terminal := a.runInternalLoop(ctx, out, summaryState, true)
	if terminal.Type == EventRunError || terminal.Type == EventRunCancelled {
		if terminal.Err != nil {
			return
		}
		_ = emit(terminal.Type, terminal.Data)
		return
	}
	_ = emit(EventRunDone, map[string]any{"output": stringValue(terminal.Data["output"]), "todos": todos})
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

func planExecuteTerminal(state harness.RunState) bool {
	if terminal, ok := state.Meta[planExecuteTerminalKey].(bool); ok && terminal {
		return true
	}
	return planExecuteStage(state.Meta) == planExecuteStageSummary &&
		(state.Phase == harness.RunPhaseCompleted || state.Phase == harness.RunPhaseFailed || state.Phase == harness.RunPhaseCancelled)
}

func planExecuteUserMeta(meta map[string]any) map[string]any {
	out := cloneMap(meta)
	delete(out, planExecutePresetMetaKey)
	delete(out, planExecuteInputMetaKey)
	delete(out, planExecuteStageMetaKey)
	delete(out, planExecuteTerminalKey)
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
	return newTaskRunState(runID, input, nil, meta)
}

func newTaskRunState(runID, input string, initial []model.Message, meta map[string]any) harness.RunState {
	now := time.Now().UTC()
	messages := make([]harness.MessageState, 0, len(initial)+1)
	for _, message := range initial {
		messages = append(messages, modelMessageToHarness(message))
	}
	messages = append(messages, harness.MessageState{Role: "user", Content: input})
	return harness.RunState{
		Version:   harness.RunStateVersion,
		RunID:     runID,
		Input:     input,
		Phase:     harness.RunPhaseCreated,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  messages,
		Control:   harness.RunControlState{Status: harness.RunStatusRunning},
		Meta:      cloneMap(meta),
	}
}

func modelMessageToHarness(message model.Message) harness.MessageState {
	return harness.MessageState{
		Role:       message.Role,
		Content:    message.Content,
		Name:       message.Name,
		ToolCallID: message.ToolCallID,
		ToolCalls:  modelToolCallsToHarness(message.ToolCalls),
	}
}

type loopTerminal struct {
	Type EventType
	Data map[string]any
	Err  error
}

func (a *Agent) runLoop(ctx context.Context, out chan<- Event, state harness.RunState, resumed bool) {
	a.runHarnessLoop(ctx, out, state, resumed, false)
}

func (a *Agent) runInternalLoop(ctx context.Context, out chan<- Event, state harness.RunState, resumed bool) loopTerminal {
	return a.runHarnessLoop(ctx, out, state, resumed, true)
}

func (a *Agent) runHarnessLoop(ctx context.Context, out chan<- Event, state harness.RunState, resumed, internal bool) loopTerminal {
	runID := state.RunID
	var captured loopTerminal
	emit := eventEmitter(func(eventType EventType, data map[string]any) error {
		if internal {
			switch eventType {
			case EventRunStarted, EventRunResumed:
				return nil
			case EventRunDone, EventRunError, EventRunCancelled:
				captured = loopTerminal{Type: eventType, Data: cloneMap(data)}
				return nil
			}
		}
		if err := a.emit(ctx, out, eventType, runID, data); err != nil {
			if internal {
				captured = loopTerminal{Type: EventRunError, Data: map[string]any{"error": err.Error()}, Err: err}
			}
			return err
		}
		return nil
	})

	checkpointSeq, err := a.latestCheckpointSeq(ctx, runID)
	if err != nil {
		message := fmt.Sprintf("load latest checkpoint: %v", err)
		_ = emit(EventRunError, map[string]any{"error": message})
		if captured.Type != "" {
			return captured
		}
		return loopTerminal{Type: EventRunError, Data: map[string]any{"error": message}}
	}
	checkpointState := func(checkpointCtx context.Context, current harness.RunState) error {
		if a.config.Checkpoints == nil {
			return nil
		}
		saveCtx := checkpointCtx
		if checkpointCtx.Err() != nil {
			saveCtx = context.WithoutCancel(checkpointCtx)
		}
		cp := a.newCheckpoint(current, checkpointSeq+1)
		if err := a.saveCheckpoint(saveCtx, cp); err != nil {
			return fmt.Errorf("save checkpoint: %w", err)
		}
		checkpointSeq = cp.Seq
		return emit(EventCheckpointCreated, checkpointCreatedPayload(cp))
	}

	runner := harness.Runner{
		MaxSteps: a.config.MaxSteps,
		Mode:     string(runStateMode(state, a.config)),
		Emit: func(eventType harness.RuntimeEvent, data map[string]any) error {
			return emit(EventType(eventType), data)
		},
		Checkpoint: checkpointState,
		CallModel: func(callCtx context.Context, current harness.RunState, choice model.ToolChoice) (harness.MessageState, model.Usage, error) {
			return a.callModel(callCtx, emit, current, choice)
		},
		DurableCallModel: func(callCtx context.Context, current *harness.RunState, choice model.ToolChoice) (harness.MessageState, model.Usage, error) {
			return a.callModelDurable(callCtx, emit, checkpointState, current, choice)
		},
		RunPendingTools: func(callCtx context.Context, current *harness.RunState) error {
			return a.runPendingTools(callCtx, emit, func() error {
				return checkpointState(callCtx, *current)
			}, current)
		},
		DrainSteers: func(callCtx context.Context, current *harness.RunState) error {
			return a.drainSteers(callCtx, emit, func() error {
				return checkpointState(callCtx, *current)
			}, current)
		},
		ResumeWaitingApproval: func(callCtx context.Context, current *harness.RunState) error {
			return a.resumeWaitingApproval(callCtx, emit, func() error {
				return checkpointState(callCtx, *current)
			}, current)
		},
		ResumeTerminal: func(current harness.RunState) bool {
			return a.resumeTerminal(emit, current)
		},
		IsPause: func(err error) bool {
			return errors.Is(err, errApprovalPending)
		},
		IsPersistenceError: isEventPersistenceError,
	}
	result := runner.Run(ctx, state, resumed)
	if result.Type == "" && captured.Type != "" {
		return captured
	}
	return loopTerminal{Type: EventType(result.Type), Data: cloneMap(result.Data), Err: result.Err}
}

func (a *Agent) latestCheckpointSeq(ctx context.Context, runID string) (int64, error) {
	if a.config.Checkpoints == nil {
		return 0, nil
	}
	loadCtx := ctx
	if ctx.Err() != nil {
		loadCtx = context.WithoutCancel(ctx)
	}
	cp, err := a.config.Checkpoints.Load(loadCtx, runID)
	if errors.Is(err, checkpoint.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return cp.Seq, nil
}

func newCheckpoint(state harness.RunState, seq int64) checkpoint.Checkpoint {
	now := time.Now().UTC()
	state.UpdatedAt = now
	return checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   state.RunID,
		Seq:     seq,
		State:   state,
		SavedAt: now,
	}
}

const skillFingerprintMetaKey = "zenforge.skills.fingerprint"

func (a *Agent) newCheckpoint(state harness.RunState, seq int64) checkpoint.Checkpoint {
	state.Meta = cloneMap(state.Meta)
	if a.config.Skills != nil {
		if state.Meta == nil {
			state.Meta = map[string]any{}
		}
		state.Meta[skillFingerprintMetaKey] = a.config.Skills.Fingerprint()
	} else {
		delete(state.Meta, skillFingerprintMetaKey)
	}
	return newCheckpoint(state, seq)
}

func (a *Agent) validateCheckpointSkills(state harness.RunState) error {
	rawFingerprint, checkpointHasSkills := state.Meta[skillFingerprintMetaKey]
	checkpointFingerprint, validFingerprint := rawFingerprint.(string)
	if checkpointHasSkills && (!validFingerprint || checkpointFingerprint == "") {
		return fmt.Errorf("resume rejected: checkpoint skills bundle fingerprint is invalid")
	}
	currentHasSkills := a.config.Skills != nil
	switch {
	case checkpointHasSkills && !currentHasSkills:
		return fmt.Errorf("resume rejected: checkpoint requires skills bundle %q, but Config.Skills is not configured", checkpointFingerprint)
	case !checkpointHasSkills && currentHasSkills:
		return fmt.Errorf("resume rejected: checkpoint has no skills bundle, but Config.Skills is configured with %q", a.config.Skills.Fingerprint())
	case checkpointHasSkills && checkpointFingerprint != a.config.Skills.Fingerprint():
		return fmt.Errorf("resume rejected: skills bundle fingerprint changed: checkpoint=%q current=%q", checkpointFingerprint, a.config.Skills.Fingerprint())
	default:
		return nil
	}
}

func checkpointCreatedPayload(cp checkpoint.Checkpoint) map[string]any {
	return map[string]any{
		"checkpointSeq": cp.Seq,
		"version":       cp.Version,
		"phase":         string(cp.State.Phase),
	}
}

func (a *Agent) saveCheckpoint(ctx context.Context, cp checkpoint.Checkpoint) error {
	saveCtx := ctx
	if ctx.Err() != nil {
		saveCtx = context.WithoutCancel(ctx)
	}
	return a.config.Checkpoints.Save(saveCtx, cp)
}

func (a *Agent) saveCheckpointAfterLatest(ctx context.Context, state harness.RunState) (checkpoint.Checkpoint, error) {
	if a.config.Checkpoints == nil {
		return checkpoint.Checkpoint{}, nil
	}
	saveCtx := ctx
	if ctx.Err() != nil {
		saveCtx = context.WithoutCancel(ctx)
	}
	latest, err := a.latestCheckpointSeq(saveCtx, state.RunID)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	cp := a.newCheckpoint(state, latest+1)
	if err := a.saveCheckpoint(saveCtx, cp); err != nil {
		return checkpoint.Checkpoint{}, err
	}
	return cp, nil
}

func validateNoToolAnswer(message harness.MessageState) error {
	if len(message.ToolCalls) > 0 {
		return fmt.Errorf("final no-tool model turn returned %d tool call(s)", len(message.ToolCalls))
	}
	return nil
}

func (a *Agent) resumeTerminal(emit eventEmitter, state harness.RunState) bool {
	switch state.Phase {
	case harness.RunPhaseCompleted:
		_ = emit(EventRunDone, map[string]any{"output": lastAssistantContent(state)})
		return true
	case harness.RunPhaseFailed:
		_ = emit(EventRunError, map[string]any{"error": terminalError(state, "run failed before resume")})
		return true
	case harness.RunPhaseCancelled:
		_ = emit(EventRunCancelled, map[string]any{"error": terminalError(state, "run cancelled before resume")})
		return true
	default:
		return false
	}
}

func (a *Agent) resumeWaitingApproval(ctx context.Context, emit eventEmitter, checkpointState func() error, state *harness.RunState) error {
	if state.Approval.Waiting == nil {
		return nil
	}
	if state.Tool.Active == nil {
		return fmt.Errorf("resume waiting approval requires active tool call")
	}
	call := *state.Tool.Active
	req := approvalRequestFromState(*state.Approval.Waiting, state.RunID, call)
	if err := a.registerApprovalRequest(ctx, req); err != nil {
		return err
	}
	if err := emit(EventApprovalRequested, map[string]any{
		"requestId":  req.ID,
		"toolCallId": call.ID,
		"toolName":   call.Name,
		"operation":  req.Operation,
		"risk":       string(req.Risk),
		"request":    req,
		"resumed":    true,
	}); err != nil {
		return err
	}
	if a.config.Approval == nil {
		return errApprovalPending
	}
	decision, err := a.config.Approval.Request(ctx, req)
	if err != nil {
		return err
	}
	decision = normalizeApprovalDecision(decision)
	if err := resolveApproval(state, req, decision); err != nil {
		return err
	}
	if err := a.persistApprovalGrant(ctx, *state, req, decision); err != nil {
		return err
	}
	if err := checkpointState(); err != nil {
		return err
	}
	resolvedType := EventApprovalResolved
	if decision.Action == approval.DecisionReject && decision.Reason == approval.ErrorExpired {
		resolvedType = EventApprovalExpired
	}
	if err := emit(resolvedType, map[string]any{
		"requestId":  decision.RequestID,
		"toolCallId": call.ID,
		"toolName":   call.Name,
		"action":     string(decision.Action),
		"scope":      string(decision.Scope),
		"reason":     decision.Reason,
		"resumed":    true,
	}); err != nil {
		return err
	}
	if decision.Action == approval.DecisionAbort {
		return approval.NewAbortError(decision.Reason)
	}
	if approval.IsApprovedAction(decision.Action) {
		call.Meta = approval.ApprovedMetadata(call.Meta, req, decision)
		call.Status = harness.ToolCallPending
		call.StartedAt = nil
		state.Tool.Pending = append([]harness.ToolCallState{call}, state.Tool.Pending...)
		state.Tool.Active = nil
		state.Control.Status = harness.RunStatusRunning
		if err := checkpointState(); err != nil {
			return err
		}
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
	if err := checkpointState(); err != nil {
		return err
	}
	if err := emit(EventToolError, map[string]any{
		"toolCallId": call.ID,
		"toolName":   call.Name,
		"error":      result.Error,
		"exitCode":   result.ExitCode,
		"resumed":    true,
	}); err != nil {
		return err
	}
	return nil
}

func (a *Agent) registerApprovalRequest(ctx context.Context, req approval.Request) error {
	registrar, ok := a.config.Approval.(approval.RequestRegistrar)
	if !ok {
		return nil
	}
	if err := registrar.RegisterRequest(ctx, req); err != nil {
		return fmt.Errorf("register approval request: %w", err)
	}
	return nil
}

func (a *Agent) callModel(ctx context.Context, emit eventEmitter, state harness.RunState, choice model.ToolChoice) (harness.MessageState, model.Usage, error) {
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
			if err := emit(EventModelDelta, map[string]any{"textDelta": event.Delta, "step": state.Step}); err != nil {
				return harness.MessageState{}, model.Usage{}, err
			}
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

func (a *Agent) callModelDurable(
	ctx context.Context,
	emit eventEmitter,
	checkpointState func(context.Context, harness.RunState) error,
	state *harness.RunState,
	choice model.ToolChoice,
) (harness.MessageState, model.Usage, error) {
	attempt := &harness.ModelAttempt{
		ID:          fmt.Sprintf("attempt_%d", time.Now().UnixNano()),
		LogicalStep: state.Step,
		Status:      harness.ModelAttemptStarted,
		StartedAt:   time.Now().UTC(),
	}
	if state.Model.Active != nil && state.Model.Active.Status == harness.ModelAttemptSuperseded {
		attempt.ReplacesID = state.Model.Active.ID
		state.Model.Active.ReplacementID = attempt.ID
		state.Model.AppendAttempt(*state.Model.Active)
	}
	state.Model.Active = attempt
	if err := checkpointState(ctx, *state); err != nil {
		return harness.MessageState{}, model.Usage{}, err
	}
	if err := emit(EventModelStarted, attemptEventData(attempt, 0)); err != nil {
		return harness.MessageState{}, model.Usage{}, err
	}
	if attempt.ReplacesID != "" {
		if err := emit(EventModelRestarted, attemptEventData(attempt, 0)); err != nil {
			return harness.MessageState{}, model.Usage{}, err
		}
	}

	stream, err := a.config.Model.Stream(ctx, model.Request{
		Messages:   a.modelMessages(*state),
		Tools:      a.toolSpecs(),
		ToolChoice: choice,
		Meta:       cloneMap(state.Meta),
	})
	if err != nil {
		return harness.MessageState{}, model.Usage{}, err
	}

	for event := range stream {
		if event.Error != nil {
			return harness.MessageState{}, model.Usage{}, event.Error
		}
		if event.Delta == "" && event.Message == nil && len(event.ToolCalls) == 0 && !hasUsage(event.Usage) {
			continue
		}
		offset := len(attempt.TextDraft)
		attempt.ChunkSeq++
		attempt.Status = harness.ModelAttemptStreaming
		attempt.TextDraft += event.Delta
		if len(event.ToolCalls) > 0 {
			attempt.ToolCallsDraft = append(attempt.ToolCallsDraft, modelToolCallsToHarness(event.ToolCalls)...)
		}
		if hasUsage(event.Usage) {
			attempt.ObservedUsage = usageState(event.Usage)
		}
		if event.Message != nil {
			if event.Message.Content != "" {
				attempt.TextDraft = event.Message.Content
			}
			if len(event.Message.ToolCalls) > 0 {
				attempt.ToolCallsDraft = modelToolCallsToHarness(event.Message.ToolCalls)
			}
		}
		if err := checkpointState(ctx, *state); err != nil {
			return harness.MessageState{}, model.Usage{}, err
		}
		data := attemptEventData(attempt, offset)
		if event.Delta != "" {
			data["textDelta"] = event.Delta
			if err := emit(EventModelDelta, data); err != nil {
				return harness.MessageState{}, model.Usage{}, err
			}
		}
		if hasUsage(event.Usage) {
			data["usage"] = map[string]any{
				"promptTokens": event.Usage.PromptTokens, "completionTokens": event.Usage.CompletionTokens,
				"totalTokens": event.Usage.TotalTokens,
			}
			if err := emit(EventModelUsage, data); err != nil {
				return harness.MessageState{}, model.Usage{}, err
			}
		}
		if len(event.ToolCalls) > 0 || (event.Message != nil && len(event.Message.ToolCalls) > 0) {
			data["toolCalls"] = append([]harness.ToolCallSpec(nil), attempt.ToolCallsDraft...)
			if err := emit(EventModelToolCallDraft, data); err != nil {
				return harness.MessageState{}, model.Usage{}, err
			}
		}
	}

	message := harness.MessageState{
		Role:      "assistant",
		Content:   attempt.TextDraft,
		ToolCalls: append([]harness.ToolCallSpec(nil), attempt.ToolCallsDraft...),
	}
	return message, model.Usage{
		PromptTokens:     attempt.ObservedUsage.InputTokens,
		CompletionTokens: attempt.ObservedUsage.OutputTokens,
		TotalTokens:      attempt.ObservedUsage.TotalTokens,
	}, nil
}

func hasUsage(usage model.Usage) bool {
	return usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0
}

func usageState(usage model.Usage) harness.UsageState {
	return harness.UsageState{
		InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
	}
}

func attemptEventData(attempt *harness.ModelAttempt, offset int) map[string]any {
	return map[string]any{
		"attemptId": attempt.ID, "step": attempt.LogicalStep,
		"chunkSeq": attempt.ChunkSeq, "offset": offset,
	}
}

func (a *Agent) runPendingTools(ctx context.Context, emit eventEmitter, checkpointState func() error, state *harness.RunState) error {
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
		if err := checkpointState(); err != nil {
			return err
		}
		if err := emit(EventToolCall, map[string]any{
			"toolCallId": call.ID,
			"toolName":   call.Name,
			"arguments":  tool.RedactJSONArguments(call.Arguments, a.config.ToolArgumentRedaction),
		}); err != nil {
			return err
		}

		result, err := a.invokeToolOrRuntime(ctx, emit, checkpointState, state, call)
		if err != nil && result.Error == "" {
			result = tool.Result{Error: err.Error(), ExitCode: 1}
		}
		if result.Error != "" && result.ExitCode == 0 {
			result.ExitCode = 1
		}
		applySandboxResultState(state, result)
		if req, ok := approval.RequestFromResult(result); ok {
			req = normalizeApprovalRequest(req, *state, call)
			if err := req.Validate(); err != nil {
				return err
			}
			decision, reused := reusedApprovalDecision(*state, req)
			if !reused {
				var lookupErr error
				decision, reused, lookupErr = a.reusedPersistentApproval(ctx, *state, req)
				if lookupErr != nil {
					return lookupErr
				}
			}
			if reused {
				state.ResolveApproval(approvalDecisionState(decision))
				if err := checkpointState(); err != nil {
					return err
				}
				if err := emit(EventApprovalResolved, map[string]any{
					"requestId":  decision.RequestID,
					"toolCallId": call.ID,
					"toolName":   call.Name,
					"action":     string(decision.Action),
					"scope":      string(decision.Scope),
					"reason":     decision.Reason,
					"reused":     true,
				}); err != nil {
					return err
				}
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
				state.SetWaitingApproval(approvalRequestState(req, call))
				if err := checkpointState(); err != nil {
					return err
				}
				if err := a.registerApprovalRequest(ctx, req); err != nil {
					return err
				}
				if err := emit(EventApprovalRequested, map[string]any{
					"requestId":  req.ID,
					"toolCallId": call.ID,
					"toolName":   call.Name,
					"operation":  req.Operation,
					"risk":       string(req.Risk),
					"request":    req,
				}); err != nil {
					return err
				}
				if a.config.Approval == nil {
					return errApprovalPending
				}
				decision, approvalErr := a.config.Approval.Request(ctx, req)
				if approvalErr != nil {
					return approvalErr
				}
				decision = normalizeApprovalDecision(decision)
				if err := resolveApproval(state, req, decision); err != nil {
					return err
				}
				if err := a.persistApprovalGrant(ctx, *state, req, decision); err != nil {
					return err
				}
				if err := checkpointState(); err != nil {
					return err
				}
				resolvedType := EventApprovalResolved
				if decision.Action == approval.DecisionReject && decision.Reason == approval.ErrorExpired {
					resolvedType = EventApprovalExpired
				}
				if err := emit(resolvedType, map[string]any{
					"requestId":  decision.RequestID,
					"toolCallId": call.ID,
					"toolName":   call.Name,
					"action":     string(decision.Action),
					"scope":      string(decision.Scope),
					"reason":     decision.Reason,
				}); err != nil {
					return err
				}
				if decision.Action == approval.DecisionAbort {
					return approval.NewAbortError(decision.Reason)
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
			if err := emit(EventTodoUpdated, map[string]any{
				"todos": result.Structured["todos"],
			}); err != nil {
				return err
			}
		}
		changedPath, workspaceChanged := workspaceChangedPath(call, result)
		if workspaceChanged {
			state.Workspace.DirtyPaths = appendDirtyPath(state.Workspace.DirtyPaths, changedPath)
		}
		state.Messages = append(state.Messages, harness.MessageState{
			Role:       "tool",
			Content:    a.toolResultContent(call.Name, result),
			ToolCallID: call.ID,
			Name:       call.Name,
		})
		state.Control.Status = harness.RunStatusRunning
		if err := checkpointState(); err != nil {
			return err
		}
		if workspaceChanged {
			if err := emit(EventWorkspaceChanged, map[string]any{
				"toolCallId": call.ID,
				"toolName":   call.Name,
				"path":       changedPath,
				"dirtyPaths": append([]string(nil), state.Workspace.DirtyPaths...),
			}); err != nil {
				return err
			}
		}
		if err := emit(eventType, map[string]any{
			"toolCallId": call.ID,
			"toolName":   call.Name,
			"output":     result.Output,
			"error":      result.Error,
			"exitCode":   result.ExitCode,
		}); err != nil {
			return err
		}
	}
	return nil
}

func workspaceChangedPath(call harness.ToolCallState, result tool.Result) (string, bool) {
	if call.Name != "workspace_write" || result.Error != "" || result.ExitCode != 0 {
		return "", false
	}
	path := stringValue(result.Structured["path"])
	if path == "" {
		return "", false
	}
	return path, true
}

func appendDirtyPath(paths []string, path string) []string {
	if path == "" {
		return paths
	}
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

func (a *Agent) invokeToolOrRuntime(ctx context.Context, emit eventEmitter, checkpointState func() error, state *harness.RunState, call harness.ToolCallState) (tool.Result, error) {
	if tasktool.IsTaskTool(call.Name) {
		return a.invokeSubAgentTool(ctx, emit, checkpointState, state, call)
	}
	return a.invokeTool(ctx, *state, call)
}

func (a *Agent) invokeSubAgentTool(ctx context.Context, emit eventEmitter, checkpointState func() error, state *harness.RunState, call harness.ToolCallState) (tool.Result, error) {
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
	req.Options = mergeSubAgentRequestOptions(a.subAgentOptions(), req.Options)
	if req.Options.InheritContext {
		req.Context = cloneMap(state.Meta)
	}
	if err := req.Validate(); err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	orchestrator, err := a.subAgentOrchestrator()
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	startSubtasks(state, req)
	if err := checkpointState(); err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	skipped, runnable := splitSubtasksForRun(*state, req)
	result := subagent.Result{Tasks: skipped}
	var invokeErr error
	if len(runnable) > 0 {
		runReq := req
		runReq.Tasks = runnable
		observer := &parentSubtaskObserver{
			checkpoint: checkpointState,
			emit:       emit,
			parentID:   req.ParentTaskID,
			state:      state,
		}
		runReq.Observer = observer
		runningResult, err := orchestrator.Invoke(ctx, runReq)
		invokeErr = err
		for _, task := range runningResult.Tasks {
			if observer.finishedTask(task.ID) {
				continue
			}
			for _, childEvent := range task.Events {
				if err := observer.SubtaskEvent(ctx, subagent.TaskSpec{ID: task.ID, AgentName: task.AgentName, Name: task.Name}, task.RunID, childEvent); err != nil {
					return tool.Result{Error: err.Error(), ExitCode: 1}, err
				}
			}
			if err := observer.SubtaskFinished(ctx, task); err != nil {
				return tool.Result{Error: err.Error(), ExitCode: 1}, err
			}
		}
		result.Tasks = mergeSubtaskResults(req.Tasks, skipped, runningResult.Tasks)
	}
	applySubtaskResults(state, req.ParentTaskID, result)
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

type parentSubtaskObserver struct {
	mu         sync.Mutex
	checkpoint func() error
	emit       eventEmitter
	parentID   string
	state      *harness.RunState
	finished   map[string]bool
}

func (o *parentSubtaskObserver) SubtaskStarted(_ context.Context, task subagent.TaskSpec) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	setSubtaskRunning(o.state, o.parentID, task)
	if err := o.checkpoint(); err != nil {
		return err
	}
	return o.emit(EventSubtaskStarted, map[string]any{
		"parentRunId": o.state.RunID,
		"subtaskId":   task.ID,
		"agentName":   task.NormalizedAgentName(),
		"name":        task.Name,
	})
}

func (o *parentSubtaskObserver) SubtaskEvent(_ context.Context, task subagent.TaskSpec, childRunID string, event subagent.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.emit(EventSubtaskEvent, map[string]any{
		"parentRunId":    o.state.RunID,
		"subtaskId":      task.ID,
		"agentName":      task.NormalizedAgentName(),
		"childRunId":     childRunID,
		"childEventType": event.Type,
		"childEvent":     event,
	})
}

func (o *parentSubtaskObserver) SubtaskFinished(_ context.Context, result subagent.TaskResult) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	applySubtaskResults(o.state, o.parentID, subagent.Result{Tasks: []subagent.TaskResult{result}})
	if err := o.checkpoint(); err != nil {
		return err
	}
	if o.finished == nil {
		o.finished = make(map[string]bool)
	}
	o.finished[result.ID] = true
	eventType := EventSubtaskDone
	if result.Status == subagent.StatusFailed {
		eventType = EventSubtaskError
	}
	return o.emit(eventType, map[string]any{
		"parentRunId": o.state.RunID,
		"subtaskId":   result.ID,
		"agentName":   result.AgentName,
		"status":      result.Status,
		"output":      result.Output,
		"error":       result.Error,
		"runId":       result.RunID,
	})
}

func (o *parentSubtaskObserver) finishedTask(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.finished[id]
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
		Payload:     cloneMap(req.Payload),
		ExpiresAt:   req.ExpiresAt,
	}
}

func normalizeApprovalRequest(req approval.Request, state harness.RunState, call harness.ToolCallState) approval.Request {
	req.RunID = state.RunID
	req.ToolCallID = call.ID
	req.ToolName = call.Name
	if req.ID == "" {
		req.ID = approval.NewRequestID(state.RunID, call.ID, req.Operation)
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	return req
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
		Payload:   cloneMap(decision.Payload),
		DecidedAt: decision.DecidedAt,
	}
}

func normalizeApprovalDecision(decision approval.Decision) approval.Decision {
	if decision.Scope == "" {
		decision.Scope = approval.ScopeOnce
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	}
	return decision
}

func resolveApproval(state *harness.RunState, req approval.Request, decision approval.Decision) error {
	decision = normalizeApprovalDecision(decision)
	if err := approval.ValidateDecisionForRequest(req, decision); err != nil {
		return err
	}
	var scopeKey string
	if approval.IsApprovedAction(decision.Action) && decision.Scope != approval.ScopeOnce {
		var err error
		scopeKey, err = approval.ScopeKey(req, decision.Scope)
		if err != nil {
			return err
		}
	}
	state.ResolveApproval(approvalDecisionState(decision))
	if scopeKey == "" {
		return nil
	}
	grant := harness.ApprovalGrantState{
		RequestID: decision.RequestID,
		Action:    string(decision.Action),
		Scope:     string(decision.Scope),
		GrantedAt: decision.DecidedAt,
	}
	switch decision.Scope {
	case approval.ScopeRun:
		grant.Fingerprint = scopeKey
	case approval.ScopeRule:
		grant.RuleKey = scopeKey
	}
	state.AddApprovalGrant(grant)
	return nil
}

func reusedApprovalDecision(state harness.RunState, req approval.Request) (approval.Decision, bool) {
	for i := len(state.Approval.Grants) - 1; i >= 0; i-- {
		grant := state.Approval.Grants[i]
		scope := approval.DecisionScope(grant.Scope)
		key, err := approval.ScopeKey(req, scope)
		if err != nil {
			continue
		}
		matches := scope == approval.ScopeRun && grant.Fingerprint == key ||
			scope == approval.ScopeRule && grant.RuleKey == key
		if matches && approval.IsApprovedAction(grant.Action) {
			return approval.Decision{
				RequestID: req.ID,
				Action:    approval.DecisionAction(grant.Action),
				Scope:     scope,
				Reason:    approval.ReasonReused,
				DecidedAt: time.Now().UTC(),
			}, true
		}
	}
	return approval.Decision{}, false
}

const (
	approvalTenantStateKey  = "zenforge.approval.tenant"
	approvalSubjectStateKey = "zenforge.approval.subject"
)

func (a *Agent) approvalNamespace(task approval.Namespace) approval.Namespace {
	if task.Tenant != "" || task.Subject != "" {
		return task
	}
	return a.config.ApprovalNamespace
}

func approvalNamespaceMeta(meta map[string]any, namespace approval.Namespace) map[string]any {
	out := cloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	delete(out, approvalTenantStateKey)
	delete(out, approvalSubjectStateKey)
	if namespace.Tenant != "" || namespace.Subject != "" {
		out[approvalTenantStateKey] = namespace.Tenant
		out[approvalSubjectStateKey] = namespace.Subject
	}
	return out
}

func approvalNamespaceFromState(state harness.RunState) approval.Namespace {
	return approval.Namespace{
		Tenant:  stringValue(state.Meta[approvalTenantStateKey]),
		Subject: stringValue(state.Meta[approvalSubjectStateKey]),
	}
}

func grantStoreConfigured(store approval.GrantStore) bool {
	if store == nil {
		return false
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (a *Agent) reusedPersistentApproval(ctx context.Context, state harness.RunState, req approval.Request) (approval.Decision, bool, error) {
	if !grantStoreConfigured(a.config.ApprovalGrants) {
		return approval.Decision{}, false, nil
	}
	namespace := approvalNamespaceFromState(state)
	if err := namespace.Validate(); err != nil {
		return approval.Decision{}, false, err
	}
	ruleKey, ruleOK := approvalString(req.Payload, "ruleKey")
	fingerprint, fingerprintOK := approvalString(req.Payload, "fingerprint")
	if !ruleOK || !fingerprintOK {
		return approval.Decision{}, false, nil
	}
	grant, err := a.config.ApprovalGrants.Get(ctx, namespace, ruleKey, fingerprint)
	if errors.Is(err, approval.ErrGrantNotFound) {
		return approval.Decision{}, false, nil
	}
	if err != nil {
		return approval.Decision{}, false, fmt.Errorf("load persistent approval grant: %w", err)
	}
	if err := grant.Validate(); err != nil {
		return approval.Decision{}, false, fmt.Errorf("invalid persistent approval grant: %w", err)
	}
	if grant.Namespace != namespace || grant.RuleKey != ruleKey || grant.Fingerprint != fingerprint ||
		grant.Expired(time.Now().UTC()) || !approval.IsApprovedAction(grant.Action) {
		return approval.Decision{}, false, nil
	}
	return approval.Decision{
		RequestID: req.ID,
		Action:    grant.Action,
		Scope:     approval.ScopeRule,
		Reason:    approval.ReasonReused,
		DecidedAt: time.Now().UTC(),
	}, true, nil
}

func (a *Agent) persistApprovalGrant(ctx context.Context, state harness.RunState, req approval.Request, decision approval.Decision) error {
	if !grantStoreConfigured(a.config.ApprovalGrants) ||
		decision.Scope != approval.ScopeRule || !approval.IsApprovedAction(decision.Action) ||
		decision.Reason == approval.ReasonReused {
		return nil
	}
	namespace := approvalNamespaceFromState(state)
	if err := namespace.Validate(); err != nil {
		return err
	}
	ruleKey, ruleOK := approvalString(req.Payload, "ruleKey")
	fingerprint, fingerprintOK := approvalString(req.Payload, "fingerprint")
	if !ruleOK || !fingerprintOK {
		return fmt.Errorf("persistent approval rule scope requires exact ruleKey and fingerprint")
	}
	grant := approval.Grant{
		Namespace:   namespace,
		RuleKey:     ruleKey,
		Fingerprint: fingerprint,
		Action:      decision.Action,
		RequestID:   decision.RequestID,
		GrantedAt:   decision.DecidedAt,
	}
	if a.config.ApprovalGrantTTL > 0 {
		expires := grant.GrantedAt.Add(a.config.ApprovalGrantTTL)
		grant.ExpiresAt = &expires
	}
	if err := a.config.ApprovalGrants.Put(ctx, grant); err != nil {
		return fmt.Errorf("persist approval grant: %w", err)
	}
	return nil
}

func approvalString(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key].(string)
	return value, ok && strings.TrimSpace(value) != ""
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
		ID:                   call.ID,
		RunID:                state.RunID,
		Name:                 call.Name,
		Arguments:            call.Arguments,
		Metadata:             toolCallMetadata(state, call.Meta),
		RedactedArgumentKeys: append([]string(nil), a.config.ToolArgumentRedaction...),
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
		Options:  a.subAgentOptions(),
	}), nil
}

func (a *Agent) subAgentOptions() subagent.Options {
	options := a.config.SubAgentOptions
	if options == (subagent.Options{}) {
		options.Parallel = true
	}
	if options.MaxTasks <= 0 {
		options.MaxTasks = 8
	}
	if options.MaxDepth <= 0 {
		options.MaxDepth = 1
	}
	return options
}

func mergeSubAgentRequestOptions(host, request subagent.Options) subagent.Options {
	if request.MaxTasks <= 0 || request.MaxTasks > host.MaxTasks {
		request.MaxTasks = host.MaxTasks
	}
	request.MaxDepth = host.MaxDepth
	request.AllowNested = host.AllowNested
	request.InheritContext = host.InheritContext
	return request
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
	childMeta := cloneMap(task.Metadata)
	if req.Options.InheritContext {
		childMeta = mergeMetadata(childMeta, req.Context)
	}
	childMeta = mergeMetadata(childMeta, spec.Metadata)
	delete(childMeta, skillFingerprintMetaKey)
	if len(task.Files) > 0 {
		childMeta["subagent.files"] = append([]string(nil), task.Files...)
	}
	childMeta["parentRunId"] = req.RunID
	childMeta["subtaskId"] = task.ID
	childMeta["subagent.depth"] = req.Depth + 1
	childConfig := Config{
		Model:        childModel,
		Instructions: childInstructions,
		Tools:        spec.Tools,
		Approval:     a.config.Approval,
		Checkpoints:  a.config.Checkpoints,
		Events:       a.config.Events,
		Trace:        a.config.Trace,
		Workspace:    a.config.Workspace,
		MaxSteps:     maxSteps,
		Planning:     PlanningDisabled,
	}
	if req.Options.AllowNested && req.Depth+1 < req.Options.MaxDepth {
		childConfig.SubAgents = SubAgentsEnabled
		childConfig.SubAgentSpecs = a.config.SubAgentSpecs
		childConfig.SubAgentRegistry = a.config.SubAgentRegistry
		childConfig.SubAgentOrchestrator = a.config.SubAgentOrchestrator
		childConfig.SubAgentRunner = a.config.SubAgentRunner
		childConfig.SubAgentOptions = a.subAgentOptions()
	}
	child := New(childConfig)
	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	events, err := childSubAgentEvents(childCtx, child, a.config.Checkpoints, childRunID, task.Input, childMeta)
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
	var observerErr error
	for event := range events {
		childEvent := subagent.Event{
			Seq:       event.Seq,
			Type:      string(event.Type),
			Timestamp: event.Timestamp,
			Payload:   cloneMap(event.Payload),
		}
		if req.Observer == nil {
			childEvents = append(childEvents, childEvent)
		}
		if req.Observer != nil && observerErr == nil {
			if err := req.NotifySubtaskEvent(ctx, task, childRunID, childEvent); err != nil {
				observerErr = err
				cancelChild()
			}
		}
		if event.Type == EventRunDone {
			output = stringValue(event.Payload["output"])
		}
		if event.Type == EventRunError {
			runErr = fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
		if event.Type == EventRunCancelled {
			message := stringValue(event.Payload["error"])
			switch message {
			case context.Canceled.Error():
				runErr = context.Canceled
			case context.DeadlineExceeded.Error():
				runErr = context.DeadlineExceeded
			default:
				runErr = fmt.Errorf("%s", message)
			}
		}
	}
	if observerErr != nil {
		runErr = observerErr
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
			return nil, fmt.Errorf("load child checkpoint %q: %w", childRunID, err)
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

func setSubtaskRunning(state *harness.RunState, parentID string, task subagent.TaskSpec) {
	idx := findSubtask(state.Subtasks, parentID, task.ID)
	if idx < 0 {
		return
	}
	state.Subtasks[idx].Status = harness.SubtaskRunning
	state.Subtasks[idx].RunID = fmt.Sprintf("%s_sub_%s", state.RunID, task.ID)
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
			RunID:         state.Sandbox.RunID,
			SubtaskID:     state.Sandbox.SubtaskID,
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
	if sandboxStateCleared(result.Metadata) || sandboxStateCleared(result.Meta) {
		state.Sandbox = harness.SandboxState{}
		return
	}
	sandboxState, ok := sandbox.StateFromMetadata(result.Metadata)
	if !ok {
		sandboxState, ok = sandbox.StateFromMetadata(result.Meta)
	}
	if !ok {
		return
	}
	state.Sandbox = harness.SandboxState{
		SessionID:     sandboxState.SessionID,
		RunID:         sandboxState.RunID,
		SubtaskID:     sandboxState.SubtaskID,
		EnvironmentID: sandboxState.EnvironmentID,
		WorkingDir:    sandboxState.WorkingDir,
		Meta:          cloneMap(sandboxState.Metadata),
	}
}

func sandboxStateCleared(metadata map[string]any) bool {
	cleared, _ := metadata[sandbox.MetadataClearStateKey].(bool)
	return cleared
}

func (a *Agent) modelMessages(state harness.RunState) []model.Message {
	messages := make([]model.Message, 0, len(state.Messages)+2)
	if a.config.Instructions != "" {
		messages = append(messages, model.Message{Role: "system", Content: a.config.Instructions})
	}
	if a.skillCatalogPrompt != "" {
		messages = append(messages, model.Message{Role: "system", Content: a.skillCatalogPrompt})
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
	existing := make(map[string]struct{}, len(configuredTools))
	for _, current := range configuredTools {
		existing[strings.ToLower(current.Name())] = struct{}{}
	}
	if a.todos != nil {
		todoTools, err := todotools.Tools(todotools.Config{Manager: a.todos})
		if err != nil {
			return nil, err
		}
		for _, current := range todoTools {
			name := strings.ToLower(current.Name())
			if _, ok := existing[name]; ok {
				continue
			}
			configuredTools = append(configuredTools, current)
			existing[name] = struct{}{}
		}
	}
	if a.config.SubAgents == SubAgentsEnabled || a.config.SubAgentOrchestrator != nil || a.config.SubAgentRegistry != nil || len(a.config.SubAgentSpecs) > 0 {
		taskTools, err := tasktool.Tools(tasktool.Config{MaxTasks: a.subAgentOptions().MaxTasks})
		if err != nil {
			return nil, err
		}
		for _, current := range taskTools {
			name := strings.ToLower(current.Name())
			if _, ok := existing[name]; ok {
				continue
			}
			configuredTools = append(configuredTools, current)
			existing[name] = struct{}{}
		}
	}
	return configuredTools, nil
}

func modelToolCallsToHarness(calls []model.ToolCallSpec) []harness.ToolCallSpec {
	out := make([]harness.ToolCallSpec, 0, len(calls))
	for _, call := range calls {
		out = append(out, harness.ToolCallSpec{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: append(json.RawMessage(nil), call.Arguments...),
		})
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

func (a *Agent) toolResultContent(toolName string, result tool.Result) string {
	if a.config.Skills == nil || !strings.EqualFold(strings.TrimSpace(toolName), "load_skill") ||
		result.Error != "" || result.Output == "" || len(result.Structured) == 0 {
		return toolResultContent(result)
	}
	content := cloneMap(result.Structured)
	content["instructions"] = result.Output
	data, err := json.Marshal(content)
	if err != nil {
		return toolResultContent(result)
	}
	return string(data)
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

func mergeMetadata(base, override map[string]any) map[string]any {
	out := cloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range override {
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

package zenforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
	workflowtool "github.com/feiyu912/zenforge/tools/workflow"
	workflowengine "github.com/feiyu912/zenforge/workflow"
)

// invokeWorkflowTool runs one workflow script. The tool is intercepted here
// for the same reason the task tool is: the script needs the sub-agent
// runtime the agent owns, and the tool definition only describes the call.
func (a *Agent) invokeWorkflowTool(ctx context.Context, emit eventEmitter, checkpointState func() error, state *harness.RunState, call harness.ToolCallState) (tool.Result, error) {
	_ = checkpointState
	request, err := workflowtool.Decode(call.Arguments)
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	runner, err := a.workflowRunner(ctx, emit, state, call, request.Meta.Name)
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	engine, err := workflowengine.NewWithLimits(runner, a.config.WorkflowLimits)
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	engineRequest := request.Engine()
	engineRequest.Observer = workflowProgressObserver{
		emit:        emit,
		parentRunID: state.RunID,
		toolCallID:  call.ID,
		workflow:    request.Meta.Name,
	}
	result, runErr := engine.Run(ctx, engineRequest)
	return workflowToolOutcome(request, result, runErr)
}

// workflowToolOutcome renders a finished run for the model: the reference's
// text shape for the reading model, and structured content for a host that
// wants to branch on the outcome.
func workflowToolOutcome(request workflowtool.Request, result workflowengine.Result, runErr error) (tool.Result, error) {
	value := result.Value
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	structured := map[string]any{
		"workflow":      request.Meta.Name,
		"agentsStarted": result.AgentsStarted,
		"stopReason":    string(result.StopReason),
		"value":         value,
	}
	if runErr != nil {
		structured["error"] = runErr.Error()
		if code, ok := workflowengine.ErrorCodeOf(runErr); ok {
			structured["code"] = string(code)
		}
		output := fmt.Sprintf(
			"workflow %q did not complete (%s after %d agent(s)): %s",
			request.Meta.Name, result.StopReason, result.AgentsStarted, runErr,
		)
		return tool.Result{Output: output, Structured: structured, Error: runErr.Error(), ExitCode: 1}, runErr
	}
	output := fmt.Sprintf(
		"workflow %q completed (%d agent(s)).\nReturn value:\n%s",
		request.Meta.Name, result.AgentsStarted, string(value),
	)
	return tool.Result{Output: output, Structured: structured}, nil
}

// workflowRunner implements the engine's child-agent seam over the harness's
// sub-agent runtime.
type workflowRunner struct {
	orchestrator subagent.Orchestrator
	agentName    string
	options      subagent.Options
	parentRunID  string
	parentStep   int
	toolCallID   string
	depth        int
	workflowName string
	context      map[string]any
	progress     subagent.Observer
	// resolveModel turns an agent() option into an adapter. It is nil on a
	// host that cannot reach a second model, and the runner then refuses a
	// named model instead of quietly running the child on the host's own.
	resolveModel func(provider, model string) (model.Model, error)
	seq          atomic.Int64
}

// workflowRunner builds the runner for one workflow tool call.
func (a *Agent) workflowRunner(ctx context.Context, emit eventEmitter, state *harness.RunState, call harness.ToolCallState, workflowName string) (workflowengine.Runner, error) {
	_ = ctx
	orchestrator, err := a.subAgentOrchestrator()
	if err != nil {
		return nil, err
	}
	agentName, err := a.workflowAgentName()
	if err != nil {
		return nil, err
	}
	depth := 0
	if value, ok := intFromMeta(state.Meta["subagent.depth"]); ok {
		depth = value
	}
	var resolveModel func(provider, model string) (model.Model, error)
	if a.config.ModelResolver != nil {
		resolveModel = a.config.ModelResolver.Resolve
	}
	return &workflowRunner{
		orchestrator: orchestrator,
		agentName:    agentName,
		resolveModel: resolveModel,
		options:      mergeSubAgentRequestOptions(a.subAgentOptions(), subagent.Options{MaxTasks: 1}),
		parentRunID:  state.RunID,
		parentStep:   state.Step,
		toolCallID:   call.ID,
		depth:        depth,
		workflowName: workflowName,
		context:      cloneMap(state.Meta),
		progress: workflowChildObserver{
			emit:        emit,
			parentRunID: state.RunID,
			toolCallID:  call.ID,
			workflow:    workflowName,
		},
	}, nil
}

// workflowAgentName resolves the sub-agent a workflow's agent() calls run as.
// The reference's workflow spawns its host's worker agent; a host with
// several registered sub-agents names one explicitly.
func (a *Agent) workflowAgentName() (string, error) {
	if name := strings.TrimSpace(a.config.WorkflowAgent); name != "" {
		return name, nil
	}
	if registry := a.config.SubAgentRegistry; registry != nil {
		if specs := registry.List(); len(specs) > 0 {
			return specs[0].Name, nil
		}
	}
	if len(a.config.SubAgentSpecs) > 0 {
		return a.config.SubAgentSpecs[0].Name, nil
	}
	return "", errors.New("the workflow tool needs a configured sub-agent to run its agent() calls")
}

// StartChild builds one child run. It returns promptly — the engine calls it
// on its own goroutine and holds a concurrency slot — and every failure it
// can detect here is a start failure the script hears about as fatal, because
// a child that never started cannot be reported as a null item.
func (r *workflowRunner) StartChild(ctx context.Context, request workflowengine.ChildRequest) (workflowengine.Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var childModel model.Model
	if request.Provider != "" || request.Model != "" {
		if r.resolveModel == nil {
			return nil, fmt.Errorf(
				"agent() asked for provider %q, model %q and this host cannot resolve a model by name; remove the option or run the child through the task tool",
				request.Provider, request.Model,
			)
		}
		resolved, err := r.resolveModel(request.Provider, request.Model)
		if err != nil {
			return nil, fmt.Errorf("resolve agent() model (provider %q, model %q): %w", request.Provider, request.Model, err)
		}
		if resolved == nil {
			return nil, fmt.Errorf(
				"the model resolver returned no model for provider %q, model %q", request.Provider, request.Model)
		}
		childModel = resolved
	}
	seq := r.seq.Add(1)
	metadata := map[string]any{
		"workflow.parentToolCall": r.toolCallID,
		"workflow.name":           r.workflowName,
		"workflow.label":          request.Label,
	}
	if request.Phase != "" {
		metadata["workflow.phase"] = request.Phase
	}
	task := subagent.TaskSpec{
		ID:        fmt.Sprintf("%s_agent_%d", r.toolCallID, seq),
		AgentName: r.agentName,
		Name:      request.Label,
		Input:     workflowChildInput(request),
		Metadata:  metadata,
		// The resolved adapter rides on the task, so a workflow child runs on
		// the model the script named and every other child is unaffected.
		Model: childModel,
	}
	childRequest := subagent.Request{
		RunID:        r.parentRunID,
		ParentStep:   r.parentStep,
		ParentTaskID: r.toolCallID,
		ToolCallID:   r.toolCallID,
		Depth:        r.depth,
		Tasks:        []subagent.TaskSpec{task},
		Options:      r.options,
		Observer:     r.progress,
	}
	if r.options.InheritContext {
		childRequest.Context = r.context
	}
	if err := childRequest.Validate(); err != nil {
		return nil, err
	}
	return &workflowChild{ctx: ctx, runner: r, request: childRequest, schema: request.Schema}, nil
}

// workflowChild is one started child run.
type workflowChild struct {
	ctx     context.Context
	runner  *workflowRunner
	request subagent.Request
	schema  json.RawMessage
}

// Result runs the child to settlement through the orchestrator and maps its
// outcome onto the engine's contract: a completed child carries its text (and
// its parsed structured output when the script asked for a schema), and a
// child that did not complete carries its non-completed status with no error,
// which the engine turns into a null item.
func (c *workflowChild) Result() (workflowengine.ChildResult, error) {
	result, err := c.runner.orchestrator.Invoke(c.ctx, c.request)
	if err != nil {
		return workflowengine.ChildResult{}, err
	}
	if len(result.Tasks) == 0 {
		return workflowengine.ChildResult{}, errors.New("the sub-agent runtime returned no result for a workflow child")
	}
	task := result.Tasks[0]
	if task.Status != subagent.StatusCompleted {
		return workflowengine.ChildResult{Output: task.Output, StopReason: task.Status}, nil
	}
	child := workflowengine.ChildResult{Output: task.Output, StopReason: workflowengine.StopReasonCompleted}
	if len(c.schema) > 0 {
		structured, ok := workflowStructuredOutput(task.Output)
		if !ok {
			// The child did not hand back JSON, so it did not satisfy the
			// schema the script asked for: that is a failed item, not a
			// value, and not a reason to kill the run.
			return workflowengine.ChildResult{Output: task.Output, StopReason: subagent.StatusFailed}, nil
		}
		child.Structured = structured
	}
	return child, nil
}

// workflowChildInput asks the child for the structured answer the script
// wanted. The host's child agents have no structured-output contract of their
// own, so the schema is stated in the prompt and the answer is parsed back;
// the engine's validator has already refused a schema outside the subset.
func workflowChildInput(request workflowengine.ChildRequest) string {
	if len(request.Schema) == 0 {
		return request.Prompt
	}
	return request.Prompt + "\n\nAnswer with a single JSON object and nothing else — no prose and no markdown fence — that satisfies this JSON Schema:\n" + string(request.Schema)
}

// workflowStructuredOutput extracts the JSON object a child returned. A fence
// is tolerated because a model that was told not to use one sometimes does
// anyway, and failing the item over it would be a false negative.
func workflowStructuredOutput(output string) (json.RawMessage, bool) {
	text := strings.TrimSpace(output)
	if strings.HasPrefix(text, "```") {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[index+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
		text = strings.TrimSpace(text)
	}
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return nil, false
	}
	candidate := text[start : end+1]
	var object map[string]any
	if err := json.Unmarshal([]byte(candidate), &object); err != nil {
		return nil, false
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(encoded), true
}

// workflowProgressObserver maps the engine's own progress onto dedicated
// workflow events: a phase(), a log(), and the script's bookkeeping view of its
// children. A child *run*'s lifecycle and its streamed events stay on the
// subtask events the sub-agent runtime defines (they carry the child's run id),
// so the two views are separable: subscribing to a script's narration no
// longer means receiving every child's stream.
type workflowProgressObserver struct {
	emit        eventEmitter
	parentRunID string
	toolCallID  string
	workflow    string
}

func (o workflowProgressObserver) WorkflowPhase(title string) {
	o.report(EventWorkflowPhase, map[string]any{"phase": title})
}

func (o workflowProgressObserver) WorkflowLog(message string) {
	o.report(EventWorkflowLog, map[string]any{"message": message})
}

func (o workflowProgressObserver) WorkflowAgentStart(info workflowengine.AgentInfo) {
	o.report(EventWorkflowAgentStarted, map[string]any{
		"seq":   info.Seq,
		"label": info.Label,
		"phase": info.Phase,
	})
}

func (o workflowProgressObserver) WorkflowAgentEnd(info workflowengine.AgentInfo, outcome string) {
	o.report(EventWorkflowAgentDone, map[string]any{
		"seq":     info.Seq,
		"label":   info.Label,
		"phase":   info.Phase,
		"outcome": outcome,
	})
}

// report emits one workflow event carrying the identity every such event
// shares plus the kind's own fields. The fields are flat because the event type
// already says what the event is; there is no second level to unwrap.
func (o workflowProgressObserver) report(eventType EventType, fields map[string]any) {
	if o.emit == nil {
		return
	}
	payload := map[string]any{
		"parentRunId": o.parentRunID,
		"toolCallId":  o.toolCallID,
		"workflow":    o.workflow,
	}
	for key, value := range fields {
		payload[key] = value
	}
	_ = o.emit(eventType, payload)
}

// workflowChildObserver streams one workflow child's lifecycle and events
// into the parent's log. Unlike the task tool's observer it records nothing
// in the parent's run state: a workflow's children are the script's, not the
// parent's plan, so resume replays the script instead of replaying children.
type workflowChildObserver struct {
	emit        eventEmitter
	parentRunID string
	toolCallID  string
	workflow    string
}

func (o workflowChildObserver) SubtaskStarted(ctx context.Context, task subagent.TaskSpec) error {
	if o.emit == nil {
		return nil
	}
	return o.emit(EventSubtaskStarted, map[string]any{
		"parentRunId": o.parentRunID,
		"subtaskId":   task.ID,
		"agentName":   task.NormalizedAgentName(),
		"name":        task.Name,
		"workflow":    o.workflow,
		"toolCallId":  o.toolCallID,
	})
}

func (o workflowChildObserver) SubtaskEvent(ctx context.Context, task subagent.TaskSpec, childRunID string, event subagent.Event) error {
	if o.emit == nil {
		return nil
	}
	return o.emit(EventSubtaskEvent, map[string]any{
		"parentRunId": o.parentRunID,
		"subtaskId":   task.ID,
		"childRunId":  childRunID,
		"type":        event.Type,
		"payload":     event.Payload,
		"workflow":    o.workflow,
	})
}

func (o workflowChildObserver) SubtaskFinished(ctx context.Context, task subagent.TaskResult) error {
	if o.emit == nil {
		return nil
	}
	eventType := EventSubtaskDone
	if task.Status != subagent.StatusCompleted {
		eventType = EventSubtaskError
	}
	return o.emit(eventType, map[string]any{
		"parentRunId": o.parentRunID,
		"subtaskId":   task.ID,
		"childRunId":  task.RunID,
		"status":      task.Status,
		"error":       task.Error,
		"workflow":    o.workflow,
	})
}

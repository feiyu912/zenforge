package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// interrupt reasons handed to Runtime.Interrupt.
type interruptReason string

const (
	interruptScriptTimeout interruptReason = "script-timeout"
	interruptCancelled     interruptReason = "cancelled"
)

// childTask is one in-flight agent() call.
type childTask struct {
	info    AgentInfo
	request ChildRequest
	resolve func(any) error
	reject  func(any) error
}

// childCompletion is a child's outcome, delivered to the run loop.
type childCompletion struct {
	task         *childTask
	result       ChildResult
	err          error
	startFailure bool
}

// execution is one run. It owns the JavaScript runtime, which is only ever
// touched from the goroutine running [Engine.Run]: children report through a
// channel, and every child promise is settled here.
type execution struct {
	engine  *Engine
	limits  Limits
	request Request
	ctx     context.Context

	childCtx       context.Context
	cancelChildren context.CancelFunc

	vm        *goja.Runtime
	parseJSON goja.Callable
	stringify goja.Callable

	completions chan childCompletion
	waiting     []*childTask
	active      int
	started     int
	phase       string

	cancelled bool
	cancelErr *Error

	settled  bool
	result   Result
	runError *Error
}

func newExecution(engine *Engine, request Request, ctx context.Context) (*execution, error) {
	childCtx, cancelChildren := context.WithCancel(ctx)
	vm := goja.New()
	jsonObject, ok := vm.GlobalObject().Get("JSON").(*goja.Object)
	if !ok {
		cancelChildren()
		return nil, newError(CodeInvalidArgument, "workflow runtime has no JSON object")
	}
	parseJSON, ok := goja.AssertFunction(jsonObject.Get("parse"))
	if !ok {
		cancelChildren()
		return nil, newError(CodeInvalidArgument, "workflow runtime has no JSON.parse")
	}
	stringify, ok := goja.AssertFunction(jsonObject.Get("stringify"))
	if !ok {
		cancelChildren()
		return nil, newError(CodeInvalidArgument, "workflow runtime has no JSON.stringify")
	}
	return &execution{
		engine:         engine,
		limits:         engine.limits,
		request:        request,
		ctx:            ctx,
		childCtx:       childCtx,
		cancelChildren: cancelChildren,
		vm:             vm,
		parseJSON:      parseJSON,
		stringify:      stringify,
		completions:    make(chan childCompletion, engine.limits.MaxConcurrentAgents),
	}, nil
}

// run executes the script and drives the run to settlement. Cancellation is
// cooperative at hook boundaries first, the grace timer covers a script parked
// on a promise no hook owns, and the run-context interrupt is the last resort
// for a script that will not come back to a hook boundary at all.
func (x *execution) run() (Result, error) {
	defer x.cancelChildren()
	defer x.vm.ClearInterrupt()

	program, err := goja.Compile("workflow:"+x.request.Meta.Name, wrapScript(x.request.Script), false)
	if err != nil {
		failure := newError(CodeScriptParse, "workflow script does not parse: %v", err)
		return x.failureResult(failure)
	}
	if err := x.installGlobals(); err != nil {
		var failure *Error
		if !errors.As(err, &failure) {
			failure = newError(CodeInvalidArgument, "%v", err)
		}
		return x.failureResult(failure)
	}

	// The sync watchdog bounds the script's synchronous prefix. Cancellation
	// is cooperative first — the loop rejects what it can and the script may
	// settle on its own — and only becomes an interrupt if the script has not
	// come back to a hook boundary within the grace window, which is this
	// engine's equivalent of terminating the reference's worker thread.
	watchdog := time.AfterFunc(x.limits.SyncTimeout, func() {
		x.vm.Interrupt(interruptScriptTimeout)
	})
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-x.ctx.Done():
		case <-watchDone:
			return
		}
		select {
		case <-time.After(x.limits.CancelGrace):
			x.vm.Interrupt(interruptCancelled)
		case <-watchDone:
		}
	}()
	scriptValue, runErr := x.vm.RunProgram(program)
	watchdog.Stop()
	if runErr != nil {
		var interrupted *goja.InterruptedError
		if errors.As(runErr, &interrupted) && interrupted.Value() == interruptScriptTimeout {
			return x.failureResult(newError(
				CodeScriptTimeout,
				"workflow script did not yield within %s; a workflow script must await its hooks instead of blocking",
				x.limits.SyncTimeout,
			))
		}
		return x.failureResult(x.failureFromReason(x.reasonValue(runErr), renderThrown(runErr)))
	}

	x.awaitSettled(scriptValue, func(value goja.Value) {
		materialized, materializeErr := x.materialize(value)
		if materializeErr != nil {
			x.settle(StopError, nil, materializeErr.Message)
			x.runError = materializeErr
			return
		}
		x.settle(StopCompleted, materialized, "")
	}, func(reason goja.Value) {
		if x.cancelled {
			x.settle(StopCancelled, nil, x.cancelErr.Message)
			return
		}
		message := renderReason(reason)
		x.settle(StopError, nil, message)
		x.runError = x.failureFromReason(reason, message)
	})

	x.drive()
	return x.outcome()
}

// failureResult records a failure raised before the script produced a value.
func (x *execution) failureResult(failure *Error) (Result, error) {
	return Result{
		StopReason:    StopError,
		Error:         failure.Message,
		AgentsStarted: x.started,
	}, failure
}

// outcome maps the settled state onto the caller's pair.
func (x *execution) outcome() (Result, error) {
	switch x.result.StopReason {
	case StopCompleted:
		return x.result, nil
	case StopCancelled:
		if x.cancelErr == nil {
			x.cancelErr = newError(CodeCancelled, "workflow run cancelled")
		}
		return x.result, x.cancelErr
	default:
		if x.runError == nil {
			x.runError = newError(CodeScriptError, "%s", x.result.Error)
		}
		return x.result, x.runError
	}
}

// drive waits for the script to settle, feeding child completions back into
// the runtime.
func (x *execution) drive() {
	ctxDone := x.ctx.Done()
	var grace <-chan time.Time
	var graceTimer *time.Timer
	for !x.settled {
		select {
		case completion := <-x.completions:
			x.applyCompletion(completion)
		case <-ctxDone:
			// A cancelled run must not spin on a permanently ready channel.
			ctxDone = nil
			x.cancel("the run context was cancelled")
			if grace == nil {
				graceTimer = time.NewTimer(x.limits.CancelGrace)
				grace = graceTimer.C
			}
		case <-grace:
			// The script never settled after cancellation. It is reported as
			// cancelled anyway; a script parked on a promise no hook owns
			// must not hold the run forever.
			x.settle(StopCancelled, nil, x.cancelErr.Message)
		}
	}
	if graceTimer != nil {
		graceTimer.Stop()
	}
}

// cancel marks the run cancelled and rejects callers that never got a slot.
// A waiting agent() is a hook already in flight, so it observes cancellation
// exactly like the reference's slot waiters do.
func (x *execution) cancel(reason string) {
	if x.cancelled {
		return
	}
	x.cancelled = true
	x.cancelErr = newError(CodeCancelled, "workflow run cancelled: %s", reason)
	x.cancelChildren()
	waiting := x.waiting
	x.waiting = nil
	for _, task := range waiting {
		x.observeAgentEnd(task, OutcomeCancelled)
		x.rejectTask(task, x.errorValue(x.cancelErr))
	}
}

// applyCompletion feeds one settled child back into the script.
func (x *execution) applyCompletion(completion childCompletion) {
	x.active--
	task := completion.task
	switch {
	case x.cancelled:
		x.observeAgentEnd(task, OutcomeCancelled)
		x.rejectTask(task, x.errorValue(x.cancelErr))
	case completion.err != nil:
		x.observeAgentEnd(task, OutcomeFailed)
		code := CodeAgentResult
		message := "child agent run failed: %v"
		if completion.startFailure {
			code = CodeAgentStart
			message = "agent() could not start a child: %v"
		}
		x.rejectTask(task, x.errorValue(newError(code, message, completion.err)))
		// The child failed: everything still waiting keeps its place, and
		// the script is told through the rejected promise.
	case completion.result.StopReason != StopReasonCompleted:
		x.observeAgentEnd(task, OutcomeFailed)
		x.resolveTask(task, goja.Null())
	default:
		x.resolveCompletedChild(task, completion.result)
	}
	x.startWaiting()
}

// resolveCompletedChild resolves one completed child onto the value the
// script asked for: the structured result when a schema was requested, the
// final text otherwise. A requested schema with nothing structured behind it
// is a failed item, not a value.
func (x *execution) resolveCompletedChild(task *childTask, result ChildResult) {
	if len(task.request.Schema) == 0 {
		x.observeAgentEnd(task, OutcomeCompleted)
		x.resolveTask(task, x.vm.ToValue(result.Output))
		return
	}
	if len(result.Structured) == 0 {
		x.observeAgentEnd(task, OutcomeFailed)
		x.resolveTask(task, goja.Null())
		return
	}
	value, err := x.jsonValue(result.Structured)
	if err != nil {
		// Structured output the host cannot even parse is the child failing
		// to satisfy the schema, which is a per-item null like any other
		// missing structured result — not a reason to kill the workflow.
		x.observeAgentEnd(task, OutcomeFailed)
		x.resolveTask(task, goja.Null())
		return
	}
	x.observeAgentEnd(task, OutcomeCompleted)
	x.resolveTask(task, value)
}

// startWaiting launches queued agent() calls while slots are free. The
// children are started in call order, so a wide fan-out cannot starve the
// first call the script made.
func (x *execution) startWaiting() {
	if x.cancelled {
		// Cancellation is the last hook boundary: a hook that runs while the
		// script unwinds must not start new children.
		return
	}
	for x.active < x.limits.MaxConcurrentAgents && len(x.waiting) > 0 {
		task := x.waiting[0]
		x.waiting = x.waiting[1:]
		x.active++
		if observer := x.request.Observer; observer != nil {
			observer.WorkflowAgentStart(task.info)
		}
		// StartChild is called here, on the runtime goroutine, so children
		// reach the runner in call order. It is therefore required to return
		// promptly; the waiting happens in Child.Result on its own goroutine.
		child, err := x.engine.runner.StartChild(x.childCtx, task.request)
		if err != nil {
			x.postCompletion(childCompletion{task: task, err: err, startFailure: true})
			continue
		}
		if child == nil {
			x.postCompletion(childCompletion{task: task, err: errors.New("runner returned no child")})
			continue
		}
		go func() {
			result, resultErr := child.Result()
			x.postCompletion(childCompletion{task: task, result: result, err: resultErr})
		}()
	}
}

// postCompletion hands one child outcome to the run loop. The channel's
// capacity is the concurrency limit and every started child posts at most
// once, so a live run never blocks here; the context arm keeps a child that
// finishes after the run ended from being stranded.
func (x *execution) postCompletion(completion childCompletion) {
	select {
	case x.completions <- completion:
	case <-x.childCtx.Done():
		// The run is over and the loop will not read this; dropping it keeps
		// the goroutine from outliving the run.
	}
}

func (x *execution) resolveTask(task *childTask, value goja.Value) {
	if task.resolve == nil {
		return
	}
	_ = task.resolve(value)
}

func (x *execution) rejectTask(task *childTask, reason goja.Value) {
	if task.reject == nil {
		return
	}
	_ = task.reject(reason)
}

func (x *execution) observeAgentEnd(task *childTask, outcome string) {
	if observer := x.request.Observer; observer != nil {
		observer.WorkflowAgentEnd(task.info, outcome)
	}
}

// settle records the run's outcome once.
func (x *execution) settle(reason StopReason, value json.RawMessage, failure string) {
	if x.settled {
		return
	}
	x.settled = true
	x.result = Result{
		Value:         value,
		StopReason:    reason,
		Error:         failure,
		AgentsStarted: x.started,
	}
}

// wrapScript puts the body inside an async function, which is what makes
// top-level await and `return <value>` the script's contract.
func wrapScript(body string) string {
	return "(async () => {\n" + body + "\n})()"
}

// installGlobals exposes the hooks and the caller's args to the script.
func (x *execution) installGlobals() error {
	x.vm.Set("agent", x.hookAgent)
	x.vm.Set("parallel", x.hookParallel)
	x.vm.Set("pipeline", x.hookPipeline)
	x.vm.Set("phase", x.hookPhase)
	x.vm.Set("log", x.hookLog)
	if len(x.request.Args) == 0 {
		return nil
	}
	value, err := x.parseJSON(goja.Undefined(), x.vm.ToValue(string(x.request.Args)))
	if err != nil {
		return newError(CodeInvalidArgument, "workflow args are not valid JSON: %s", renderThrown(err))
	}
	x.vm.Set("args", value)
	return nil
}

// hookAgent is the agent(prompt, opts?) hook.
func (x *execution) hookAgent(call goja.FunctionCall) goja.Value {
	x.throwIfCancelled()
	promptValue := call.Argument(0)
	if !goja.IsString(promptValue) || promptValue.String() == "" {
		panic(x.errorValue(newError(CodeInvalidArgument, "agent() requires a non-empty prompt string")))
	}
	prompt := promptValue.String()
	options, err := x.readAgentOptions(call.Argument(1))
	if err != nil {
		panic(x.errorValue(err))
	}
	if x.started >= x.limits.MaxTotalAgents {
		panic(x.errorValue(newError(
			CodeAgentCap,
			"this run reached its total agent cap (%d) — a runaway-loop backstop; raise the applicable limit if the scale is intentional",
			x.limits.MaxTotalAgents,
		)))
	}
	x.started++
	label := options.label
	if label == "" {
		label = defaultLabel(prompt)
	}
	phase := options.phase
	if phase == "" {
		phase = x.phase
	}
	info := AgentInfo{Seq: x.started, Label: label, Phase: phase}
	promise, resolve, reject := x.vm.NewPromise()
	x.containRejection(promise)
	task := &childTask{
		info: info,
		request: ChildRequest{
			Prompt:   prompt,
			Schema:   options.schema,
			Provider: options.provider,
			Model:    options.model,
			Label:    label,
			Phase:    phase,
		},
		resolve: resolve,
		reject:  reject,
	}
	x.waiting = append(x.waiting, task)
	x.startWaiting()
	return x.vm.ToValue(promise)
}

// agentOptions is the validated agent() option bag.
type agentOptions struct {
	label    string
	phase    string
	provider string
	model    string
	schema   json.RawMessage
}

var (
	supportedAgentOptions = map[string]bool{
		"label": true, "phase": true, "schema": true, "provider": true, "model": true,
	}
	deferredAgentOptions = map[string]bool{
		"effort": true, "isolation": true, "agentType": true,
	}
)

// readAgentOptions materializes and validates the options bag. Unknown keys
// are refused loudly rather than ignored: a script that asked for something
// the engine cannot honor must not silently get a different run.
func (x *execution) readAgentOptions(raw goja.Value) (agentOptions, *Error) {
	if raw == nil || goja.IsUndefined(raw) || goja.IsNull(raw) {
		return agentOptions{}, nil
	}
	serialized, err := x.stringify(goja.Undefined(), raw)
	if err != nil || goja.IsUndefined(serialized) {
		return agentOptions{}, newError(CodeInvalidArgument, "agent() options must be plain JSON data")
	}
	decoded := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(serialized.String()), &decoded); err != nil {
		return agentOptions{}, newError(CodeInvalidArgument, "agent() options must be an object")
	}
	options := agentOptions{}
	for key, value := range decoded {
		switch {
		case key == "schema":
			if err := ValidateObjectSchema(value); err != nil {
				var schemaErr *Error
				if errors.As(err, &schemaErr) {
					return agentOptions{}, newError(CodeUnsupportedSchema, "agent() %s", schemaErr.Message)
				}
				return agentOptions{}, newError(CodeUnsupportedSchema, "agent() schema is outside the supported subset")
			}
			options.schema = value
		case key == "label" || key == "phase" || key == "provider" || key == "model":
			text, ok := jsonStringValue(value)
			if !ok {
				return agentOptions{}, newError(CodeInvalidArgument, "agent() option %q must be a string", key)
			}
			switch key {
			case "label":
				options.label = text
			case "phase":
				options.phase = text
			case "provider":
				options.provider = text
			case "model":
				options.model = text
			}
		case deferredAgentOptions[key]:
			return agentOptions{}, newError(
				CodeUnsupportedOption,
				"agent() option %q is deferred and not supported by this engine (supported: label, phase, schema, provider, model)",
				key,
			)
		case !supportedAgentOptions[key]:
			return agentOptions{}, newError(
				CodeUnsupportedOption,
				"agent() option %q is not recognized (supported: label, phase, schema, provider, model)",
				key,
			)
		}
	}
	return options, nil
}

// hookParallel is the parallel(thunks) hook: every thunk runs at once, an
// ordinary failure nulls its item, and a fatal error rejects the call.
func (x *execution) hookParallel(call goja.FunctionCall) goja.Value {
	x.throwIfCancelled()
	items, ok := x.arrayValues(call.Argument(0))
	if !ok {
		panic(x.errorValue(newError(CodeInvalidArgument, "parallel() requires an array of zero-argument functions")))
	}
	if len(items) > x.limits.MaxItemsPerCall {
		panic(x.errorValue(x.itemCapError("parallel()", len(items))))
	}
	thunks := make([]goja.Callable, len(items))
	for index, item := range items {
		thunk, ok := goja.AssertFunction(item)
		if !ok {
			panic(x.errorValue(newError(CodeInvalidArgument, "parallel() item %d is not a function", index)))
		}
		thunks[index] = thunk
	}

	promise, resolve, reject := x.vm.NewPromise()
	x.containRejection(promise)
	settled := false
	remaining := len(thunks)
	results := make([]goja.Value, len(thunks))
	finish := func(index int, value goja.Value) {
		if settled {
			return
		}
		results[index] = value
		remaining--
		if remaining == 0 {
			settled = true
			_ = resolve(x.vm.NewArray(valuesOf(results)...))
		}
	}
	fail := func(index int, reason goja.Value) {
		if settled {
			return
		}
		if x.fatalReason(reason) {
			settled = true
			_ = reject(reason)
			return
		}
		finish(index, goja.Null())
	}
	for index, thunk := range thunks {
		index := index
		value, err := thunk(goja.Undefined())
		if err != nil {
			fail(index, x.reasonValue(err))
			continue
		}
		x.awaitSettled(value, func(next goja.Value) {
			finish(index, next)
		}, func(reason goja.Value) {
			fail(index, reason)
		})
	}
	if remaining == 0 && !settled {
		settled = true
		_ = resolve(x.vm.NewArray(valuesOf(results)...))
	}
	return x.vm.ToValue(promise)
}

// hookPipeline is the pipeline(items, ...stages) hook: each item walks the
// stages on its own, with no barrier between stages.
func (x *execution) hookPipeline(call goja.FunctionCall) goja.Value {
	x.throwIfCancelled()
	arguments := call.Arguments
	if len(arguments) == 0 {
		panic(x.errorValue(newError(CodeInvalidArgument, "pipeline() requires an items array")))
	}
	items, ok := x.arrayValues(arguments[0])
	if !ok {
		panic(x.errorValue(newError(CodeInvalidArgument, "pipeline() requires an items array")))
	}
	if len(items) > x.limits.MaxItemsPerCall {
		panic(x.errorValue(x.itemCapError("pipeline()", len(items))))
	}
	rawStages := arguments[1:]
	if len(rawStages) == 0 {
		panic(x.errorValue(newError(CodeInvalidArgument, "pipeline() requires at least one stage function")))
	}
	stages := make([]goja.Callable, len(rawStages))
	for index, rawStage := range rawStages {
		stage, ok := goja.AssertFunction(rawStage)
		if !ok {
			panic(x.errorValue(newError(CodeInvalidArgument, "pipeline() stage %d is not a function", index)))
		}
		stages[index] = stage
	}

	promise, resolve, reject := x.vm.NewPromise()
	x.containRejection(promise)
	settled := false
	remaining := len(items)
	results := make([]goja.Value, len(items))
	for index, item := range items {
		index := index
		item := item
		finishItem := func(value goja.Value) {
			if settled {
				return
			}
			results[index] = value
			remaining--
			if remaining == 0 {
				settled = true
				_ = resolve(x.vm.NewArray(valuesOf(results)...))
			}
		}
		var step func(stageIndex int, value goja.Value)
		var itemFailed func(reason goja.Value)
		itemFailed = func(reason goja.Value) {
			if settled {
				return
			}
			if x.fatalReason(reason) {
				settled = true
				_ = reject(reason)
				return
			}
			// An ordinary error inside a stage drops the item and skips its
			// remaining stages; the rest of the pipeline keeps going.
			finishItem(goja.Null())
		}
		step = func(stageIndex int, value goja.Value) {
			if settled {
				return
			}
			if stageIndex == len(stages) {
				finishItem(value)
				return
			}
			produced, err := stages[stageIndex](goja.Undefined(), value, item, x.vm.ToValue(index))
			if err != nil {
				itemFailed(x.reasonValue(err))
				return
			}
			x.awaitSettled(produced, func(next goja.Value) {
				step(stageIndex+1, next)
			}, itemFailed)
		}
		step(0, item)
	}
	if remaining == 0 && !settled {
		settled = true
		_ = resolve(x.vm.NewArray(valuesOf(results)...))
	}
	return x.vm.ToValue(promise)
}

// hookPhase is the phase(title) hook: it names the phase the following
// agent() calls belong to.
func (x *execution) hookPhase(call goja.FunctionCall) goja.Value {
	x.throwIfCancelled()
	title := call.Argument(0)
	if !goja.IsString(title) || title.String() == "" {
		panic(x.errorValue(newError(CodeInvalidArgument, "phase() requires a non-empty title string")))
	}
	x.phase = title.String()
	if observer := x.request.Observer; observer != nil {
		observer.WorkflowPhase(x.phase)
	}
	return goja.Undefined()
}

// hookLog is the log(message) hook.
func (x *execution) hookLog(call goja.FunctionCall) goja.Value {
	x.throwIfCancelled()
	message := call.Argument(0)
	if !goja.IsString(message) {
		panic(x.errorValue(newError(CodeInvalidArgument, "log() requires a message string")))
	}
	if observer := x.request.Observer; observer != nil {
		observer.WorkflowLog(message.String())
	}
	return goja.Undefined()
}

// awaitSettled delivers a value's settled outcome to one of two callbacks.
// A value that is not promise-like is delivered synchronously; a
// promise-like value goes through its own `then`, so the callback runs on
// the runtime's goroutine while the queue drains.
func (x *execution) awaitSettled(value goja.Value, onValue func(goja.Value), onRejected func(goja.Value)) {
	object, ok := value.(*goja.Object)
	if !ok {
		onValue(value)
		return
	}
	then, ok := goja.AssertFunction(object.Get("then"))
	if !ok {
		onValue(value)
		return
	}
	if _, err := then(object,
		x.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			onValue(call.Argument(0))
			return goja.Undefined()
		}),
		x.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			onRejected(call.Argument(0))
			return goja.Undefined()
		}),
	); err != nil {
		onRejected(x.reasonValue(err))
	}
}

// containRejection attaches a rejection consumer without changing what the
// script sees, so a promise the script drops cannot become an unhandled
// rejection that aborts the run.
func (x *execution) containRejection(promise *goja.Promise) {
	object, ok := x.vm.ToValue(promise).(*goja.Object)
	if !ok {
		return
	}
	then, ok := goja.AssertFunction(object.Get("then"))
	if !ok {
		return
	}
	_, _ = then(object, goja.Undefined(), x.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		return goja.Undefined()
	}))
}

// arrayValues reads a JavaScript array into its elements.
func (x *execution) arrayValues(value goja.Value) ([]goja.Value, bool) {
	object, ok := value.(*goja.Object)
	if !ok || object.ClassName() != "Array" {
		return nil, false
	}
	length := int(object.Get("length").ToInteger())
	if length < 0 {
		return nil, false
	}
	values := make([]goja.Value, 0, length)
	for index := 0; index < length; index++ {
		values = append(values, object.Get(fmt.Sprintf("%d", index)))
	}
	return values, true
}

// jsonValue turns a JSON document into a script value.
func (x *execution) jsonValue(raw json.RawMessage) (goja.Value, error) {
	return x.parseJSON(goja.Undefined(), x.vm.ToValue(string(raw)))
}

// materialize turns the script's return value into plain JSON, which is the
// only shape a result may have.
func (x *execution) materialize(value goja.Value) (json.RawMessage, *Error) {
	if goja.IsUndefined(value) || goja.IsNull(value) {
		return json.RawMessage("null"), nil
	}
	serialized, err := x.stringify(goja.Undefined(), value)
	if err != nil {
		return nil, newError(
			CodeResultUnserializable,
			"the workflow's return value is not plain JSON data — %s. Return only JSON-serializable objects/arrays/scalars.",
			renderReason(x.reasonValue(err)),
		)
	}
	if goja.IsUndefined(serialized) {
		return nil, newError(
			CodeResultUnserializable,
			"the workflow's return value is not plain JSON data. Return only JSON-serializable objects/arrays/scalars.",
		)
	}
	text := serialized.String()
	if !json.Valid([]byte(text)) {
		return nil, newError(
			CodeResultUnserializable,
			"the workflow's return value is not plain JSON data. Return only JSON-serializable objects/arrays/scalars.",
		)
	}
	return json.RawMessage(text), nil
}

// failureFromReason keeps the engine's own failure code when a marked hook
// failure reaches the top level uncaught: the message is what the reference
// reports, and the code is what this package can still tell the caller.
func (x *execution) failureFromReason(reason goja.Value, message string) *Error {
	if object, ok := reason.(*goja.Object); ok {
		if code := object.Get("code"); code != nil && goja.IsString(code) {
			if knownErrorCodes[ErrorCode(code.String())] {
				return newError(ErrorCode(code.String()), "%s", message)
			}
		}
	}
	return newError(CodeScriptError, "%s", message)
}

// errorValue builds the marked error object a hook throws. The marker is what
// makes fatality observable on both sides of the boundary: a script sees a
// normal Error with code/fatal properties, and a combinator can tell a fatal
// refusal from an ordinary error thrown by script code.
func (x *execution) errorValue(failure *Error) *goja.Object {
	object := x.vm.NewGoError(failure)
	_ = object.Set("code", string(failure.Code))
	_ = object.Set("fatal", failure.Fatal())
	return object
}

// fatalReason reports whether a rejection reason is one this engine raised.
func (x *execution) fatalReason(reason goja.Value) bool {
	object, ok := reason.(*goja.Object)
	if !ok {
		return false
	}
	fatal := object.Get("fatal")
	return fatal != nil && fatal.ToBoolean()
}

// reasonValue turns a Go-side throw into the script value it carried.
func (x *execution) reasonValue(err error) goja.Value {
	var exception *goja.Exception
	if errors.As(err, &exception) {
		return exception.Value()
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return x.vm.ToValue(interrupted.String())
	}
	return x.vm.NewGoError(err)
}

// throwIfCancelled makes cancellation the next hook boundary: a script that
// caught one cancelled rejection cannot keep working through another hook.
func (x *execution) throwIfCancelled() {
	if x.cancelled {
		panic(x.errorValue(x.cancelErr))
	}
}

func (x *execution) itemCapError(hook string, length int) *Error {
	return newError(
		CodeItemCap,
		"%s received %d items — over the per-call cap (%d); split the work or raise MaxItemsPerCall",
		hook, length, x.limits.MaxItemsPerCall,
	)
}

// defaultLabel derives a display label from a prompt the script labelled
// nothing.
func defaultLabel(prompt string) string {
	line := prompt
	if index := strings.IndexByte(prompt, '\n'); index >= 0 {
		line = prompt[:index]
	}
	runes := []rune(line)
	if len(runes) <= 48 {
		return line
	}
	return string(runes[:47]) + "…"
}

// renderThrown renders a Go-side error for a message.
func renderThrown(err error) string {
	var exception *goja.Exception
	if errors.As(err, &exception) {
		return renderReason(exception.Value())
	}
	return err.Error()
}

// renderReason renders a thrown script value for a message without trusting
// it: a value that cannot be rendered still produces a sentence.
func renderReason(reason goja.Value) string {
	if reason == nil {
		return "unknown error"
	}
	if object, ok := reason.(*goja.Object); ok {
		if message := object.Get("message"); message != nil && !goja.IsUndefined(message) {
			if text := message.String(); text != "" {
				return text
			}
		}
	}
	if text := reason.String(); text != "" {
		return text
	}
	return "unknown error"
}

func valuesOf(values []goja.Value) []any {
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	return items
}

func jsonStringValue(raw json.RawMessage) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}

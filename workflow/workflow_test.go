package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// textRunner answers every child with a fixed text.
func textRunner(text string) Runner {
	return StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{Output: text, StopReason: StopReasonCompleted}, nil
	})
}

func runScript(t *testing.T, runner Runner, script string) (Result, error) {
	t.Helper()
	return runRequest(t, runner, Limits{}, Request{
		Meta:   Meta{Name: "test-workflow", Description: "a test workflow"},
		Script: script,
	})
}

func runRequest(t *testing.T, runner Runner, limits Limits, request Request) (Result, error) {
	t.Helper()
	engine, err := NewWithLimits(runner, limits)
	if err != nil {
		t.Fatalf("NewWithLimits returned error: %v", err)
	}
	return engine.Run(context.Background(), request)
}

func mustCompleteScript(t *testing.T, runner Runner, script string) Result {
	t.Helper()
	result, err := runScript(t, runner, script)
	return mustComplete(t, result, err)
}

func mustComplete(t *testing.T, result Result, err error) Result {
	t.Helper()
	if err != nil {
		t.Fatalf("Run returned error: %v (result %#v)", err, result)
	}
	if result.StopReason != StopCompleted {
		t.Fatalf("stop reason = %q, want completed (error %q)", result.StopReason, result.Error)
	}
	return result
}

func requireCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("Run returned no error, want %s", want)
	}
	code, ok := ErrorCodeOf(err)
	if !ok {
		t.Fatalf("error %v carries no workflow code", err)
	}
	if code != want {
		t.Fatalf("error code = %s, want %s (%v)", code, want, err)
	}
}

func resultValue(t *testing.T, result Result) any {
	t.Helper()
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(result.Value)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("result value is not JSON (%v): %s", err, result.Value)
	}
	return decoded
}

func TestRunReturnsTheScriptValue(t *testing.T) {
	result := mustCompleteScript(t, textRunner("unused"), `
		const parts = await Promise.all([Promise.resolve(1), Promise.resolve(2)]);
		return { total: parts[0] + parts[1], items: parts };
	`)
	value, ok := resultValue(t, result).(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", resultValue(t, result))
	}
	if value["total"].(json.Number).String() != "3" {
		t.Fatalf("total = %#v", value["total"])
	}
	if result.AgentsStarted != 0 {
		t.Fatalf("agentsStarted = %d, want 0", result.AgentsStarted)
	}
}

func TestRunExposesArgsAndKeepsThemOutOfTheCaller(t *testing.T) {
	args := json.RawMessage(`{"files":["a.go","b.go"]}`)
	result, err := runRequest(t, textRunner("unused"), Limits{}, Request{
		Meta:   Meta{Name: "args-workflow", Description: "reads args"},
		Script: `args.files.push("c.go"); return { count: args.files.length, name: args.files[0] };`,
		Args:   args,
	})
	mustComplete(t, result, err)
	value := resultValue(t, result).(map[string]any)
	if value["count"].(json.Number).String() != "3" {
		t.Fatalf("count = %#v", value["count"])
	}
	if string(args) != `{"files":["a.go","b.go"]}` {
		t.Fatalf("the script mutated the caller's args: %s", args)
	}
}

func TestAgentResolvesTextAndStructuredResults(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		if len(req.Schema) > 0 {
			return ChildResult{
				Structured: json.RawMessage(`{"verdict":"ok"}`),
				StopReason: StopReasonCompleted,
			}, nil
		}
		return ChildResult{Output: "the text answer", StopReason: StopReasonCompleted}, nil
	})
	result := mustCompleteScript(t, runner, `
		const text = await agent("summarize the file");
		const structured = await agent("judge it", {
			label: "judge",
			phase: "review",
			schema: { type: "object", properties: { verdict: { type: "string" } }, required: ["verdict"], additionalProperties: false },
		});
		return { text, verdict: structured.verdict };
	`)
	value := resultValue(t, result).(map[string]any)
	if value["text"] != "the text answer" || value["verdict"] != "ok" {
		t.Fatalf("value = %#v", value)
	}
	if result.AgentsStarted != 2 {
		t.Fatalf("agentsStarted = %d, want 2", result.AgentsStarted)
	}
}

func TestAgentNullsAChildThatDidNotComplete(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{StopReason: "failed"}, nil
	})
	result := mustCompleteScript(t, runner, `
		const answers = [await agent("one"), await agent("two")];
		return { nulls: answers.filter((answer) => answer === null).length };
	`)
	value := resultValue(t, result).(map[string]any)
	if value["nulls"].(json.Number).String() != "2" {
		t.Fatalf("value = %#v", value)
	}
}

func TestAgentNullsARequestedSchemaWithoutAStructuredResult(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{Output: "ignored", StopReason: StopReasonCompleted}, nil
	})
	result := mustCompleteScript(t, runner, `
		const value = await agent("judge", { schema: { type: "object", properties: {}, additionalProperties: false } });
		return { isNull: value === null };
	`)
	if resultValue(t, result).(map[string]any)["isNull"] != true {
		t.Fatalf("value = %#v", resultValue(t, result))
	}
}

func TestAgentNullsAMalformedStructuredResult(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{Structured: json.RawMessage(`{"verdict":`), StopReason: StopReasonCompleted}, nil
	})
	result := mustCompleteScript(t, runner, `
		const value = await agent("judge", { schema: { type: "object", properties: {}, additionalProperties: false } });
		return { isNull: value === null };
	`)
	if resultValue(t, result).(map[string]any)["isNull"] != true {
		t.Fatalf("value = %#v", resultValue(t, result))
	}
}

func TestAgentStartAndResultFailuresAreFatal(t *testing.T) {
	cases := []struct {
		name   string
		runner Runner
		code   ErrorCode
	}{
		{
			name: "start failure",
			runner: runnerFunc(func(ctx context.Context, req ChildRequest) (Child, error) {
				return nil, errors.New("no capacity")
			}),
			code: CodeAgentStart,
		},
		{
			name: "result failure",
			runner: StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
				return ChildResult{}, errors.New("the child log disappeared")
			}),
			code: CodeAgentResult,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := runScript(t, testCase.runner, `return await agent("do work");`)
			requireCode(t, err, testCase.code)
			if result.StopReason != StopError {
				t.Fatalf("stop reason = %q, want error", result.StopReason)
			}
			if result.AgentsStarted != 1 {
				t.Fatalf("agentsStarted = %d, want 1", result.AgentsStarted)
			}
		})
	}
}

func TestAgentCapIsAFatalBackstop(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{Output: "ok", StopReason: StopReasonCompleted}, nil
	})
	result, err := runRequest(t, runner, Limits{MaxTotalAgents: 2}, Request{
		Meta:   Meta{Name: "cap-workflow", Description: "trips the cap"},
		Script: `for (let index = 0; index < 5; index++) { await agent("child " + index); } return "unreachable";`,
	})
	requireCode(t, err, CodeAgentCap)
	if result.AgentsStarted != 2 {
		t.Fatalf("agentsStarted = %d, want 2", result.AgentsStarted)
	}
	if !strings.Contains(result.Error, "total agent cap (2)") {
		t.Fatalf("error = %q", result.Error)
	}
}

// trackingRunner records the order starts arrive in and how many children
// are running at once. The work lives in Result, so StartChild stays prompt
// and the recorded order is the engine's order, not the scheduler's.
type trackingRunner struct {
	mu          sync.Mutex
	order       []string
	inFlight    int
	maxInFlight int
}

func (r *trackingRunner) StartChild(ctx context.Context, req ChildRequest) (Child, error) {
	r.mu.Lock()
	r.order = append(r.order, req.Prompt)
	r.inFlight++
	if r.inFlight > r.maxInFlight {
		r.maxInFlight = r.inFlight
	}
	r.mu.Unlock()
	return &trackingChild{runner: r, ctx: ctx, req: req}, nil
}

type trackingChild struct {
	runner *trackingRunner
	ctx    context.Context
	req    ChildRequest
}

func (c *trackingChild) Result() (ChildResult, error) {
	defer func() {
		c.runner.mu.Lock()
		c.runner.inFlight--
		c.runner.mu.Unlock()
	}()
	select {
	case <-time.After(10 * time.Millisecond):
	case <-c.ctx.Done():
		return ChildResult{}, c.ctx.Err()
	}
	return ChildResult{Output: c.req.Prompt, StopReason: StopReasonCompleted}, nil
}

func TestAgentRunsAtMostMaxConcurrentAgentsInCallOrder(t *testing.T) {
	runner := &trackingRunner{}
	result, err := runRequest(t, runner, Limits{MaxConcurrentAgents: 2}, Request{
		Meta: Meta{Name: "concurrency", Description: "limits fan-out"},
		Script: `
			const started = [];
			for (let index = 0; index < 5; index++) { started.push(agent("child " + index)); }
			const values = await Promise.all(started);
			return values.length;
		`,
	})
	mustComplete(t, result, err)
	if result.AgentsStarted != 5 {
		t.Fatalf("agentsStarted = %d, want 5", result.AgentsStarted)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.maxInFlight > 2 {
		t.Fatalf("max in flight = %d, want at most 2", runner.maxInFlight)
	}
	for index, prompt := range runner.order {
		if prompt != fmt.Sprintf("child %d", index) {
			t.Fatalf("start order = %#v; children must start in call order", runner.order)
		}
	}
}

func TestParallelRunsThunksAndNullsOrdinaryFailures(t *testing.T) {
	result := mustCompleteScript(t, textRunner("answer"), `
		const values = await parallel([
			() => 1,
			() => { throw new Error("ordinary failure"); },
			() => agent("call the model"),
		]);
		return { values };
	`)
	values := resultValue(t, result).(map[string]any)["values"].([]any)
	if len(values) != 3 {
		t.Fatalf("values = %#v", values)
	}
	if values[0].(json.Number).String() != "1" {
		t.Fatalf("values[0] = %#v", values[0])
	}
	if values[1] != nil {
		t.Fatalf("values[1] = %#v, want null", values[1])
	}
	if values[2] != "answer" {
		t.Fatalf("values[2] = %#v", values[2])
	}
}

func TestParallelFatalErrorKillsTheScript(t *testing.T) {
	result, err := runScript(t, textRunner("answer"), `
		try {
			await parallel([() => agent("ok"), () => agent("bad", { effort: "high" })]);
			return "the failure was swallowed";
		} catch (error) {
			return error.code;
		}
	`)
	// The script catches it, so the run completes — and the code it caught is
	// the fatal one, not a per-item null.
	mustComplete(t, result, err)
	if resultValue(t, result) != string(CodeUnsupportedOption) {
		t.Fatalf("value = %#v", resultValue(t, result))
	}
}

func TestPipelineRunsStagesPerItemWithoutBarrier(t *testing.T) {
	result := mustCompleteScript(t, textRunner("unused"), `
		const values = await pipeline(
			["a", "b", "c"],
			(previous, item, index) => previous + item + index,
			(previous) => { if (previous === "bb1") { throw new Error("second item fails"); } return previous.toUpperCase(); },
		);
		return { values };
	`)
	values := resultValue(t, result).(map[string]any)["values"].([]any)
	if values[0] != "AA0" {
		t.Fatalf("values[0] = %#v", values[0])
	}
	if values[1] != nil {
		t.Fatalf("values[1] = %#v, want null", values[1])
	}
	if values[2] != "CC2" {
		t.Fatalf("values[2] = %#v", values[2])
	}
}

func TestPipelineFatalErrorKillsTheScript(t *testing.T) {
	result, err := runScript(t, textRunner("unused"), `
		await pipeline([1], async (item) => item + 1, async (item) => {
			return phase();
		});
		return "unreachable";
	`)
	requireCode(t, err, CodeInvalidArgument)
	if result.StopReason != StopError {
		t.Fatalf("stop reason = %q, want error", result.StopReason)
	}
}

func TestUnsupportedAgentOptionsAreFatal(t *testing.T) {
	cases := map[string]string{
		`await agent("x", { effort: "high" });`:        "deferred",
		`await agent("x", { isolation: "worktree" });`: "deferred",
		`await agent("x", { agentType: "reviewer" });`: "deferred",
		`await agent("x", { unknown: true });`:         "not recognized",
	}
	for script, want := range cases {
		t.Run(want+"/"+script, func(t *testing.T) {
			_, err := runScript(t, textRunner("unused"), `return `+script)
			requireCode(t, err, CodeUnsupportedOption)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want it to mention %q", err, want)
			}
		})
	}
}

func TestAgentOptionTypesAreChecked(t *testing.T) {
	_, err := runScript(t, textRunner("unused"), `return await agent("x", { label: 7 });`)
	requireCode(t, err, CodeInvalidArgument)
}

func TestUnsupportedSchemaIsFatal(t *testing.T) {
	_, err := runScript(t, textRunner("unused"), `return await agent("x", { schema: { type: "object", properties: { a: { type: "string", pattern: "^a" } } } });`)
	requireCode(t, err, CodeUnsupportedSchema)
	if !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("error = %q", err)
	}
}

func TestItemCapIsFatal(t *testing.T) {
	_, err := runRequest(t, textRunner("unused"), Limits{MaxItemsPerCall: 2}, Request{
		Meta:   Meta{Name: "item-cap", Description: "trips the item cap"},
		Script: `return await parallel([() => 1, () => 2, () => 3]);`,
	})
	requireCode(t, err, CodeItemCap)
}

func TestScriptProblemsAreReported(t *testing.T) {
	cases := []struct {
		name   string
		script string
		code   ErrorCode
	}{
		{"parse failure", `return (;`, CodeScriptParse},
		{"throw", `throw new Error("boom");`, CodeScriptError},
		{"rejection", `await Promise.reject(new Error("async boom"));`, CodeScriptError},
		{"unserializable result", `return () => 1;`, CodeResultUnserializable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := runScript(t, textRunner("unused"), testCase.script)
			requireCode(t, err, testCase.code)
			if result.StopReason != StopError {
				t.Fatalf("stop reason = %q, want error", result.StopReason)
			}
			if result.Error == "" {
				t.Fatal("the failure has no message")
			}
		})
	}
}

func TestScriptThatDoesNotYieldIsInterrupted(t *testing.T) {
	start := time.Now()
	result, err := runRequest(t, textRunner("unused"), Limits{SyncTimeout: 50 * time.Millisecond}, Request{
		Meta:   Meta{Name: "spin", Description: "spins"},
		Script: `while (true) {}`,
	})
	requireCode(t, err, CodeScriptTimeout)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the run took %s", elapsed)
	}
	if result.StopReason != StopError {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
}

func TestCancellationReportsCancelled(t *testing.T) {
	release := make(chan struct{})
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		select {
		case <-release:
			return ChildResult{Output: "late", StopReason: StopReasonCompleted}, nil
		case <-ctx.Done():
			return ChildResult{}, ctx.Err()
		}
	})
	engine, err := New(runner)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	result, runErr := engine.Run(ctx, Request{
		Meta:   Meta{Name: "cancel", Description: "cancels"},
		Script: `const first = agent("one"); const second = agent("two"); const third = agent("three"); return await third;`,
	})
	close(release)
	requireCode(t, runErr, CodeCancelled)
	if result.StopReason != StopCancelled {
		t.Fatalf("stop reason = %q, want cancelled", result.StopReason)
	}
	if result.AgentsStarted != 3 {
		t.Fatalf("agentsStarted = %d, want 3", result.AgentsStarted)
	}
}

func TestCancellationRejectsCallersStillWaitingForASlot(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		<-ctx.Done()
		return ChildResult{}, ctx.Err()
	})
	engine, err := NewWithLimits(runner, Limits{MaxConcurrentAgents: 1, CancelGrace: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewWithLimits returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result, runErr := engine.Run(ctx, Request{
		Meta: Meta{Name: "waiting", Description: "rejects callers that never got a slot"},
		Script: `
			const first = agent("one");
			const second = agent("two");
			try {
				await second;
				return "the waiting call resolved";
			} catch (error) {
				return error.code + "/" + error.fatal;
			}
		`,
	})
	// The waiting call is rejected with the marked cancellation, and a script
	// that catches that and settles before the grace timer has completed.
	if runErr != nil {
		t.Fatalf("Run returned error: %v (result %#v)", runErr, result)
	}
	if got := resultValue(t, result); got != string(CodeCancelled)+"/true" {
		t.Fatalf("value = %#v, want the CANCELLED marker", got)
	}
	if result.AgentsStarted != 2 {
		t.Fatalf("agentsStarted = %d, want 2", result.AgentsStarted)
	}
}

func TestCancellationGraceSettlesAScriptParkedOnItsOwnPromise(t *testing.T) {
	engine, err := NewWithLimits(textRunner("unused"), Limits{CancelGrace: 250 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewWithLimits returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	result, runErr := engine.Run(ctx, Request{
		Meta:   Meta{Name: "parked", Description: "parks forever"},
		Script: `await new Promise(() => {}); return "never";`,
	})
	requireCode(t, runErr, CodeCancelled)
	if result.StopReason != StopCancelled {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the grace timer never fired (%s)", elapsed)
	}
}

func TestChildContextIsCancelledWhenTheRunEnds(t *testing.T) {
	var (
		mu       sync.Mutex
		childCtx context.Context
	)
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		mu.Lock()
		childCtx = ctx
		mu.Unlock()
		return ChildResult{Output: "ok", StopReason: StopReasonCompleted}, nil
	})
	mustCompleteScript(t, runner, `return await agent("one");`)
	mu.Lock()
	captured := childCtx
	mu.Unlock()
	if captured == nil {
		t.Fatal("the child never ran")
	}
	deadline := time.Now().Add(5 * time.Second)
	for captured.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if captured.Err() == nil {
		t.Fatal("the child context outlived the run")
	}
}

// observerFuncs adapts function fields into an Observer.
type observerFuncs struct {
	phase func(string)
	log   func(string)
	start func(AgentInfo)
	end   func(AgentInfo, string)
}

func (o observerFuncs) WorkflowPhase(title string) { o.phase(title) }
func (o observerFuncs) WorkflowLog(message string) { o.log(message) }
func (o observerFuncs) WorkflowAgentStart(info AgentInfo) {
	o.start(info)
}
func (o observerFuncs) WorkflowAgentEnd(info AgentInfo, outcome string) { o.end(info, outcome) }

func TestObserverSeesPhasesLogsAndAgents(t *testing.T) {
	var (
		mu       sync.Mutex
		phases   []string
		logs     []string
		starts   []AgentInfo
		outcomes []string
	)
	observer := observerFuncs{
		phase: func(title string) {
			mu.Lock()
			defer mu.Unlock()
			phases = append(phases, title)
		},
		log: func(message string) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, message)
		},
		start: func(info AgentInfo) {
			mu.Lock()
			defer mu.Unlock()
			starts = append(starts, info)
		},
		end: func(info AgentInfo, outcome string) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, fmt.Sprintf("%d:%s", info.Seq, outcome))
		},
	}
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{Output: "ok", StopReason: StopReasonCompleted}, nil
	})
	result, err := runRequest(t, runner, Limits{}, Request{
		Meta:     Meta{Name: "observed", Description: "reports progress"},
		Observer: observer,
		Script: `
			phase("first");
			log("starting");
			await agent("unlabelled child");
			await agent("labelled child", { label: "custom", phase: "second" });
			return "done";
		`,
	})
	mustComplete(t, result, err)
	if result.AgentsStarted != 2 {
		t.Fatalf("agentsStarted = %d", result.AgentsStarted)
	}
	mu.Lock()
	defer mu.Unlock()
	// The per-call phase option scopes the child; only phase() is a progress
	// notification, so the observer hears "first" alone.
	if len(phases) != 1 || phases[0] != "first" {
		t.Fatalf("phases = %#v", phases)
	}
	if len(logs) != 1 || logs[0] != "starting" {
		t.Fatalf("logs = %#v", logs)
	}
	if len(starts) != 2 {
		t.Fatalf("starts = %#v", starts)
	}
	if starts[0].Label != "unlabelled child" || starts[0].Phase != "first" {
		t.Fatalf("first start = %#v", starts[0])
	}
	if starts[1].Label != "custom" || starts[1].Phase != "second" {
		t.Fatalf("second start = %#v", starts[1])
	}
	if len(outcomes) != 2 || outcomes[0] != "1:completed" || outcomes[1] != "2:completed" {
		t.Fatalf("outcomes = %#v", outcomes)
	}
}

func TestAgentOptionsOverrideThePhaseAndLabelDefaults(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []ChildRequest
	)
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return ChildResult{Output: "ok", StopReason: StopReasonCompleted}, nil
	})
	long := strings.Repeat("word ", 30)
	result, err := runRequest(t, runner, Limits{}, Request{
		Meta:   Meta{Name: "defaults", Description: "defaults labels"},
		Script: `phase("named"); return await agent("` + long + `");`,
	})
	mustComplete(t, result, err)
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].Label != defaultLabel(long) {
		t.Fatalf("label = %q", requests[0].Label)
	}
	if runes := []rune(requests[0].Label); len(runes) != 48 || runes[47] != '…' {
		t.Fatalf("label = %q (%d runes)", requests[0].Label, len(runes))
	}
	if requests[0].Phase != "named" {
		t.Fatalf("phase = %q", requests[0].Phase)
	}
}

func TestRunRequiresMetaAndValidArgs(t *testing.T) {
	engine, err := New(textRunner("unused"))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := engine.Run(context.Background(), Request{Script: `return 1;`}); err == nil {
		t.Fatal("a run without meta.name was accepted")
	}
	if _, err := engine.Run(context.Background(), Request{
		Meta:   Meta{Name: "args", Description: "bad args"},
		Script: `return 1;`,
		Args:   json.RawMessage(`{`),
	}); err == nil {
		t.Fatal("a run with invalid args was accepted")
	}
	if _, err := NewWithLimits(nil, Limits{}); err == nil {
		t.Fatal("an engine without a runner was accepted")
	}
}

func TestCancelledContextDoesNotStartTheScript(t *testing.T) {
	engine, err := New(textRunner("unused"))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, runErr := engine.Run(ctx, Request{
		Meta:   Meta{Name: "already-cancelled", Description: "never starts"},
		Script: `throw new Error("the script ran");`,
	})
	requireCode(t, runErr, CodeCancelled)
	if result.StopReason != StopCancelled {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
}

// runnerFunc adapts a StartChild-shaped function into a Runner for the tests
// that need to fail at the start boundary.
type runnerFunc func(ctx context.Context, req ChildRequest) (Child, error)

func (f runnerFunc) StartChild(ctx context.Context, req ChildRequest) (Child, error) {
	return f(ctx, req)
}

func TestAgentNullsAStructuredAnswerThatViolatesTheSchema(t *testing.T) {
	var (
		mu   sync.Mutex
		logs []string
	)
	observer := observerFuncs{
		phase: func(string) {},
		log: func(message string) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, message)
		},
		start: func(AgentInfo) {},
		end:   func(AgentInfo, string) {},
	}
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{
			Structured: json.RawMessage(`{"verdict": 7, "extra": true}`),
			StopReason: StopReasonCompleted,
		}, nil
	})
	result, err := runRequest(t, runner, Limits{}, Request{
		Meta:     Meta{Name: "violating", Description: "one child, one bad answer"},
		Observer: observer,
		Script: `
			const value = await agent("judge", {
				schema: { type: "object", properties: { verdict: { type: "string" } }, required: ["verdict"], additionalProperties: false },
			});
			return { isNull: value === null };
		`,
	})
	mustComplete(t, result, err)
	if resultValue(t, result).(map[string]any)["isNull"] != true {
		t.Fatalf("value = %#v", resultValue(t, result))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	// The null carries no explanation, so the reason has to reach the
	// workflow's own log channel — every violation, not the first one.
	for _, want := range []string{"agent()", "value.verdict must be string", "value.extra is not allowed"} {
		if !strings.Contains(logs[0], want) {
			t.Fatalf("log = %q, want to contain %q", logs[0], want)
		}
	}
}

func TestAgentNullsANonObjectAnswerForAnObjectSchema(t *testing.T) {
	runner := StartFunc(func(ctx context.Context, req ChildRequest) (ChildResult, error) {
		return ChildResult{Structured: json.RawMessage(`[1, 2]`), StopReason: StopReasonCompleted}, nil
	})
	result := mustCompleteScript(t, runner, `
		const value = await agent("judge", { schema: { type: "object", properties: {}, additionalProperties: false } });
		return { isNull: value === null };
	`)
	if resultValue(t, result).(map[string]any)["isNull"] != true {
		t.Fatalf("value = %#v", resultValue(t, result))
	}
}

package zenforge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
)

// routeModel is one adapter a run can be pointed at. It answers every
// conversation in two model steps -- a tool call, then the answer -- so a run
// makes more than one request, which is what makes "the next step re-read the
// adapter" observable at all. It records its requests under a lock, unlike
// scriptedModel, because the whole point of a per-run model is that two runs may
// call two different adapters at the same time.
type routeModel struct {
	answer string

	mu       sync.Mutex
	requests []model.Request
}

func (m *routeModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	if conversationHasToolResult(req.Messages) {
		return &model.Response{Message: model.Message{Role: "assistant", Content: m.answer}}, nil
	}
	return &model.Response{Message: model.Message{Role: "assistant", ToolCalls: routeToolCalls()}}, nil
}

func (m *routeModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	out := make(chan model.Event, 2)
	if conversationHasToolResult(req.Messages) {
		out <- model.Event{Delta: m.answer}
	} else {
		out <- model.Event{Message: &model.Message{Role: "assistant", ToolCalls: routeToolCalls()}}
	}
	close(out)
	return out, nil
}

// served copies the requests this adapter was asked to serve, so an assertion
// can read them without holding the lock.
func (m *routeModel) served() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Request(nil), m.requests...)
}

func (m *routeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// userInputs is the text of every user message this adapter was asked about, in
// order. It is how a test tells whose conversation an adapter received.
func (m *routeModel) userInputs() []string {
	var inputs []string
	for _, request := range m.served() {
		for _, message := range request.Messages {
			if message.Role == "user" {
				inputs = append(inputs, message.Content)
			}
		}
	}
	return inputs
}

func routeToolCalls() []model.ToolCallSpec {
	return []model.ToolCallSpec{{ID: "call_route", Name: "echo", Arguments: json.RawMessage(`{"text":"ok"}`)}}
}

// conversationHasToolResult reports whether a request already carries the result
// of the tool call routeModel answers with first. Scripting off the conversation
// instead of a counter keeps the two runs of a concurrent test independent: a
// shared counter would interleave them.
func conversationHasToolResult(messages []model.Message) bool {
	for _, message := range messages {
		if message.Role == "tool" {
			return true
		}
	}
	return false
}

// swapRouteModel is the shape of the host's own adapter in the serve command: a
// delegating value whose inner adapter an operator's settings change replaces.
// Every call reads the current inner, which is exactly the leak a per-run model
// has to stop.
type swapRouteModel struct {
	mu    sync.Mutex
	inner model.Model
}

func (m *swapRouteModel) set(inner model.Model) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inner = inner
}

func (m *swapRouteModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	m.mu.Lock()
	inner := m.inner
	m.mu.Unlock()
	return inner.Generate(ctx, req)
}

func (m *swapRouteModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	m.mu.Lock()
	inner := m.inner
	m.mu.Unlock()
	return inner.Stream(ctx, req)
}

// swapHostModelTool swaps the host's adapter between the run's two model steps:
// it is the tool the run calls in between them. Doing the swap from the tool is
// what makes the test deterministic instead of a race it hopes to lose.
type swapHostModelTool struct {
	swap func()
}

func (t swapHostModelTool) Name() string { return "echo" }

func (t swapHostModelTool) Description() string { return "Swap the host's model" }

func (t swapHostModelTool) Schema() map[string]any { return nil }

func (t swapHostModelTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	t.swap()
	return tool.Result{Output: "ok"}, nil
}

func routeResolver(byRoute map[string]model.Model, asked *[][2]string, mu *sync.Mutex) ModelResolver {
	return resolverFunc(func(providerName, modelName string) (model.Model, error) {
		if mu != nil && asked != nil {
			mu.Lock()
			*asked = append(*asked, [2]string{providerName, modelName})
			mu.Unlock()
		}
		adapter, ok := byRoute[providerName]
		if !ok {
			return nil, errors.New("this host cannot resolve provider " + providerName)
		}
		return adapter, nil
	})
}

// TestARunResolvesItsModelRouteOnceAndKeepsItForEveryStep pins the per-run
// model: the route a task names is resolved once when the run starts, and every
// later step of that run uses the adapter it resolved -- not a second answer
// from the resolver, and not whatever the host holds now.
func TestARunResolvesItsModelRouteOnceAndKeepsItForEveryStep(t *testing.T) {
	host := &routeModel{answer: "host answer"}
	resolved := &routeModel{answer: "resolved answer"}
	second := &routeModel{answer: "second answer"}
	var mu sync.Mutex
	var asked [][2]string
	agent := New(Config{
		Model: host,
		Tools: []Tool{echoTool{}},
		ModelResolver: resolverFunc(func(providerName, modelName string) (model.Model, error) {
			mu.Lock()
			asked = append(asked, [2]string{providerName, modelName})
			call := len(asked)
			mu.Unlock()
			if call == 1 {
				return resolved, nil
			}
			return second, nil
		}),
	})
	result, err := agent.Run(context.Background(), Task{
		RunID: "run_route_once", Input: "work",
		ModelProvider: "acme", ModelName: "acme-large",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output != "resolved answer" {
		t.Fatalf("output = %q, want the route's own answer", result.Output)
	}
	if len(asked) != 1 || asked[0] != [2]string{"acme", "acme-large"} {
		t.Fatalf("the resolver was asked %#v, want the route exactly once", asked)
	}
	if resolved.count() != 2 {
		t.Fatalf("the run's own model served %d steps, want 2: a later step resolved its model again", resolved.count())
	}
	if second.count() != 0 || host.count() != 0 {
		t.Fatalf("an outranked model ran: resolver %d, host %d", second.count(), host.count())
	}
}

// TestATaskWithItsOwnAdapterUsesItWithoutResolving pins the shape the console
// hands over: it has already built the adapter a session's route names, so the run
// must use that adapter instead of building a second one -- the resolver is never
// asked at all. The route still rides on the task beside it, because a live client
// cannot be serialized and only the route can be resolved again from a checkpoint
// (TestResumeResolvesTheModelRouteTheRunWasFrozenOn).
func TestATaskWithItsOwnAdapterUsesItWithoutResolving(t *testing.T) {
	handed := &routeModel{answer: "handed answer"}
	host := &routeModel{answer: "host answer"}
	agent := New(Config{
		Model: host,
		Tools: []Tool{echoTool{}},
		ModelResolver: resolverFunc(func(providerName, modelName string) (model.Model, error) {
			return nil, errors.New("the resolver was asked for a route the task already built an adapter for")
		}),
	})
	result, err := agent.Run(context.Background(), Task{
		RunID: "run_route_handed", Input: "work",
		Model: handed, ModelProvider: "acme", ModelName: "acme-large",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output != "handed answer" {
		t.Fatalf("output = %q, want the handed adapter's own answer", result.Output)
	}
	if handed.count() != 2 {
		t.Fatalf("the handed adapter served %d steps, want 2", handed.count())
	}
	if host.count() != 0 {
		t.Fatalf("the host's adapter ran %d times, want 0", host.count())
	}
}

// TestATaskThatOnlyHandsOverAnAdapterFreezesNoRoute keeps the durable half honest
// for the embedder that builds its own adapter: the run uses it, and the checkpoint
// records no route, because there is no route to resolve again. A caller that wants
// its run to survive a resume must name the provider and model too.
func TestATaskThatOnlyHandsOverAnAdapterFreezesNoRoute(t *testing.T) {
	checkpoints := checkpointmemory.New()
	handed := &routeModel{answer: "handed answer"}
	agent := New(Config{Model: answerModel{}, Checkpoints: checkpoints})
	stream, err := agent.Stream(context.Background(), Task{
		RunID: "run_route_adapter_only", Input: "work", Model: handed,
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range stream {
	}
	if handed.count() == 0 {
		t.Fatal("the run never reached the adapter it was handed")
	}
	cp, err := checkpoints.Load(context.Background(), "run_route_adapter_only")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if _, recorded := cp.State.Meta[metaModelProvider]; recorded {
		t.Fatalf("an adapter-only run recorded a route it never named: %#v", cp.State.Meta)
	}
}

// TestConcurrentRunsKeepTheirOwnModelRoute is the isolation the console needs:
// two runs started at the same time under different routes each run on their own
// adapter, and neither adapter ever sees the other's conversation.
func TestConcurrentRunsKeepTheirOwnModelRoute(t *testing.T) {
	host := &routeModel{answer: "host answer"}
	first := &routeModel{answer: "first answer"}
	second := &routeModel{answer: "second answer"}
	agent := New(Config{
		Model:         host,
		Tools:         []Tool{echoTool{}},
		ModelResolver: routeResolver(map[string]model.Model{"one": first, "two": second}, nil, nil),
	})
	start := make(chan struct{})
	results := make([]*Result, 2)
	failures := make([]error, 2)
	var wg sync.WaitGroup
	for index, route := range []string{"one", "two"} {
		wg.Add(1)
		go func(index int, route string) {
			defer wg.Done()
			<-start
			results[index], failures[index] = agent.Run(context.Background(), Task{
				RunID: "run_concurrent_" + route, Input: route,
				ModelProvider: route, ModelName: route + "-model",
			})
		}(index, route)
	}
	close(start)
	wg.Wait()

	for index, want := range []string{"first answer", "second answer"} {
		if failures[index] != nil {
			t.Fatalf("run %d returned error: %v", index, failures[index])
		}
		if results[index].Output != want {
			t.Fatalf("run %d output = %q, want %q", index, results[index].Output, want)
		}
	}
	if first.count() != 2 || second.count() != 2 {
		t.Fatalf("the two runs' models served %d and %d steps, want 2 each", first.count(), second.count())
	}
	if host.count() != 0 {
		t.Fatalf("the host adapter ran %d times, want 0", host.count())
	}
	for _, adapter := range []*routeModel{first, second} {
		for _, input := range adapter.userInputs() {
			if input != "one" && input != "two" {
				t.Fatalf("an adapter saw an unexpected conversation: %q", input)
			}
		}
	}
	if inputs := first.userInputs(); len(inputs) == 0 || inputs[0] != "one" {
		t.Fatalf("the first run's adapter saw %#v, want its own conversation", inputs)
	}
	if inputs := second.userInputs(); len(inputs) == 0 || inputs[0] != "two" {
		t.Fatalf("the second run's adapter saw %#v, want its own conversation", inputs)
	}
}

// TestARunningRunIsNotReachedByAHostModelSwap pins the other half of the
// isolation: a run that resolved its own route never reads the host's adapter
// again, so reconfiguring the host in the middle of a run cannot change the
// model that run is using. Before per-run models, the second step read the host
// adapter and the run changed models between steps.
func TestARunningRunIsNotReachedByAHostModelSwap(t *testing.T) {
	before := &routeModel{answer: "before the swap"}
	after := &routeModel{answer: "after the swap"}
	host := &swapRouteModel{inner: before}
	route := &routeModel{answer: "route answer"}
	agent := New(Config{
		Model: host,
		Tools: []Tool{swapHostModelTool{swap: func() { host.set(after) }}},
		ModelResolver: routeResolver(
			map[string]model.Model{"acme": route}, nil, nil),
	})
	result, err := agent.Run(context.Background(), Task{
		RunID: "run_route_swap", Input: "work",
		ModelProvider: "acme", ModelName: "acme-large",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output != "route answer" {
		t.Fatalf("output = %q, want the run's own model's answer", result.Output)
	}
	if route.count() != 2 {
		t.Fatalf("the run's own model served %d steps, want 2", route.count())
	}
	if before.count() != 0 || after.count() != 0 {
		t.Fatalf("the host's adapter was reached mid-run: before %d, after %d", before.count(), after.count())
	}
}

// TestAFreshRunFreezesItsModelRouteIntoDurableMeta pins the durable half of the
// contract: the route a task named is carried in run Meta, which is what a
// checkpoint persists and a resume reads back. A run that named no route records
// none, so the host's configured adapter stays the default rather than being
// written into the log as a choice nobody made.
func TestAFreshRunFreezesItsModelRouteIntoDurableMeta(t *testing.T) {
	checkpoints := checkpointmemory.New()
	route := &routeModel{answer: "route answer"}
	agent := New(Config{
		Model:         &routeModel{answer: "host answer"},
		Tools:         []Tool{echoTool{}},
		Checkpoints:   checkpoints,
		ModelResolver: routeResolver(map[string]model.Model{"acme": route}, nil, nil),
	})
	stream, err := agent.Stream(context.Background(), Task{
		RunID: "run_route_meta", Input: "work",
		ModelProvider: "acme", ModelName: "acme-large",
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range stream {
	}
	cp, err := checkpoints.Load(context.Background(), "run_route_meta")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cp.State.Meta[metaModelProvider] != "acme" || cp.State.Meta[metaModelName] != "acme-large" {
		t.Fatalf("checkpoint meta = %#v, want the named route frozen", cp.State.Meta)
	}

	plain := &routeModel{answer: "host answer"}
	plainAgent := New(Config{Model: plain, Checkpoints: checkpoints})
	stream, err = plainAgent.Stream(context.Background(), Task{RunID: "run_route_meta_plain", Input: "work"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range stream {
	}
	cp, err = checkpoints.Load(context.Background(), "run_route_meta_plain")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if _, recorded := cp.State.Meta[metaModelProvider]; recorded {
		t.Fatalf("a run that named no route recorded one: %#v", cp.State.Meta)
	}
}

// TestResumeResolvesTheModelRouteTheRunWasFrozenOn keeps a resume on the model
// the run started with: the route travels in the checkpoint's Meta, so a host
// holding a different adapter now -- another session's selection before the fix,
// or an operator's settings change -- cannot move a resumed run onto it.
func TestResumeResolvesTheModelRouteTheRunWasFrozenOn(t *testing.T) {
	checkpoints := checkpointmemory.New()
	state := newTaskRunState("run_route_resume", "current query", "", nil, nil, map[string]any{
		metaModelProvider: "acme",
		metaModelName:     "acme-large",
	})
	state.Phase = harness.RunPhaseModel
	state.Control.Status = harness.RunStatusModelStreaming
	if err := checkpoints.Save(context.Background(), checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   state.RunID,
		Seq:     1,
		State:   state,
		SavedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Save checkpoint returned error: %v", err)
	}

	host := &routeModel{answer: "host answer"}
	route := &routeModel{answer: "route answer"}
	var mu sync.Mutex
	var asked [][2]string
	agent := New(Config{
		Model:       host,
		Tools:       []Tool{echoTool{}},
		Checkpoints: checkpoints,
		ModelResolver: resolverFunc(func(providerName, modelName string) (model.Model, error) {
			mu.Lock()
			asked = append(asked, [2]string{providerName, modelName})
			mu.Unlock()
			if providerName != "acme" || modelName != "acme-large" {
				return nil, errors.New("the resume asked for a route the run never named")
			}
			return route, nil
		}),
	})
	events, err := agent.Resume(context.Background(), state.RunID)
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	var output string
	for event := range events {
		if event.Type == EventRunDone {
			output = stringValue(event.Payload["output"])
		}
	}
	if len(asked) != 1 || asked[0] != [2]string{"acme", "acme-large"} {
		t.Fatalf("the resolver was asked %#v, want the frozen route", asked)
	}
	if output != "route answer" {
		t.Fatalf("output = %q, want the frozen route's own answer", output)
	}
	if route.count() == 0 {
		t.Fatal("the resumed run never reached the model it was frozen on")
	}
	if host.count() != 0 {
		t.Fatalf("the resumed run fell back to the host's adapter %d times", host.count())
	}
}

// TestResumeRefusesWhenTheFrozenModelRouteCannotBeResolved keeps an unresolvable
// route a visible resume failure. Falling back to the host's adapter would run
// the conversation on a model the operator never chose, which is the failure the
// per-run model exists to remove.
func TestResumeRefusesWhenTheFrozenModelRouteCannotBeResolved(t *testing.T) {
	checkpoints := checkpointmemory.New()
	state := newTaskRunState("run_route_resume_broken", "current query", "", nil, nil, map[string]any{
		metaModelProvider: "acme",
		metaModelName:     "acme-large",
	})
	state.Phase = harness.RunPhaseModel
	state.Control.Status = harness.RunStatusModelStreaming
	if err := checkpoints.Save(context.Background(), checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   state.RunID,
		Seq:     1,
		State:   state,
		SavedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Save checkpoint returned error: %v", err)
	}
	host := &routeModel{answer: "host answer"}
	agent := New(Config{
		Model:       host,
		Checkpoints: checkpoints,
		ModelResolver: resolverFunc(func(string, string) (model.Model, error) {
			return nil, errUnresolvableForTest
		}),
	})
	_, err := agent.Resume(context.Background(), state.RunID)
	if err == nil {
		t.Fatal("a resume with an unresolvable frozen route was allowed to run on the host's adapter")
	}
	if !strings.Contains(err.Error(), "acme-large") {
		t.Fatalf("error = %q, want the route named", err)
	}
	if host.count() != 0 {
		t.Fatalf("the host's adapter ran %d times after a refused resume", host.count())
	}
}

// TestStreamRefusesATaskModelRouteWithoutAResolver names the gap: a host that
// cannot build an adapter for a named route must say so instead of running the
// task on the adapter it happens to hold.
func TestStreamRefusesATaskModelRouteWithoutAResolver(t *testing.T) {
	agent := New(Config{Model: answerModel{}})
	_, err := agent.Stream(context.Background(), Task{
		RunID: "run_route_no_resolver", Input: "work",
		ModelProvider: "acme", ModelName: "acme-large",
	})
	if err == nil {
		t.Fatal("a task named a route and the run started anyway")
	}
	if !strings.Contains(err.Error(), "ModelResolver") {
		t.Fatalf("error = %q, want the missing seam named", err)
	}
}

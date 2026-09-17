package zenforge

import (
	"context"
	"encoding/json"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools/toolsearch"
)

// deferredProbe is an eager tool that also serves as the deferred
// definition the search activates.
type deferredProbe struct {
	name     string
	deferred bool
}

func (t *deferredProbe) Name() string           { return t.name }
func (t *deferredProbe) Description() string    { return "Remote catalog tool for " + t.name }
func (t *deferredProbe) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *deferredProbe) DeferredLoading() bool  { return t.deferred }
func (t *deferredProbe) Call(context.Context, json.RawMessage, tool.Context) (tool.Result, error) {
	return tool.Result{Output: "remote ok"}, nil
}

func TestAgentHidesDeferredToolsUntilSearchActivatesThem(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	deferred := &deferredProbe{name: "mcp__issues__create", deferred: true}
	eager := &deferredProbe{name: "workspace_read"}
	registry, err := tool.NewRegistry(eager, deferred)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	search, err := toolsearch.New(toolsearch.Config{Source: registry})
	if err != nil {
		t.Fatalf("toolsearch New returned error: %v", err)
	}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurnArgs("call_1", toolsearch.Name, `{"query":"issue"}`),
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []tool.Tool{eager, deferred, search},
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    4,
	})

	stream, err := agent.Stream(ctx, Task{Input: "find the issue tool"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	if len(fakeModel.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(fakeModel.requests))
	}

	first := toolNames(fakeModel.requests[0].Tools)
	if containsName(first, "mcp__issues__create") {
		t.Fatalf("deferred tool was visible before activation: %v", first)
	}
	if !containsName(first, "workspace_read") || !containsName(first, toolsearch.Name) {
		t.Fatalf("eager tools missing from the first request: %v", first)
	}

	second := toolNames(fakeModel.requests[1].Tools)
	if !containsName(second, "mcp__issues__create") {
		t.Fatalf("activated tool missing from the second request: %v", second)
	}

	activations := eventsByType(collected, EventToolsActivated)
	if len(activations) != 1 {
		t.Fatalf("tools.activated events = %d, want 1", len(activations))
	}
	activated, _ := activations[0].Payload["tools"].([]string)
	if len(activated) != 1 || activated[0] != "mcp__issues__create" {
		t.Fatalf("activated tools = %#v", activations[0].Payload["tools"])
	}

	// Activation is durable: a resumed run keeps the loaded schema.
	cp, err := checkpoints.Load(ctx, collected[0].RunID())
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	if cp.State.Meta[metaActiveTools] == nil {
		t.Fatalf("activation was not persisted in run state: %#v", cp.State.Meta)
	}
}

func TestAgentSkipsActivationEventWhenSearchReturnsNothing(t *testing.T) {
	ctx := context.Background()
	deferred := &deferredProbe{name: "mcp__billing__invoice", deferred: true}
	registry, err := tool.NewRegistry(deferred)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	search, err := toolsearch.New(toolsearch.Config{Source: registry})
	if err != nil {
		t.Fatalf("toolsearch New returned error: %v", err)
	}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurnArgs("call_1", toolsearch.Name, `{"query":"nothing-matches"}`),
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []tool.Tool{deferred, search},
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		MaxSteps:    4,
	})
	stream, err := agent.Stream(ctx, Task{Input: "search"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if activations := eventsByType(collected, EventToolsActivated); len(activations) != 0 {
		t.Fatalf("no-match search activated %d tools", len(activations))
	}
	if len(fakeModel.requests) != 2 || containsName(toolNames(fakeModel.requests[1].Tools), "mcp__billing__invoice") {
		t.Fatalf("unsearched tool leaked into the second request: %v", toolNames(fakeModel.requests[1].Tools))
	}
}

func toolNames(specs []model.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

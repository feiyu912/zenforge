package zenforge

import (
	"context"
	"sync"
	"testing"

	"errors"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/subagent"
)

var errUnresolvableForTest = errors.New("no credentials for that provider")

// resolverFunc adapts a function to the Config seam the CLI implements.
type resolverFunc func(provider, name string) (model.Model, error)

func (f resolverFunc) Resolve(provider, name string) (model.Model, error) {
	return f(provider, name)
}

// TestRunChildSubAgentPrefersTheTaskModel pins the selection order: a task's
// own adapter is the most specific thing a caller can say, so it outranks the
// agent spec's and the host's.
func TestRunChildSubAgentPrefersTheTaskModel(t *testing.T) {
	hostModel := &scriptedModel{}
	specModel := &scriptedModel{}
	taskModel := &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "task answer"}}}}}
	agent := New(Config{Model: hostModel})
	result, err := agent.runChildSubAgent(context.Background(),
		subagent.SubAgentSpec{Name: "worker", Model: specModel},
		subagent.TaskSpec{ID: "task_1", Input: "work", Model: taskModel},
		subagent.Request{RunID: "run_task_model", Depth: 0},
	)
	if err != nil {
		t.Fatalf("runChildSubAgent returned error: %v", err)
	}
	if result.Output != "task answer" {
		t.Fatalf("output = %q", result.Output)
	}
	if len(taskModel.requests) != 1 {
		t.Fatalf("the task's model ran %d times, want 1", len(taskModel.requests))
	}
	if len(specModel.requests) != 0 || len(hostModel.requests) != 0 {
		t.Fatalf("an outranked model ran: spec %d, host %d", len(specModel.requests), len(hostModel.requests))
	}
}

// TestWorkflowToolRunsANamedChildOnAResolvedModel covers the whole path a
// workflow script's agent() option takes: the option reaches the resolver as
// written, and the adapter it returns rides on the child's task rather than
// changing anything for a sibling that asked for nothing.
func TestWorkflowToolRunsANamedChildOnAResolvedModel(t *testing.T) {
	fakeModel := workflowScriptModel(t, `{
		"meta": {"name": "named", "description": "One child on another model"},
		"script": "const named = await agent(\"one\", {provider: \"anthropic\", model: \"claude-test\"}); const plain = await agent(\"two\"); return [named, plain];"
	}`)
	resolved := &scriptedModel{}
	var mu sync.Mutex
	var asked [][2]string
	var seen []model.Model
	agent := New(Config{
		Model:     fakeModel,
		SubAgents: SubAgentsEnabled,
		ModelResolver: resolverFunc(func(provider, name string) (model.Model, error) {
			mu.Lock()
			asked = append(asked, [2]string{provider, name})
			mu.Unlock()
			return resolved, nil
		}),
		SubAgentRegistry: subagent.MustRegistry(subagent.SubAgentSpec{Name: "worker"}),
		SubAgentRunner: subagent.RunnerFunc(func(_ context.Context, _ subagent.SubAgentSpec, task subagent.TaskSpec, _ subagent.Request) (subagent.TaskResult, error) {
			mu.Lock()
			seen = append(seen, task.Model)
			mu.Unlock()
			return subagent.TaskResult{Output: "child answer"}, nil
		}),
	})
	events, err := agent.Stream(context.Background(), Task{RunID: "run_named_model", Input: "named"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != [2]string{"anthropic", "claude-test"} {
		t.Fatalf("the resolver was asked %#v", asked)
	}
	if len(seen) != 2 {
		t.Fatalf("children = %d", len(seen))
	}
	// The named child carries the resolved adapter; the one that named nothing
	// carries none, so it still runs on the host's model.
	if seen[0] != model.Model(resolved) {
		t.Fatalf("the named child's model was not the resolved one: %#v", seen[0])
	}
	if seen[1] != nil {
		t.Fatalf("a child that asked for nothing got a model: %#v", seen[1])
	}
}

// TestWorkflowToolReportsAnUnresolvableModel keeps a resolver's failure a
// visible start failure: a child that asked for a model the host cannot build
// must not silently run on the host's own.
func TestWorkflowToolReportsAnUnresolvableModel(t *testing.T) {
	fakeModel := workflowScriptModel(t, `{
		"meta": {"name": "named", "description": "Ask for a model that does not resolve"},
		"script": "return await agent(\"one\", {model: \"no-such-model\"});"
	}`)
	agent := New(Config{
		Model:     fakeModel,
		SubAgents: SubAgentsEnabled,
		ModelResolver: resolverFunc(func(string, string) (model.Model, error) {
			return nil, errUnresolvableForTest
		}),
		SubAgentRegistry: subagent.MustRegistry(subagent.SubAgentSpec{Name: "worker"}),
		SubAgentRunner: subagent.RunnerFunc(func(context.Context, subagent.SubAgentSpec, subagent.TaskSpec, subagent.Request) (subagent.TaskResult, error) {
			t.Fatal("an unresolvable model must not start a child")
			return subagent.TaskResult{}, nil
		}),
	})
	events, err := agent.Stream(context.Background(), Task{RunID: "run_unresolvable", Input: "named"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	second := fakeModel.requests[1]
	toolMessage := second.Messages[len(second.Messages)-1]
	if !contains(toolMessage.Content, "resolve agent() model") || !contains(toolMessage.Content, "no-such-model") {
		t.Fatalf("unexpected failure text: %q", toolMessage.Content)
	}
}

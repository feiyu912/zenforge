package subagent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSpecJSONRoundTripAndValidation(t *testing.T) {
	spec := SubAgentSpec{Name: "researcher", Description: "Reads docs"}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var decoded SubAgentSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded.Name != spec.Name {
		t.Fatalf("decoded = %#v", decoded)
	}
	if err := (TaskSpec{Input: "missing agent"}).Validate(); err == nil {
		t.Fatalf("expected missing agent error")
	}
}

func TestRegistryRegisterLookupList(t *testing.T) {
	registry, err := NewRegistry(SubAgentSpec{Name: "zeta"}, SubAgentSpec{Name: "alpha"})
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	if _, ok := registry.Lookup("ALPHA"); !ok {
		t.Fatalf("expected case-insensitive lookup")
	}
	if got := registry.List()[0].Name; got != "alpha" {
		t.Fatalf("first listed agent = %q", got)
	}
	if err := registry.Register(SubAgentSpec{Name: "alpha"}); err == nil {
		t.Fatalf("expected duplicate error")
	}
}

func TestOrchestratorRunsTasksInStableOrder(t *testing.T) {
	registry := MustRegistry(SubAgentSpec{Name: "researcher"}, SubAgentSpec{Name: "reviewer"})
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: registry,
		Runner: RunnerFunc(func(ctx context.Context, spec SubAgentSpec, task TaskSpec, req Request) (TaskResult, error) {
			return TaskResult{Output: spec.Name + ":" + task.Input}, nil
		}),
	})
	result, err := orchestrator.Invoke(context.Background(), Request{Tasks: []TaskSpec{
		{Agent: "reviewer", Input: "b"},
		{Agent: "researcher", Input: "a"},
	}})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Tasks[0].Output != "reviewer:b" || result.Tasks[1].Output != "researcher:a" {
		t.Fatalf("unstable result order: %#v", result)
	}
}

func TestOrchestratorUnknownAndNested(t *testing.T) {
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "known"}),
		Runner: RunnerFunc(func(ctx context.Context, spec SubAgentSpec, task TaskSpec, req Request) (TaskResult, error) {
			return TaskResult{Output: "ok"}, nil
		}),
	})
	result, err := orchestrator.Invoke(context.Background(), Request{Tasks: []TaskSpec{{Agent: "missing", Input: "x"}}})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Tasks[0].Status != StatusFailed {
		t.Fatalf("expected failed unknown result: %#v", result)
	}
	if _, err := orchestrator.Invoke(context.Background(), Request{Depth: 1, Tasks: []TaskSpec{{Agent: "known", Input: "x"}}}); err == nil {
		t.Fatalf("expected nested guard error")
	}
}

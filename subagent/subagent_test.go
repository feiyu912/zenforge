package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
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

func TestOrchestratorRunsParallelTasksInStableOrder(t *testing.T) {
	registry := MustRegistry(SubAgentSpec{Name: "worker"})
	started := make(chan string, 2)
	release := make(chan struct{})
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: registry,
		Runner: RunnerFunc(func(ctx context.Context, spec SubAgentSpec, task TaskSpec, req Request) (TaskResult, error) {
			started <- task.Input
			select {
			case <-release:
			case <-ctx.Done():
				return TaskResult{}, ctx.Err()
			}
			return TaskResult{Output: task.Input}, nil
		}),
		Options: Options{Parallel: true},
	})

	done := make(chan Result, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := orchestrator.Invoke(context.Background(), Request{Tasks: []TaskSpec{
			{Agent: "worker", Input: "first"},
			{Agent: "worker", Input: "second"},
		}})
		if err != nil {
			errs <- err
			return
		}
		done <- result
	}()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case input := <-started:
			seen[input] = true
		case err := <-errs:
			t.Fatalf("Invoke returned early error: %v", err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for parallel tasks to start; seen=%#v", seen)
		}
	}
	close(release)

	select {
	case err := <-errs:
		t.Fatalf("Invoke returned error: %v", err)
	case result := <-done:
		if result.Tasks[0].Output != "first" || result.Tasks[1].Output != "second" {
			t.Fatalf("parallel result order was not stable: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for parallel result")
	}
}

func TestOrchestratorParallelFailFastCancelsOtherTasks(t *testing.T) {
	registry := MustRegistry(SubAgentSpec{Name: "worker"})
	enteredSlow := make(chan struct{})
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: registry,
		Runner: RunnerFunc(func(ctx context.Context, spec SubAgentSpec, task TaskSpec, req Request) (TaskResult, error) {
			if task.Input == "fail" {
				select {
				case <-enteredSlow:
				case <-ctx.Done():
					return TaskResult{Error: ctx.Err().Error()}, ctx.Err()
				}
				return TaskResult{Error: "boom"}, fmt.Errorf("boom")
			}
			close(enteredSlow)
			<-ctx.Done()
			return TaskResult{Error: ctx.Err().Error()}, ctx.Err()
		}),
		Options: Options{Parallel: true, FailFast: true},
	})

	result, err := orchestrator.Invoke(context.Background(), Request{Tasks: []TaskSpec{
		{Agent: "worker", Input: "slow"},
		{Agent: "worker", Input: "fail"},
	}})
	if err == nil {
		t.Fatalf("expected fail-fast error, got result=%#v", result)
	}
	select {
	case <-enteredSlow:
	default:
		t.Fatalf("slow task did not start before fail-fast completion")
	}
	if len(result.Tasks) != 2 {
		t.Fatalf("expected both task slots in result, got %#v", result)
	}
	if result.Tasks[0].Status != StatusFailed || !errors.Is(err, context.Canceled) && err.Error() != "boom" {
		t.Fatalf("unexpected fail-fast result=%#v err=%v", result, err)
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

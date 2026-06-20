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

func TestOrchestratorSequentialContinueOnErrorReturnsAggregate(t *testing.T) {
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(_ context.Context, _ SubAgentSpec, task TaskSpec, _ Request) (TaskResult, error) {
			if task.Input == "fail" {
				return TaskResult{Error: "boom"}, errors.New("boom")
			}
			return TaskResult{Output: task.Input}, nil
		}),
	})

	result, err := orchestrator.Invoke(context.Background(), Request{Tasks: []TaskSpec{
		{Agent: "worker", Input: "fail"},
		{Agent: "worker", Input: "continue"},
	}})
	if err != nil {
		t.Fatalf("continue-on-error returned ordinary task error: %v", err)
	}
	if len(result.Tasks) != 2 || result.Tasks[0].Status != StatusFailed || result.Tasks[1].Output != "continue" {
		t.Fatalf("continue-on-error aggregate = %#v", result)
	}
}

func TestOrchestratorParallelContinueOnErrorReturnsAggregate(t *testing.T) {
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(_ context.Context, _ SubAgentSpec, task TaskSpec, _ Request) (TaskResult, error) {
			if task.Input == "fail" {
				return TaskResult{Error: "boom"}, errors.New("boom")
			}
			return TaskResult{Output: task.Input}, nil
		}),
		Options: Options{Parallel: true},
	})

	result, err := orchestrator.Invoke(context.Background(), Request{Tasks: []TaskSpec{
		{Agent: "worker", Input: "fail"},
		{Agent: "worker", Input: "continue"},
	}})
	if err != nil {
		t.Fatalf("parallel continue-on-error returned ordinary task error: %v", err)
	}
	if len(result.Tasks) != 2 || result.Tasks[0].Status != StatusFailed || result.Tasks[1].Output != "continue" {
		t.Fatalf("parallel continue-on-error aggregate = %#v", result)
	}
}

func TestOrchestratorAlwaysPropagatesObserverError(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			observerErr := errors.New("observer unavailable")
			orchestrator := NewOrchestrator(OrchestratorConfig{
				Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
				Runner: RunnerFunc(func(ctx context.Context, _ SubAgentSpec, task TaskSpec, req Request) (TaskResult, error) {
					if err := req.NotifySubtaskEvent(ctx, task, "child_run", Event{Type: "child.progress"}); err != nil {
						return TaskResult{Error: err.Error()}, err
					}
					return TaskResult{Output: task.Input}, nil
				}),
				Options: Options{Parallel: parallel},
			})
			_, err := orchestrator.Invoke(context.Background(), Request{
				Observer: failingEventObserver{err: observerErr},
				Tasks: []TaskSpec{
					{Agent: "worker", Input: "one"},
					{Agent: "worker", Input: "two"},
				},
			})
			if !errors.Is(err, observerErr) {
				t.Fatalf("observer error = %v, want %v", err, observerErr)
			}
		})
	}
}

type failingEventObserver struct {
	err error
}

func (f failingEventObserver) SubtaskStarted(context.Context, TaskSpec) error { return nil }
func (f failingEventObserver) SubtaskEvent(context.Context, TaskSpec, string, Event) error {
	return f.err
}
func (f failingEventObserver) SubtaskFinished(context.Context, TaskResult) error { return nil }

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

func TestOrchestratorRequestMaxTasksCanOnlyTightenHostLimit(t *testing.T) {
	calls := 0
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(context.Context, SubAgentSpec, TaskSpec, Request) (TaskResult, error) {
			calls++
			return TaskResult{Output: "ok"}, nil
		}),
		Options: Options{MaxTasks: 2},
	})
	tasks := []TaskSpec{
		{Agent: "worker", Input: "one"},
		{Agent: "worker", Input: "two"},
		{Agent: "worker", Input: "three"},
	}

	if _, err := orchestrator.Invoke(context.Background(), Request{
		Tasks:   tasks,
		Options: Options{MaxTasks: 100},
	}); err == nil || err.Error() != "too many subtasks: 3 > 2" {
		t.Fatalf("host max was widened: %v", err)
	}
	if calls != 0 {
		t.Fatalf("runner calls = %d, want 0", calls)
	}

	if _, err := orchestrator.Invoke(context.Background(), Request{
		Tasks:   tasks[:2],
		Options: Options{MaxTasks: 1},
	}); err == nil || err.Error() != "too many subtasks: 2 > 1" {
		t.Fatalf("request max did not tighten host max: %v", err)
	}
	if calls != 0 {
		t.Fatalf("runner calls = %d, want 0", calls)
	}
}

func TestOrchestratorUsesDefaultHostTaskLimit(t *testing.T) {
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(context.Context, SubAgentSpec, TaskSpec, Request) (TaskResult, error) {
			return TaskResult{Output: "must not run"}, nil
		}),
	})
	tasks := make([]TaskSpec, 9)
	for i := range tasks {
		tasks[i] = TaskSpec{Agent: "worker", Input: fmt.Sprintf("task %d", i+1)}
	}

	if _, err := orchestrator.Invoke(context.Background(), Request{
		Tasks:   tasks,
		Options: Options{MaxTasks: 100},
	}); err == nil || err.Error() != "too many subtasks: 9 > 8" {
		t.Fatalf("default host max was widened: %v", err)
	}
}

func TestRequestEnforcesNestedDepthLimit(t *testing.T) {
	task := TaskSpec{Agent: "worker", Input: "nested"}
	if err := (Request{
		Depth:   1,
		Tasks:   []TaskSpec{task},
		Options: Options{AllowNested: true, MaxDepth: 2},
	}).Validate(); err != nil {
		t.Fatalf("depth 1 should be allowed with max depth 2: %v", err)
	}
	if err := (Request{
		Depth:   2,
		Tasks:   []TaskSpec{task},
		Options: Options{AllowNested: true, MaxDepth: 2},
	}).Validate(); err == nil || err.Error() != "subagent_max_depth_exceeded: 2 >= 2" {
		t.Fatalf("unexpected max depth result: %v", err)
	}
	if err := (Request{
		Depth:   1,
		Tasks:   []TaskSpec{task},
		Options: Options{MaxDepth: 2},
	}).Validate(); err == nil || err.Error() != "nested_subagent_not_allowed" {
		t.Fatalf("unexpected default nested result: %v", err)
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(context.Context, SubAgentSpec, TaskSpec, Request) (TaskResult, error) {
			return TaskResult{Output: "must not run"}, nil
		}),
		Options: Options{AllowNested: true, MaxDepth: 2},
	})
	if _, err := orchestrator.Invoke(context.Background(), Request{
		Depth:   2,
		Tasks:   []TaskSpec{task},
		Options: Options{AllowNested: true, MaxDepth: 100},
	}); err == nil || err.Error() != "subagent_max_depth_exceeded: 2 >= 2" {
		t.Fatalf("request widened host max depth: %v", err)
	}
}

func TestOrchestratorKeepsRuntimeOptionsHostOnly(t *testing.T) {
	task := TaskSpec{Agent: "worker", Input: "nested"}
	var got Options
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(_ context.Context, _ SubAgentSpec, _ TaskSpec, req Request) (TaskResult, error) {
			got = req.Options
			return TaskResult{Output: "ok"}, nil
		}),
		Options: Options{AllowNested: true, MaxDepth: 2},
	})
	if _, err := orchestrator.Invoke(context.Background(), Request{
		Depth:   1,
		Tasks:   []TaskSpec{task},
		Options: Options{MaxDepth: 1, InheritContext: true},
	}); err != nil {
		t.Fatalf("request changed host nesting options: %v", err)
	}
	if !got.AllowNested || got.MaxDepth != 2 || got.InheritContext {
		t.Fatalf("runner options = %#v, want host nesting/context options", got)
	}

	blocked := NewOrchestrator(OrchestratorConfig{
		Registry: MustRegistry(SubAgentSpec{Name: "worker"}),
		Runner: RunnerFunc(func(context.Context, SubAgentSpec, TaskSpec, Request) (TaskResult, error) {
			return TaskResult{Output: "must not run"}, nil
		}),
	})
	if _, err := blocked.Invoke(context.Background(), Request{
		Depth:   1,
		Tasks:   []TaskSpec{task},
		Options: Options{AllowNested: true, MaxDepth: 100, InheritContext: true},
	}); err == nil || err.Error() != "nested_subagent_not_allowed" {
		t.Fatalf("request enabled host-only options: %v", err)
	}
}

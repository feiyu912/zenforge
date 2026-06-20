package subagent

import (
	"context"
	"fmt"
	"sync"
)

type Orchestrator interface {
	Invoke(ctx context.Context, req Request) (Result, error)
}

type Runner interface {
	RunSubAgent(ctx context.Context, spec SubAgentSpec, task TaskSpec, req Request) (TaskResult, error)
}

type RunnerFunc func(context.Context, SubAgentSpec, TaskSpec, Request) (TaskResult, error)

func (f RunnerFunc) RunSubAgent(ctx context.Context, spec SubAgentSpec, task TaskSpec, req Request) (TaskResult, error) {
	return f(ctx, spec, task, req)
}

type OrchestratorConfig struct {
	Registry Registry
	Runner   Runner
	Options  Options
}

type DefaultOrchestrator struct {
	registry Registry
	runner   Runner
	options  Options
}

func NewOrchestrator(config OrchestratorConfig) *DefaultOrchestrator {
	return &DefaultOrchestrator{registry: config.Registry, runner: config.Runner, options: config.Options}
}

func (o *DefaultOrchestrator) Invoke(ctx context.Context, req Request) (Result, error) {
	req.Options = mergeOptions(o.options, req.Options)
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	if o.registry == nil {
		return Result{}, fmt.Errorf("subagent registry is not configured")
	}
	if o.runner == nil {
		return Result{}, fmt.Errorf("subagent runner is not configured")
	}
	if req.Options.Parallel && len(req.Tasks) > 1 {
		return o.invokeParallel(ctx, req)
	}
	return o.invokeSequential(ctx, req)
}

func (o *DefaultOrchestrator) invokeSequential(ctx context.Context, req Request) (Result, error) {
	results := make([]TaskResult, 0, len(req.Tasks))
	for i, task := range req.Tasks {
		result, err := o.runTask(ctx, req, task, i)
		results = append(results, result)
		if shouldStop(req.Options, result, err) {
			if err == nil {
				err = fmt.Errorf("%s", result.Error)
			}
			return Result{Tasks: results}, err
		}
	}
	return Result{Tasks: results}, nil
}

func (o *DefaultOrchestrator) invokeParallel(ctx context.Context, req Request) (Result, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Options.FailFast {
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	results := make([]TaskResult, len(req.Tasks))
	errs := make([]error, len(req.Tasks))
	var wg sync.WaitGroup
	for i, task := range req.Tasks {
		wg.Add(1)
		go func(i int, task TaskSpec) {
			defer wg.Done()
			result, err := o.runTask(runCtx, req, task, i)
			results[i] = result
			errs[i] = err
			if shouldStop(req.Options, result, err) && cancel != nil {
				cancel()
			}
		}(i, task)
	}
	wg.Wait()

	out := make([]TaskResult, 0, len(results))
	var firstErr error
	for i, result := range results {
		out = append(out, result)
		if firstErr == nil && shouldStop(req.Options, result, errs[i]) {
			firstErr = errs[i]
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", result.Error)
			}
		}
	}
	return Result{Tasks: out}, firstErr
}

func (o *DefaultOrchestrator) runTask(ctx context.Context, req Request, task TaskSpec, index int) (TaskResult, error) {
	task = normalizeTask(task, index)
	if err := ctx.Err(); err != nil {
		return failedResult(task, err.Error()), err
	}
	spec, ok := o.registry.Lookup(task.NormalizedAgentName())
	if !ok {
		return failedResult(task, fmt.Sprintf("unknown subagent: %s", task.NormalizedAgentName())), nil
	}
	result, err := o.runner.RunSubAgent(ctx, spec, task, req)
	result = normalizeResult(result, spec, task)
	if err != nil && result.Error == "" {
		result.Status = StatusFailed
		result.Error = err.Error()
	}
	return result, err
}

func shouldStop(options Options, result TaskResult, err error) bool {
	return options.FailFast && (err != nil || result.Status == StatusFailed)
}

func mergeOptions(base, override Options) Options {
	out := base
	if out.MaxTasks <= 0 {
		out.MaxTasks = 8
	}
	if override.MaxTasks > 0 && override.MaxTasks < out.MaxTasks {
		out.MaxTasks = override.MaxTasks
	}
	if out.MaxDepth <= 0 {
		out.MaxDepth = 1
	}
	if override.Parallel {
		out.Parallel = true
	}
	if override.FailFast {
		out.FailFast = true
	}
	return out
}

func normalizeTask(task TaskSpec, index int) TaskSpec {
	if task.ID == "" {
		task.ID = fmt.Sprintf("subtask_%d", index+1)
	}
	if task.AgentName == "" {
		task.AgentName = task.Agent
	}
	return task
}

func normalizeResult(result TaskResult, spec SubAgentSpec, task TaskSpec) TaskResult {
	if result.ID == "" {
		result.ID = task.ID
	}
	if result.AgentName == "" {
		result.AgentName = spec.Name
	}
	if result.Name == "" {
		result.Name = task.Name
	}
	if result.Status == "" {
		if result.Error != "" {
			result.Status = StatusFailed
		} else {
			result.Status = StatusCompleted
		}
	}
	return result
}

func failedResult(task TaskSpec, message string) TaskResult {
	return TaskResult{
		ID:        task.ID,
		AgentName: task.NormalizedAgentName(),
		Name:      task.Name,
		Status:    StatusFailed,
		Error:     message,
	}
}

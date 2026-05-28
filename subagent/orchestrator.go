package subagent

import (
	"context"
	"fmt"
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
	results := make([]TaskResult, 0, len(req.Tasks))
	for i, task := range req.Tasks {
		if err := ctx.Err(); err != nil {
			return Result{Tasks: results}, err
		}
		task = normalizeTask(task, i)
		spec, ok := o.registry.Lookup(task.NormalizedAgentName())
		if !ok {
			result := failedResult(task, fmt.Sprintf("unknown subagent: %s", task.NormalizedAgentName()))
			results = append(results, result)
			if req.Options.FailFast {
				return Result{Tasks: results}, fmt.Errorf("%s", result.Error)
			}
			continue
		}
		result, err := o.runner.RunSubAgent(ctx, spec, task, req)
		result = normalizeResult(result, spec, task)
		if err != nil && result.Error == "" {
			result.Status = StatusFailed
			result.Error = err.Error()
		}
		results = append(results, result)
		if err != nil && req.Options.FailFast {
			return Result{Tasks: results}, err
		}
	}
	return Result{Tasks: results}, nil
}

func mergeOptions(base, override Options) Options {
	out := base
	if override.MaxTasks != 0 {
		out.MaxTasks = override.MaxTasks
	}
	if override.AllowNested {
		out.AllowNested = true
	}
	if override.Parallel {
		out.Parallel = true
	}
	if override.FailFast {
		out.FailFast = true
	}
	if override.InheritContext {
		out.InheritContext = true
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

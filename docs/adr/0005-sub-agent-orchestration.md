# ADR 0005: Sub-Agent Orchestration

Status: accepted

## Context

In `agent-platform`, `agent_invoke` is not executed by the normal backend tool
executor. The model tool call is converted into `DeltaInvokeSubAgents`, then
server `frameOrchestrator` intercepts it and runs child tasks concurrently.

This works, but the current implementation mixes:

- sub-agent validation;
- chat references;
- resource tickets;
- system-init persistence;
- proxy behavior;
- server event routing;
- child session construction.

ZenForge should support sub-agents without pulling in server concerns.

## Decision

ZenForge will model sub-agent delegation as a core orchestrator behind a tool.

The public tool may be called:

- `task`;
- or `agent_invoke` for compatibility.

Internally it calls `SubAgentOrchestrator`.

## Interfaces

```go
type SubAgentSpec struct {
    Name         string
    Description  string
    Instructions string
    Tools        []tool.Tool
    Model        model.Model
}

type Orchestrator interface {
    Invoke(ctx context.Context, req Request) (Result, error)
}

type Request struct {
    RunID      string
    ParentStep string
    Tasks      []TaskSpec
}

type TaskSpec struct {
    AgentName string
    TaskName  string
    Input     string
    Files     []string
}
```

## Behavior

- Validate that every requested sub-agent exists.
- Limit task count.
- Optionally disallow nested sub-agent calls.
- Start child tasks concurrently by default.
- Route child events into parent stream as `subtask.event`.
- Aggregate child results into parent tool result.
- Mark failed child tasks explicitly.
- Checkpoint parent state before and after child execution.

## MVP Constraints

- Max child tasks default: 5.
- Nested sub-agent calls disabled by default.
- Child tasks share parent checkpoint store and event log.
- Child workspace access is inherited or explicitly scoped.

## Migration From agent-platform

Reuse ideas from:

- `DeltaInvokeSubAgents`;
- `SubAgentTaskSpec`;
- `frameOrchestrator.handleSubAgentBatch`;
- nested invocation guard;
- child result aggregation.

Do not directly copy:

- resource ticket handling;
- chat store references;
- proxy client logic;
- server route coupling.

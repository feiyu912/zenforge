# S7 Sub-Agent Runtime Spec

S7 adds delegated child tasks.

The goal is to let a main agent split work across configured sub-agents, stream
their progress, aggregate their results, and resume safely around subtask
boundaries.

## S7 Outcome

After S7, ZenForge should support:

- `SubAgentSpec`;
- `task` tool;
- optional `agent_invoke` compatibility alias;
- sub-agent registry;
- child runner invocation;
- concurrent child tasks;
- child event routing;
- result aggregation;
- nested invocation guard;
- checkpointed subtask state.

## Design Principles

1. Sub-agent orchestration is core, server transport is adapter.
2. `task` behaves like a tool to the model but uses a runtime orchestrator.
3. Child events must be visible in parent streams.
4. Child state must be checkpointed at parent boundaries.
5. Nested sub-agents are disabled by default.
6. Sub-agents receive scoped tools and instructions.

## Package Plan

```text
subagent/
  spec.go
  registry.go
  orchestrator.go
  result.go

tools/task/
  task.go
```

## SubAgentSpec

```go
type SubAgentSpec struct {
    Name         string
    Description  string
    Instructions string
    Model        model.Model
    Tools        []tool.Tool
    MaxSteps     int
    Metadata     map[string]any
}
```

Rules:

- `Name` is required and unique.
- `Description` is used in tool schema/prompt.
- `Instructions` override or extend parent instructions.
- `Tools` are scoped to the child.
- `Model` defaults to parent model if nil.

## Task Tool

Tool name:

```text
task
```

Compatibility alias:

```text
agent_invoke
```

Input:

```json
{
  "tasks": [
    {
      "agent": "researcher",
      "name": "Inspect docs",
      "input": "Read docs and summarize the architecture",
      "files": ["README.md"]
    }
  ]
}
```

Rules:

- task count must be between 1 and configured max;
- every task needs `agent` and `input`;
- unknown agent fails the tool call;
- nested task calls are blocked by default;
- parent tool result is aggregated JSON.

## Registry

```go
type Registry interface {
    Register(spec SubAgentSpec) error
    Lookup(name string) (SubAgentSpec, bool)
    List() []SubAgentSpec
}
```

## Orchestrator

```go
type Orchestrator interface {
    Invoke(ctx context.Context, req Request) (Result, error)
}

type Request struct {
    RunID        string
    ParentStep   int
    ParentTaskID string
    ToolCallID   string
    Depth        int
    Tasks        []TaskSpec
    Options      Options
    Context      map[string]any // host-populated, never model-decoded
}

type TaskSpec struct {
    ID        string
    AgentName string
    Name      string
    Input     string
    Files     []string
    Metadata  map[string]any
}

type Options struct {
    MaxTasks       int
    MaxDepth       int
    AllowNested    bool
    Parallel       bool
    FailFast       bool
    InheritContext bool
}
```

The model-facing task tool accepts bounded request options such as `parallel`,
`failFast`, and `maxTasks`. `AllowNested` remains a host/runtime option and is
not exposed through the default task tool schema.

`Config.SubAgentOptions.MaxTasks` is the authoritative host ceiling. The task
tool schema, pre-checkpoint runtime validation, and default orchestrator all use
that value. A request-level `maxTasks` may lower the ceiling for one call but
cannot increase it.

## Result

```go
type Result struct {
    Tasks []TaskResult `json:"tasks"`
}

type TaskResult struct {
    ID        string         `json:"id"`
    AgentName string         `json:"agentName"`
    Name      string         `json:"name"`
    Status    string         `json:"status"`
    Output    string         `json:"output,omitempty"`
    Error     string         `json:"error,omitempty"`
    RunID     string         `json:"runId,omitempty"`
    Metadata  map[string]any `json:"metadata,omitempty"`
}
```

## Runtime Flow

```text
parent model emits task tool call
  ↓
harness detects runtime task tool
  ↓
checkpoint parent subtask state
  ↓
orchestrator validates tasks
  ↓
emit subtask.started for each child
  ↓
run child harness per task
  ↓
route child events as subtask.event
  ↓
emit subtask.done/error
  ↓
aggregate child results
  ↓
inject aggregate as parent tool result
  ↓
checkpoint parent state
```

## Child Run Config

Child run config is derived from:

- selected `SubAgentSpec`;
- parent run ID;
- child task input;
- child scoped tools;
- child instructions;
- inherited or scoped workspace;
- inherited checkpoint/event stores.

Parent run metadata is isolated unless the host enables `InheritContext`.
Cancellation and deadlines always propagate through the Go context and are not
controlled by that option. When inheritance is enabled, child metadata
precedence is task metadata, inherited parent context, host-configured
`SubAgentSpec.Metadata`, then runtime-owned identifiers and depth. `TaskSpec`
files are copied into `subagent.files`.

Child run ID:

```text
{parentRunID}_sub_{n}
```

This can be overridden by ID generator later.

## Event Routing

Core child events:

- emitted to child run event log;
- optionally wrapped into parent stream as `subtask.event`;
- parent stream emits `subtask.started` and `subtask.done/error`.

`subtask.event` payload should include:

- subtask ID;
- child run ID;
- child event type;
- child event data.

## Checkpoint Behavior

Parent checkpoint:

- before child tasks start;
- when each child starts;
- when each child completes/fails;
- after aggregated result is injected.

Child checkpoint:

- normal S4/S5 behavior in child run.

Resume behavior:

- if parent has pending subtasks and no child run IDs, start them;
- if child run IDs exist, inspect child terminal states;
- completed child results are not rerun;
- non-terminal child runs may resume through child runner;
- after all child terminal, aggregate results.

When the same parent task tool call is started again from a checkpoint,
subtask state is keyed by parent task id plus subtask id. Re-entering the start
phase updates the existing non-terminal subtask record instead of appending a
duplicate, while terminal child records remain stable.
Terminal child records are reused in the parent aggregate result so completed
children are not invoked again during resume.

MVP simplification:

- if a child was running during crash, resume child from its latest checkpoint;
- if child resume is unsupported, mark child failed with clear error.

The default child runner checks for the deterministic child run checkpoint
before starting a fresh child stream. Existing child checkpoints resume through
`Agent.Resume`; missing child checkpoints start a new child stream. Other
checkpoint lookup errors fail before model execution. A child
`run.cancelled` outcome is propagated as a failed task result rather than
normalized into successful completion.

## Nested Invocation

Default:

```text
AllowNested = false
```

If a child tries to call `task`, the tool returns:

```text
nested_subagent_not_allowed
```

Controlled nesting is host-only and requires both `AllowNested = true` and a
finite `MaxDepth`. The default maximum depth is one, so children do not receive
the task tool. Below an explicitly larger limit, children inherit the host
registry/runner and may delegate again. Requests cannot override either flag or
raise the depth ceiling.

## Failure Behavior

Default:

- if any child fails, parent task tool returns error status but still includes
  all child results;
- parent model decides whether to continue;
- `FailFast` cancels remaining children on first failure.

## Migration From agent-platform

Source inspiration:

- `contracts.DeltaInvokeSubAgents`;
- `contracts.SubAgentTaskSpec`;
- `llm.run_stream_tools.go` `agent_invoke` recognition;
- `server.frame_orchestrator.handleSubAgentBatch`;
- nested invocation guard;
- child result aggregation;
- `SubTaskID` sandbox isolation idea.

Do not port directly:

- chat resource ticket logic;
- server frame routing;
- proxy clients;
- system-init persistence;
- ZenMind catalog session builder.

## S7 Tests

Minimum tests:

- register and lookup sub-agent;
- task tool validates missing agent/input;
- unknown sub-agent fails;
- one child task runs and aggregates result;
- multiple child tasks run in stable order;
- child failure included in aggregate;
- fail-fast cancels remaining children;
- nested invocation blocked;
- parent emits subtask events;
- parent checkpoint records child states;
- resume does not rerun completed child.

## S7 Exit Criteria

- main fake agent can delegate to fake child agent;
- child events route to parent stream;
- result aggregation is model-visible;
- subtask state is durable;
- no ZenMind server/chat imports.

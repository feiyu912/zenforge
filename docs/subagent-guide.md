# Sub-Agent Guide

This guide covers ZenForge sub-agent configuration, task dispatch, defaults,
events, and safety boundaries.

## Why Use Sub-Agents

Use sub-agents when a task can be split into independent pieces:

- research;
- code inspection;
- risk review;
- test planning;
- documentation review.

Sub-agents are useful when they have different tools or instructions from the
main agent.

## Configuration

```go
agent := zenforge.New(zenforge.Config{
    Model: model,
    Tools: baseTools,
    SubAgentSpecs: []zenforge.SubAgentSpec{
        {
            Name: "researcher",
            Description: "Reads documents and summarizes evidence.",
            Instructions: "Be precise and cite files.",
            Tools: []zenforge.Tool{workspaceRead, grep},
        },
        {
            Name: "reviewer",
            Description: "Finds bugs, risks, and missing tests.",
            Instructions: "Prioritize concrete findings.",
            Tools: []zenforge.Tool{workspaceRead, grep, shell},
        },
    },
    SubAgentOptions: zenforge.SubAgentOptions{
        MaxTasks:       3,
        MaxDepth:       1,
        Parallel:       true,
        FailFast:       false,
        InheritContext: true,
    },
})
```

Sub-agent tools do not require planner or todo configuration. Configuring
`SubAgentSpecs`, a registry, an orchestrator, or `SubAgentsEnabled` advertises
both `task` and its compatibility alias `agent_invoke`.

## Task Tool

The model calls:

```json
{
  "tasks": [
    {
      "agent": "researcher",
      "name": "Read docs",
      "input": "Read README and summarize architecture"
    }
  ],
  "options": {
    "parallel": true,
    "failFast": false,
    "maxTasks": 3
  }
}
```

The parent receives an aggregated tool result.
Task tool options expose bounded runtime controls. Nested sub-agents remain
blocked by default and are not exposed as a model-facing option.
Request `maxTasks` can only tighten the host-owned `SubAgentOptions.MaxTasks`;
it cannot raise the configured limit. ZenForge validates that limit before
creating or checkpointing child state.

## Workflow Tool

Wherever sub-agents are configured, the agent also advertises `workflow`: one
JavaScript script that fans work out across child runs. The script body runs
inside an async function, so top-level `await` is legal and `return <value>`
is the tool's result. It sees `agent(prompt, opts?)` (the child's final text,
or the object behind `opts.schema`, or `null` when the child did not complete
or did not answer the schema it was given), `parallel(thunks)`,
`pipeline(items, ...stages)` (each item walks the stages on its own, no
barrier between stages), `phase(title)`, `log(message)`, and `args`.

```json
{
  "meta": {"name": "review", "description": "Review the diff in two passes"},
  "script": "phase(\"scan\"); const first = await agent(\"list the changed files\"); return await agent(\"review: \" + first);",
  "args": {"paths": ["a.go"]}
}
```

Each `agent()` call is an ordinary sub-agent run: it carries the parent run
id, step, and tool-call id, honours the host's nesting depth, and emits the
same `subtask.*` events as the task tool. `Config.WorkflowAgent` names the
sub-agent those calls run as (empty uses the first registered one), and
`Config.WorkflowLimits` bounds concurrency, total agents, items per call,
and the cancellation/sync windows. A `provider` or `model` option is resolved through
`Config.ModelResolver` (ADR 0064): the host's own provider keeps the host's
configured credentials, any other provider is read from its own environment
variables, and a name that cannot be resolved is a fatal `AGENT_START` rather
than a silent run on the host's model. A `schema` child's answer is parsed and
then checked against that schema (ADR 0065): a non-conforming answer nulls the
item like any other child failure, and every violation is written to the
workflow's log in one line, so a bad answer is diagnosable rather than
indistinguishable from an empty one. Workflow children are the script's, not the parent's plan, so they are
not recorded as run-state subtasks; a resumed run replays the script and
reuses children through their deterministic ids (see ADR 0059).

## Defaults

- max tasks: 8;
- max depth: 1;
- run children in parallel;
- block nested sub-agent calls;
- aggregate all results;
- failed child result does not hide successful child results.

## Child Context

Child metadata is isolated by default. Set `InheritContext: true` when children
need trusted metadata from the parent run, such as platform session or tenant
scope. This option does not control Go context propagation: cancellation and
deadlines always flow from parent to child.

Child model metadata has deterministic precedence:

1. model-provided task metadata;
2. inherited parent run metadata, when enabled;
3. host-configured `SubAgentSpec.Metadata`;
4. runtime-owned `parentRunId`, `subtaskId`, and `subagent.depth`.

Task `files` are copied into `subagent.files` so the child can inspect its file
scope without allowing later caller mutation to change the active run.

The child config retains the parent's workspace, approval broker, checkpoint
store, event store, and trace sink. Its callable tools remain scoped to
`SubAgentSpec.Tools`; inheriting context never grants the parent's full tool
set.

## Events

Sub-agent work emits:

- `subtask.started`;
- `subtask.event`;
- `subtask.done`;
- `subtask.error`.

A workflow's own progress is separate (ADR 0066): `workflow.phase` for a
`phase()` call, `workflow.log` for a `log()` call, and
`workflow.agent.started`/`workflow.agent.done` for the script's bookkeeping
view of each `agent()` call, all carrying `workflow`, `parentRunId` and
`toolCallId` plus the kind's fields. A workflow child is still a sub-agent
run, so its lifecycle and streamed events remain `subtask.*` events.

## Safety

Sub-agents should receive scoped tools. Do not automatically give every child
the parent's full tool set.

The default child runner resumes a deterministic child checkpoint when one
exists and starts a fresh child only for `checkpoint.ErrNotFound`. Other
checkpoint load failures stop before the child model runs, preventing duplicate
side effects during a storage outage.

A child `run.cancelled` outcome is returned to the parent as a failed subtask
with the cancellation error. It is never normalized into a completed result
with empty output.

## Controlled Nesting

Nested delegation is disabled by default. Hosts can opt in with a finite depth:

```go
SubAgentOptions: zenforge.SubAgentOptions{
    MaxTasks:    4,
    MaxDepth:    2,
    AllowNested: true,
}
```

`MaxDepth: 2` allows a first-generation child to delegate once. A child at the
limit does not inherit sub-agent tools. Calls returned by a provider despite
that restriction fail with `subagent_max_depth_exceeded` or
`nested_subagent_not_allowed` before child state is created.

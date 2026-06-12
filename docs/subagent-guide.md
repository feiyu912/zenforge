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
        MaxTasks: 3,
        MaxDepth: 1,
        Parallel: true,
        FailFast: false,
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

## Defaults

- max tasks: 8;
- max depth: 1;
- run children in parallel;
- block nested sub-agent calls;
- aggregate all results;
- failed child result does not hide successful child results.

## Events

Sub-agent work emits:

- `subtask.started`;
- `subtask.event`;
- `subtask.done`;
- `subtask.error`.

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

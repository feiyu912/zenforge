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
    SubAgents: []zenforge.SubAgentSpec{
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
})
```

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

## Defaults

- max tasks: 5;
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

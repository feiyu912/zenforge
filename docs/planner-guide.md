# Planner Guide

This guide covers ZenForge planning modes, todo lifecycle, and the
plan/execute preset.

## When To Enable Planning

Enable planning for tasks that are:

- long-running;
- multi-step;
- tool-heavy;
- easy to lose track of;
- useful to resume later.

Examples:

- repository analysis;
- code review;
- refactor planning;
- research synthesis;
- migration checklist generation.

## Basic Config

```go
agent := zenforge.New(zenforge.Config{
    Model: model,
    Tools: tools,
    Planning: zenforge.PlanningEnabled,
})
```

With planning enabled, the agent can use todo tools:

- `todo_write`;
- `todo_read`;
- `todo_update`.

## Todo Lifecycle

Typical todo statuses:

```text
pending -> in_progress -> done
pending -> in_progress -> failed
pending -> cancelled
```

Every todo update is streamed as `todo.updated` and checkpointed.

## Plan/Execute Preset

The plan/execute preset has three stages:

1. Plan
2. Execute todos
3. Summary

The plan stage creates todos. The execute stage works through them. The summary
stage reports the final outcome.

## CLI Display

CLI should render todos compactly:

```text
[done]        Inspect project structure
[in_progress] Review tool runtime
[pending]     Draft migration plan
```

## Failure Behavior

Default behavior:

- if planning creates no todos, the run fails;
- if a todo fails, the plan stops;
- if a todo does not reach terminal status, it is marked failed;
- summary runs only after successful terminal todo flow unless configured
  otherwise.

## Customization

Later versions should allow:

- continue-on-failed-task;
- parallel todo execution;
- custom status names;
- custom stage prompts;
- planner-only mode.

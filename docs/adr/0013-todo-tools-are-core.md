# ADR 0013: Todo Tools Are Core

Status: proposed

## Context

Long-running agents need an externalized task list. Without this, planning
becomes hidden prompt text and is hard to observe, resume, or steer.

The current platform already has plan tools. ZenForge should keep that idea but
make the public concept a todo runtime.

## Decision

ZenForge will include todo tools as core built-ins:

- `todo_write`;
- `todo_read`;
- `todo_update`.

They mutate checkpointed run state and emit `todo.updated`.

## Consequences

Benefits:

- long-task progress is visible;
- resume can continue from active task;
- CLI/UI can render todos;
- plan/execute preset has a durable state model.

Costs:

- core includes a small opinionated planner primitive;
- users who want no planning must disable it.


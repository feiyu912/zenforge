# ADR 0002: Public Event Contract

Status: proposed

## Context

ZenForge needs a stable event stream for CLI, SDK consumers, server adapters,
trace systems, and UI adapters. The existing platform has rich events, but some
names and payloads are tied to chat UI and Java compatibility.

## Decision

ZenForge will expose a small stable event contract. Adapters may map this to
platform-specific events.

## Event Shape

```go
type Event struct {
    Version   string         `json:"version"`
    Seq       int64          `json:"seq"`
    Type      EventType      `json:"type"`
    RunID     string         `json:"runId"`
    ParentID  string         `json:"parentId,omitempty"`
    TaskID    string         `json:"taskId,omitempty"`
    Timestamp time.Time      `json:"timestamp"`
    Data      map[string]any `json:"data,omitempty"`
}
```

## Initial Event Types

Run lifecycle:

- `run.started`
- `run.resumed`
- `run.done`
- `run.error`
- `run.cancelled`

Step lifecycle:

- `step.started`
- `step.done`
- `step.error`

Model:

- `model.started`
- `model.delta`
- `model.done`
- `model.error`

Tool:

- `tool.call`
- `tool.result`
- `tool.error`

Todo/planner:

- `todo.updated`
- `task.started`
- `task.done`
- `task.error`

Workspace:

- `workspace.changed`

Approval:

- `approval.requested`
- `approval.resolved`
- `approval.expired`

Sub-agent:

- `subtask.started`
- `subtask.event`
- `subtask.done`
- `subtask.error`

Checkpoint:

- `checkpoint.created`

## Payload Rules

- Every event must carry `runId`.
- Every persisted event must carry monotonically increasing `seq` per run.
- `model.delta` may be high volume and should be optional in durable logs for
  some storage backends, but CLI streaming should receive it.
- Tool arguments may be redacted by policy before persistence.
- Secrets must never be stored by default.
- Adapters can add vendor/platform fields under `data.adapter`.

## Compatibility Mapping

ZenMind adapter can map:

- `run.started` -> `run.start`
- `run.done` -> `run.complete`
- `model.delta` -> `content.delta` or `reasoning.delta`
- `tool.call` -> `tool.start`/`tool.args`
- `tool.result` -> `tool.result`
- `todo.updated` -> `plan.update`
- `approval.requested` -> `awaiting.ask`
- `approval.resolved` -> `awaiting.answer`
- `subtask.started` -> `task.start`
- `subtask.done` -> `task.complete`

The core should not emit ZenMind UI names directly.


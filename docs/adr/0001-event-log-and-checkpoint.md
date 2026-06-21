# ADR 0001: Event Log And Checkpoint Are Separate

Status: accepted

## Context

`agent-platform` currently has strong event streaming and chat replay behavior,
but it does not have true durable runtime resume. The existing chat JSONL files
are optimized for UI replay, summaries, and trace inspection. They are not a
complete execution checkpoint.

ZenForge needs recoverable long-running agents. A process can crash after a
model turn, before a tool result, while waiting for approval, or during sub-agent
execution. The runtime must know where to continue.

## Decision

ZenForge will keep two separate persistence concepts:

1. `RunEventLog`
2. `CheckpointStore`

The event log is an append-only record of observable runtime events.

The checkpoint store is the source of truth for resumable execution state.

The event log can be rebuilt from checkpoints only partially. Checkpoints cannot
be rebuilt reliably from UI events. They must be written explicitly.

## Interfaces

```go
type RunEventLog interface {
    Append(ctx context.Context, event Event) error
    Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error)
    LatestSeq(ctx context.Context, runID string) (int64, error)
}

type CheckpointStore interface {
    Save(ctx context.Context, checkpoint Checkpoint) error
    Load(ctx context.Context, runID string) (*Checkpoint, error)
    Delete(ctx context.Context, runID string) error
}
```

## Checkpoint Contents

A checkpoint should include:

- schema version;
- run ID;
- task input;
- current phase;
- step index;
- messages;
- pending tool calls;
- active tool call;
- last tool result;
- todo/plan state;
- approval state;
- run control state;
- subtask state;
- usage counters;
- model/provider metadata;
- workspace snapshot references where needed;
- timestamps.

## Event Log Contents

The event log should include:

- `run.started`;
- `run.resumed`;
- `step.started`;
- `model.started`;
- `model.delta`;
- `tool.call`;
- `tool.result`;
- `todo.updated`;
- `approval.requested`;
- `approval.resolved`;
- `subtask.started`;
- `subtask.done`;
- `checkpoint.created`;
- `run.done`;
- `run.error`.

## Write Order

For resumability, checkpoint writes must happen at deterministic boundaries:

1. before model call starts;
2. after model response/tool call parse completes;
3. before tool execution starts;
4. after tool result is persisted into messages;
5. before waiting for approval;
6. after approval decision;
7. before and after subtask execution;
8. before terminal run event.

Suggested order at each boundary:

```text
mutate runtime state
save checkpoint
append checkpoint.created event
fan out live event
```

For terminal events:

```text
save terminal checkpoint
append run.done/run.error
freeze live event bus
```

If event sequence lookup or append fails, the harness stops advancing and
reports an in-memory `run.error` to the current caller. It must not publish the
unpersisted event as observable progress. Trace sinks are a separate
best-effort projection and do not participate in this durability contract.

## Consequences

Benefits:

- real resume can be implemented;
- event streaming is not overloaded as state storage;
- ZenMind chat JSONL can remain a read model;
- other stores like SQLite/Postgres can be added later.

Costs:

- more state modeling up front;
- tool and approval flows must expose serializable state;
- initial MVP may support resume only for well-defined safe boundaries.

## Migration From agent-platform

Current reusable pieces:

- `internal/stream` event concepts;
- `internal/stream/event_bus.go` for live fanout shape;
- `internal/chat/step_writer.go` as trace projection inspiration;
- `internal/contracts/run_control.go` as run-control state inspiration.

Do not directly treat:

- `chat.FileStore`;
- `StepWriter`;
- `LoadChat`;
- `events.jsonl`;

as the checkpoint layer.

# Checkpoint Resume Guide

ZenForge checkpoints are durable runtime state. They are separate from the
event log:

- checkpoint store is the resume source of truth;
- event log is the replay/read model for users, CLIs, and server adapters.

Configure both for a local durable run:

```go
events := eventlogjsonl.New(".zenforge/runs")
checkpoints := checkpointjsonl.New(".zenforge/runs")

agent := zenforge.New(zenforge.Config{
    Model:       model,
    Events:      events,
    Checkpoints: checkpoints,
})
```

For a single SQLite file:

```go
events, err := eventlogsqlite.Open(ctx, ".zenforge/runs.db")
checkpoints, err := checkpointsqlite.Open(ctx, ".zenforge/runs.db")
defer events.Close()
defer checkpoints.Close()
```

Resume from the latest checkpoint:

```go
events, err := agent.Resume(ctx, "run_123")
```

## Supported Boundaries

ZenForge resumes only from explicit runtime boundaries:

- before a model call;
- after a model turn with pending tools;
- after a tool result has been injected into messages;
- active tool boundary, by retrying the tool call from checkpointed arguments;
- waiting approval, when an approval broker is configured;
- before the final no-tool summary turn;
- completed, failed, or cancelled terminal states.

Terminal states do not rerun model or tool work. They emit `run.resumed` and
then the matching terminal event.

## Limited Boundaries

ZenForge does not resume a provider stream mid-token. It retries from the last
checkpointed boundary.

Shell commands and external side effects should be configured with idempotency
in mind. If a process crashes while a tool call was active, ZenForge retries the
checkpointed tool call; custom tools should use fingerprints, approvals, or
external guards when retrying could be unsafe.

Sub-agent resume is supported at parent and child checkpoint boundaries. If a
child run was in an uncheckpointed provider stream or external process, it
follows the same retry-from-boundary rules as any other run.

## Approval Resume

If a run was waiting for approval, resume requires an approval broker:

```go
agent := zenforge.New(zenforge.Config{
    Approval:    approval.NewChannelBroker(requests, decisions),
    Checkpoints: checkpoints,
})
```

On resume, ZenForge emits `approval.requested` again with `resumed: true`, waits
for a decision, checkpoints the decision, and continues the tool call when the
decision approves it.

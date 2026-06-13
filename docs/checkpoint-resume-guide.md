# Checkpoint Resume Guide

ZenForge checkpoints are durable runtime state. They are separate from the
event log:

- checkpoint store is the resume source of truth;
- event log is the replay/read model for users, CLIs, and server adapters.

Checkpoint records use schema version `zenforge.checkpoint.v1`. The embedded
run state uses version `zenforge.run_state.v1`. Event records use the public
flattened event contract documented in [ADR 0002](./adr/0002-public-event-contract.md).

Every model, tool, approval, sub-agent, and terminal boundary checks the
checkpoint write result before advancing. A failed write stops the run and
leaves the last successful checkpoint intact, so resume never relies on state
that was only held in memory.

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
then the matching terminal event. Failed and cancelled checkpoints retain their
terminal reason so resume can report the original error instead of a generic
fallback.

Plan/execute uses one checkpoint sequence across plan, execute, and summary
stages. The final summary is stored as a completed terminal checkpoint with the
summary output and terminal todos. Resuming that checkpoint returns the stored
summary without another model call.

Failure and cancellation checkpoints use the same fail-closed rule. When a
plan/execute terminal write fails, the previous checkpoint remains the resume
source; ZenForge reports the storage error rather than an unpersisted terminal
outcome.

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
Only a missing child checkpoint starts a fresh child stream. A checkpoint
backend read failure is surfaced before model execution so the parent cannot
silently duplicate child work.

## Approval Resume

If a run was waiting for approval, resume can continue with an approval broker:

```go
agent := zenforge.New(zenforge.Config{
    Approval:    approval.NewChannelBroker(requests, decisions),
    Checkpoints: checkpoints,
})
```

On resume, ZenForge emits `approval.requested` again with `resumed: true`, waits
for a decision, checkpoints the decision, and continues the tool call when the
decision approves it. Without a broker, resume re-emits the request and leaves
the waiting checkpoint unchanged.

Approved run/rule grants are also part of checkpoint state. After resume, an
exact fingerprint or rule-key match can reuse the grant without another broker
wait; the runtime still records a resolved audit decision for that reuse.
Grants never cross run IDs.

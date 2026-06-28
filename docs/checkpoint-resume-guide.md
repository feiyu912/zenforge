# Checkpoint Resume Guide

ZenForge checkpoints are durable runtime state. They are separate from the
event log:

- checkpoint store is the resume source of truth;
- event log is the replay/read model for users, CLIs, and server adapters.

Checkpoint records use schema version `zenforge.checkpoint.v1`. The embedded
run state uses version `zenforge.run_state.v1`. Event records use the public
flattened event contract documented in [ADR 0002](./adr/0002-public-event-contract.md).
Checkpoint loads fail closed when the embedded run state has an unknown
version, phase, mode, or invalid model-attempt chain before resume can dispatch
model or tool work. Legacy
checkpoints with an empty run-state version or mode remain readable; phase is
always required to be one of the supported runtime phases.

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
- during a model stream, by superseding the checkpointed draft and replacing
  the attempt at the same logical step;
- after a model turn with pending tools;
- after a committed text-only model turn whose terminal event was not written;
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
summary without another model call. Plan, execute, max-step finalization, and
summary model calls all use the same durable attempt lifecycle.

Failure and cancellation checkpoints use the same fail-closed rule. When a
plan/execute terminal write fails, the previous checkpoint remains the resume
source; ZenForge reports the storage error rather than an unpersisted terminal
outcome.

## Limited Boundaries

ZenForge checkpoints each accepted model chunk as an uncommitted draft before
publishing its event. A hard process interruption marks that attempt
`interrupted`, then `superseded`, and starts a replacement from the committed
conversation boundary at the same logical step. Draft text, draft tool calls,
and observed usage never enter committed messages, pending tools, or aggregate
usage. The public lifecycle is visible through `model.interrupted`,
`model.superseded`, and `model.restarted`.

This is provider-neutral attempt replacement, not a provider-native mid-token
cursor. An error explicitly returned by the model stream is a terminal run
failure; a later `Resume` reports that durable failure rather than silently
retrying it. Legacy custom model streams that close without a dedicated done
frame remain successful for compatibility.

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

Sandbox session state is resumable only when its stored `runId` and
`subtaskId` exactly match the current tool scope. Unscoped legacy state and
cross-scope state open a fresh session instead of attaching to an environment
whose ownership cannot be proven. Sessions intentionally closed by the shell
tool are not persisted for reuse.

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

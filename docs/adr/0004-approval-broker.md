# ADR 0004: Approval Broker

Status: accepted

## Context

`agent-platform` already has HITL and security approval flows, but they are
bound to ZenMind events and APIs:

- `awaiting.ask`
- `request.submit`
- `awaiting.answer`
- `/api/submit`
- WebSocket notifications
- chat pending awaiting state

ZenForge needs approval as a core runtime capability without assuming a frontend
or HTTP API.

## Decision

ZenForge will define an approval broker interface.

Tools and middleware can request approval. Runtime pauses until the broker
returns a decision, times out, or the run is cancelled.

## Interface

```go
type Broker interface {
    Request(ctx context.Context, request Request) (Decision, error)
}

type Request struct {
    ID          string
    RunID       string
    ToolName    string
    Title       string
    Description string
    Risk        RiskLevel
    Options     []Option
    Payload     map[string]any
    Timeout     time.Duration
}

type Decision struct {
    RequestID string
    Action    DecisionAction
    Reason    string
    Scope     DecisionScope
    Payload   map[string]any
}
```

## MVP Brokers

- `approval.AlwaysAllow` for tests and trusted local runs.
- `approval.AlwaysDeny` for locked-down environments.
- `approval.CLI` for interactive CLI use.
- `approval.Channel` for embedding in servers.

## Checkpoint Behavior

Before waiting:

- save checkpoint with `ApprovalSnapshot`;
- append `approval.requested`;
- set run state to waiting.

After decision:

- save checkpoint with decision;
- append `approval.resolved`;
- continue tool execution if approved.

## Adapter Mapping

ZenMind adapter can map:

- core `approval.requested` to `awaiting.ask`;
- `/api/submit` payload to `approval.Decision`;
- core `approval.resolved` to `awaiting.answer`.

Core should not know about submit routes or chat pending awaiting records.

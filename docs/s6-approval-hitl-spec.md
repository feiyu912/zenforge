# S6 Approval And HITL Spec

S6 turns approval into a first-class runtime capability.

The goal is to let tools and policies pause a run, ask for human or application
approval, checkpoint the waiting state, and continue deterministically after a
decision.

## S6 Outcome

After S6, ZenForge should support:

- approval request/decision types;
- approval broker interface;
- CLI approval broker;
- channel/server broker for embedding;
- always-allow and always-deny brokers for tests/config;
- approval checkpoint state;
- timeout and cancellation behavior;
- approval events;
- integration with tool policy middleware.

## Design Principles

1. Approval is core runtime state.
2. UI/API submit protocols are adapters.
3. Waiting approval must checkpoint before blocking.
4. Decisions must be auditable.
5. Approval scopes must be explicit.
6. Default behavior should be safe when no broker is configured.

## Package Plan

```text
approval/
  interface.go
  request.go
  decision.go
  broker.go
  memory.go
  cli/

tool/middleware/
  approval.go
```

## Core Types

### Request

```go
type Request struct {
    ID          string         `json:"id"`
    RunID       string         `json:"runId"`
    ToolCallID  string         `json:"toolCallId,omitempty"`
    ToolName    string         `json:"toolName,omitempty"`
    Operation   string         `json:"operation"`
    Title       string         `json:"title"`
    Description string         `json:"description,omitempty"`
    Risk        RiskLevel      `json:"risk"`
    Options     []Option       `json:"options"`
    Payload     map[string]any `json:"payload,omitempty"`
    CreatedAt   time.Time      `json:"createdAt"`
    ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
}
```

### Risk

```go
type RiskLevel string

const (
    RiskLow      RiskLevel = "low"
    RiskMedium   RiskLevel = "medium"
    RiskHigh     RiskLevel = "high"
    RiskCritical RiskLevel = "critical"
)
```

### Option

```go
type Option struct {
    Action      DecisionAction `json:"action"`
    Label       string         `json:"label"`
    Description string         `json:"description,omitempty"`
    Scope       DecisionScope  `json:"scope,omitempty"`
}
```

### Decision

```go
type Decision struct {
    RequestID string         `json:"requestId"`
    Action    DecisionAction `json:"action"`
    Scope     DecisionScope  `json:"scope,omitempty"`
    Reason    string         `json:"reason,omitempty"`
    Payload   map[string]any `json:"payload,omitempty"`
    DecidedAt time.Time      `json:"decidedAt"`
}
```

### Actions

```go
type DecisionAction string

const (
    DecisionApprove DecisionAction = "approve"
    DecisionReject  DecisionAction = "reject"
    DecisionAlways  DecisionAction = "always"
    DecisionAbort   DecisionAction = "abort"
)
```

### Scope

```go
type DecisionScope string

const (
    ScopeOnce DecisionScope = "once"
    ScopeRun  DecisionScope = "run"
    ScopeRule DecisionScope = "rule"
)
```

## Broker

```go
type Broker interface {
    Request(ctx context.Context, req Request) (Decision, error)
}
```

Built-in brokers:

- `AlwaysAllow`;
- `AlwaysDeny`;
- `CLI`;
- `Channel`;
- `PendingBroker` for server submit routes;
- `Timeout`.

## Channel Broker

For servers and tests:

```go
type ChannelBroker struct {
    Requests  <-chan Request
    Decisions chan<- Decision
}
```

The exact API can be refined, but the concept is:

- runtime emits request;
- host application receives it;
- host application submits decision;
- runtime continues.

For platform servers where submit arrives later by `requestId`,
`PendingBroker` keeps pending requests in memory and exposes:

```go
broker := approval.NewPendingBroker(128)
requests := broker.Requests()
request, ok := broker.Pending("approval_123")
pending := broker.ListPending()
err := broker.Submit(ctx, approval.Decision{
    RequestID: "approval_123",
    Action:    approval.DecisionApprove,
})
```

This mirrors the platform submit-route shape without importing server or
frontend DTOs into the core runtime.

## Tool Middleware Integration

Approval middleware receives an approval plan from policy layers.

Flow:

```text
tool call
  ↓
policy says approval required
  ↓
checkpoint approval waiting state
  ↓
emit approval.requested
  ↓
broker.Request
  ↓
checkpoint decision
  ↓
emit approval.resolved
  ↓
approve -> continue tool
reject -> return tool error
abort -> cancel run
```

## Checkpoint Behavior

Before waiting:

- set `RunState.Phase = approval`;
- set `RunState.Approval.Waiting`;
- save checkpoint;
- append `approval.requested`.

After decision:

- clear `RunState.Approval.Waiting`;
- append decision to `RunState.Approval.Resolved`;
- save checkpoint;
- append `approval.resolved`.

## Timeout Behavior

If approval times out:

- decision action becomes `reject` by default;
- event `approval.expired` emitted;
- tool receives `approval_expired`;
- run may continue or fail depending on policy.

Default MVP:

- timeout rejects the tool call but does not abort entire run unless configured.

## Cancellation Behavior

If context is cancelled while waiting:

- run state becomes cancelled;
- checkpoint terminal state;
- emit `run.cancelled`.

## Approval Scopes

`ScopeOnce`:

- approval applies only to this request.

`ScopeRun`:

- approval applies to matching fingerprint for current run.

`ScopeRule`:

- approval applies to matching rule key for current run.

Persistent cross-run approvals are post-MVP.

## Events

Approval emits:

- `approval.requested`;
- `approval.resolved`;
- `approval.expired`;

Payload includes:

- request ID;
- tool call ID;
- operation;
- risk;
- decision action;
- scope.

## ZenMind Adapter Mapping

ZenMind adapter can map:

- `approval.requested` -> `awaiting.ask`;
- submit payload -> `approval.Decision`;
- `approval.resolved` -> `awaiting.answer`;
- pending awaiting store -> approval waiting snapshot.

Core must not import:

- `/api/submit` DTOs;
- WebSocket notification types;
- chat pending awaiting repository;
- frontend form schema.

## Migration From agent-platform

Source inspiration:

- `internal/hitl`;
- `internal/llm/run_stream_security_approval.go`;
- `internal/llm/run_stream_hitl_submit.go`;
- `internal/hitlsubmit`;
- `contracts.RunControl.ExpectSubmit`;
- `contracts.RunControl.AwaitSubmitWithTimeout`.

Keep:

- rule/fingerprint/scoped approval idea;
- structured approval options;
- form/confirm distinction as payload metadata;
- audit-friendly decision summary.

Change:

- broker replaces platform submit route;
- core events use `approval.*`;
- frontend protocol is adapter-only.

## S6 Tests

Minimum tests:

- always-allow approves;
- always-deny rejects;
- channel broker waits and resumes;
- timeout rejects;
- cancellation while waiting cancels run;
- approval request checkpointed before wait;
- decision checkpointed after wait;
- scope once/run/rule matching;
- approval middleware runs tool only when approved;
- rejected approval returns tool error;
- abort decision cancels run.

## S6 Exit Criteria

- approval can pause and resume a fake tool call;
- approval state survives checkpoint/load;
- CLI broker works for local use;
- no ZenMind submit/chat/server imports;
- S3 shell/file policies can use approval middleware.

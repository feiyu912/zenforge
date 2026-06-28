# Approval Guide

This guide covers ZenForge approval brokers, decision scopes, events, and host
integration points.

## Why Approval Exists

Agents can call powerful tools. Approval lets an application pause before risky
actions, ask a human or policy service, and continue only if allowed.

Typical approval cases:

- shell command outside allowlist;
- workspace write without a fresh read snapshot;
- stale write after a file changed;
- network call to sensitive host;
- destructive operation.

## Broker Config

Always allow:

```go
agent := zenforge.New(zenforge.Config{
    Approval: approval.AlwaysAllow(),
})
```

CLI:

```go
agent := zenforge.New(zenforge.Config{
    Approval: cli.New(os.Stdin, os.Stdout),
})
```

Server applications can use `approval.PendingBroker` when approval requests are
resolved by an HTTP, WebSocket, or queue submit route:

```go
broker := approval.NewPendingBroker(128)

agent := zenforge.New(zenforge.Config{
    Approval: broker,
})

go func() {
    for req := range broker.Requests() {
        notifyFrontend(req)
    }
}()

func submitApproval(ctx context.Context, body []byte) error {
    var decision approval.Decision
    if err := json.Unmarshal(body, &decision); err != nil {
        return err
    }
    return broker.Submit(ctx, decision)
}
```

Platform-specific submit payloads can be translated at the edge before calling
`Submit`; for example, `adapters/zenmind` provides compatibility helpers for
ZenMind routes.

When using `server/harnesshttp`, assign the same broker to
`handler.Approvals` and expose `handler.ServeApprovals` plus
`handler.ServeApproval` from host routes.

The broker keeps pending requests addressable by `requestId`, so platform
routes can inspect `Pending` or `ListPending` and submit the matching decision.
Requests are removed when the waiting run context is canceled.

## Decision Scopes

Once:

- applies only to one request.

Run:

- applies to the exact same non-empty `fingerprint` for the current run.

Rule:

- applies to the exact same non-empty `ruleKey` for the current run.

Approved run/rule decisions create grants in `RunState.Approval.Grants`. Grants
are checkpointed, so matching tool calls can continue after resume without
asking the broker again. Every reuse still appends a resolved audit decision
and emits `approval.resolved` with `reused: true`; it does not emit a new
`approval.requested`.

A run-scoped approval without a request fingerprint, or a rule-scoped approval
without a request rule key, is invalid and fails closed. A different key always
requires a new broker decision.

The harness owns approval routing identity. It overwrites tool-provided
`runId`, `toolCallId`, and `toolName` with the active runtime values before
checkpointing or calling a broker. A broker decision must return the exact
pending `requestId`; mismatches fail closed without creating a grant.
The standalone `tool/middleware.Approval` path enforces the same identity and
scope-key checks before retrying its wrapped invoker.

## Persistent Rule Grants

Cross-run reuse is optional. Configure an `approval.GrantStore` and a
host-owned namespace:

```go
grants := approval.NewMemoryGrantStore()
agent := zenforge.New(zenforge.Config{
    Approval:          broker,
    ApprovalGrants:    grants,
    ApprovalNamespace: approval.Namespace{
        Tenant:  "tenant_1",
        Subject: "user_42",
    },
    ApprovalGrantTTL: 24 * time.Hour,
})
```

`Task.ApprovalNamespace` overrides the config namespace for that task. Both
`Tenant` and `Subject` are required when a store is configured. These values
must come from authenticated host identity, not model or tool input.

Only an approved `ScopeRule` decision is persisted across runs. Persistent
lookup requires an exact match on all four values: `tenant`, `subject`,
`ruleKey`, and `fingerprint`. This is intentionally narrower than the
checkpointed in-run rule scope, which matches its `ruleKey`. `ScopeOnce` and
`ScopeRun` never enter the persistent store.

`ApprovalGrantTTL > 0` sets `ExpiresAt`; zero leaves grants without automatic
expiry. Expired grants behave as not found and are removed on lookup. Hosts can
explicitly revoke the exact tuple with `GrantStore.Revoke`. ZenForge includes
`approval.NewMemoryGrantStore()` for process-local reuse and
`approval/sqlite.Open` for durable reuse.

Leaving `ApprovalGrants` unset, including a typed nil store, preserves the
previous checkpoint-only behavior. Once a store is configured, invalid
namespaces and store read/write errors fail closed and terminate the run rather
than silently asking again or bypassing persistence.

## Events

Approval emits:

- `approval.requested`;
- `approval.resolved`;
- `approval.expired`.

UI and server adapters can map these to their own protocols.

## Safe Defaults

If no broker is configured:

- risky operations should not run automatically;
- the approval request and active tool call are checkpointed;
- `Stream` emits `approval.requested` and closes without a terminal run event;
- `Run` returns `approval.ErrRequired`;
- a later `Resume` with a configured broker continues from the same tool call.

This is a durable pause, not a failed or completed run. Repeated resume attempts
without a broker re-emit the pending request and leave the waiting checkpoint
unchanged.

An `abort` decision is different from a rejection. Rejection produces a failed
tool result and lets the model continue; abort persists a cancelled run and
emits `run.cancelled`. At lower-level middleware boundaries, abort returns an
error matching both `approval.ErrAborted` and `context.Canceled` so hosts can
route it through their normal cancellation path.

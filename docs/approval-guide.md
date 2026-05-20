# Approval Guide

This is a draft user-facing guide for approvals.

## Why Approval Exists

Agents can call powerful tools. Approval lets an application pause before risky
actions, ask a human or policy service, and continue only if allowed.

Typical approval cases:

- shell command outside allowlist;
- file write outside write root;
- stale write after file changed;
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

Server applications can use a channel or custom broker.

## Decision Scopes

Once:

- applies only to one request.

Run:

- applies to same fingerprint for the current run.

Rule:

- applies to same rule key for the current run.

Cross-run persistent approvals are intentionally post-MVP.

## Events

Approval emits:

- `approval.requested`;
- `approval.resolved`;
- `approval.expired`.

UI and server adapters can map these to their own protocols.

## Safe Defaults

If no broker is configured:

- risky operations should not run automatically;
- tools should return `approval_required` or fail closed.


# Server HTTP Guide

The `server/harnesshttp` package is a small adapter layer for host platforms
that want to expose a ZenForge agent over HTTP.

It deliberately does not configure models, tools, auth, tenancy, or routing.
Those stay in the host server. The handler receives an already-configured
agent and exposes:

- `POST /run` style JSON input to `Agent.Stream`;
- `GET /resume?runId=...` or `POST /resume` to `Agent.Resume`;
- `GET /events?runId=...` to replay persisted event log entries;
- optional `POST /approval` style approval submit to `approval.PendingBroker`;
- standard Server-Sent Events responses through `server/sse`.

```go
agent := zenforge.New(config)
approvalBroker := approval.NewPendingBroker(128)
handler := harnesshttp.New(agent, sse.Options{RetryMillis: 1500})
handler.Events = eventStore
handler.Approvals = approvalBroker

mux := http.NewServeMux()
mux.HandleFunc("/run", handler.ServeRun)
mux.HandleFunc("/resume", handler.ServeResume)
mux.HandleFunc("/events", handler.ServeEvents)
mux.HandleFunc("/approval", handler.ServeApproval)
```

## Access Control

Host platforms can attach an access controller before exposing run, resume, or
event replay endpoints:

```go
handler.Access = harnesshttp.AccessFunc(func(ctx context.Context, r *http.Request, op harnesshttp.Operation) (harnesshttp.AccessDecision, error) {
    user, tenant, ok := authenticate(r)
    if !ok {
        return harnesshttp.AccessDecision{}, harnesshttp.ErrUnauthorized
    }
    if !allowed(user, tenant, op) {
        return harnesshttp.AccessDecision{}, harnesshttp.ErrForbidden
    }
    return harnesshttp.AccessDecision{
        Meta: map[string]any{
            "userId":   user.ID,
            "tenantId": tenant.ID,
        },
    }, nil
})
```

For `ServeRun`, trusted access metadata is merged into `zenforge.Task.Meta` and
wins over client-supplied metadata on key conflicts. For `ServeResume` and
`ServeEvents`, the same hook authorizes the target run id. For
`ServeApproval`, the handler resolves the pending approval request first and
authorizes the associated run id before submitting the decision.

ZenForge still does not own auth, sessions, tenancy, or policy lookup. The hook
only gives platform code a stable place to enforce them before calling the
harness.

Run request:

```json
{
  "runId": "run_123",
  "input": "Review this repository and propose a refactor plan.",
  "meta": {
    "sessionId": "session_abc"
  }
}
```

Resume request:

```json
{
  "runId": "run_123"
}
```

Event replay request:

```text
GET /events?runId=run_123&afterSeq=42&limit=100
```

`afterSeq` and `limit` are optional. Replay uses the configured event store as
the read model and streams matching events as SSE frames.

Approval submit request:

```json
{
  "requestId": "approval_123",
  "action": "approve",
  "scope": "once",
  "reason": ""
}
```

Approval submit requires `handler.Approvals = approval.NewPendingBroker(...)`.
The host platform still owns the route path, auth, UI payload, and notification
fanout; the handler only translates the neutral decision into the pending
broker.

## Platform Boundary

Use this package below the platform edge:

- platform auth and session lookup happen in the access hook or before calling
  the handler;
- platform catalog, memory, and policy are translated into `zenforge.Config`;
- ZenForge emits normalized runtime events;
- the handler streams live or replayed events without inventing
  platform-specific DTOs.

This keeps the harness reusable while giving ZenMind or another host platform a
concrete integration point.

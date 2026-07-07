# Server HTTP Guide

The `server/harnesshttp` package is a small adapter layer for host platforms
that want to expose a ZenForge agent over HTTP.

It deliberately does not configure models, providers, auth, tenancy, durable
storage, or routing. Those stay in the host server. The application selects an
OpenAI or Anthropic protocol adapter (and any compatible base URL), supplies
credentials, and chooses route paths.

For detached runs, `harnesshttp.NewRuntime` is the canonical assembly. It
creates one shared approval inbox, `eventlog.Bus`, and `eventlog.FanoutStore`,
then wires the agent, manager, and handler to those same instances. The package
exposes:

- `POST /run` style JSON input to `Agent.Stream`;
- `GET /resume?runId=...` or `POST /resume` to `Agent.Resume`;
- `GET /events?runId=...` to replay persisted event log entries;
- optional `GET /live?runId=...` style live event fanout from `eventlog.Bus`;
- optional `GET /approvals?runId=...` style pending approval query;
- optional `POST /approval` style approval submit to `approval.Inbox`;
- detached start, resume, status, list, attach, and cancel handlers;
- standard Server-Sent Events responses through `server/sse`.

```go
runtime, err := harnesshttp.NewRuntime(config, durableEvents, harnesshttp.RuntimeOptions{
    Access: access,
    SSE:    sse.Options{RetryMillis: 1500},
    Manager: harnesshttp.RunManagerOptions{
        MaxActive:         32,
        RunTimeout:        30 * time.Minute,
        TerminalRetention: 5 * time.Minute,
    },
    ApprovalBuffer: 128,
    LiveBuffer:     128,
})
if err != nil {
    return err
}
defer runtime.Close(shutdownContext)

mux := http.NewServeMux()
mux.HandleFunc("/run", runtime.Handler.ServeRun)
mux.HandleFunc("/resume", runtime.Handler.ServeResume)
mux.HandleFunc("/events", runtime.Handler.ServeEvents)
mux.HandleFunc("/live", runtime.Handler.ServeLiveEvents)
mux.HandleFunc("/approvals", runtime.Handler.ServeApprovals)
mux.HandleFunc("/approval", runtime.Handler.ServeApproval)
mux.HandleFunc("/runs/start", runtime.Handler.ServeDetachedStart)
mux.HandleFunc("/runs/resume", runtime.Handler.ServeDetachedResume)
mux.HandleFunc("/runs/status", runtime.Handler.ServeDetachedStatus)
mux.HandleFunc("/runs", runtime.Handler.ServeDetachedRuns)
mux.HandleFunc("/runs/attach", runtime.Handler.ServeDetachedAttach)
mux.HandleFunc("/runs/cancel", runtime.Handler.ServeDetachedCancel)
```

These paths are examples, not framework-owned routes. `Runtime.Close` rejects
new detached work, cancels active runs, and waits for their drainers; it does
not close the caller-owned durable store. The application owns HTTP server
shutdown and store closure.

The original synchronous `ServeRun` and `ServeResume` handlers remain
available. Their execution uses the request context, so disconnecting cancels
the synchronous stream.

For shared approval routing, pass a durable inbox instead of using the default
process-local `PendingBroker`:

```go
approvalStore, err := approvalsqlite.OpenInbox(ctx, "./approvals.db")
if err != nil {
    return err
}
approvalInbox, err := approval.NewStoreBroker(approvalStore, approval.StoreBrokerOptions{})
if err != nil {
    return err
}
runtime, err := harnesshttp.NewRuntime(config, durableEvents, harnesshttp.RuntimeOptions{
    ApprovalInbox: approvalInbox,
})
```

The handler uses that same inbox for `GET /approvals` and `POST /approval`, and
the agent uses it as `Config.Approval`. A submit response means the decision was
committed to the inbox; it does not mean the run has already consumed the
decision or reached a terminal event.

For shared detached execution routing, pass a run registry:

```go
runRegistry, err := harnesshttp.OpenSQLiteRunRegistry(ctx, "./detached-runs.db")
if err != nil {
    return err
}
runtime, err := harnesshttp.NewRuntime(config, durableEvents, harnesshttp.RuntimeOptions{
    Manager: harnesshttp.RunManagerOptions{
        Registry:      runRegistry,
        OwnerID:       "worker-a",
        LeaseDuration: 30 * time.Second,
    },
})
```

The registry atomically claims start/resume work, refreshes an active lease,
preserves manager status for `GET /runs/status` after process-local retention
expires, and backs `GET /runs` with a durable list of `RunInfo` snapshots.
`harnesshttp.NewMemoryRunRegistry` is available for tests and embedded
single-process deployments; `OpenSQLiteRunRegistry` is the durable local
implementation.

## Detached Run Lifecycle

Start accepts the synchronous run body:

```http
POST /runs/start
Content-Type: application/json

{"runId":"run_123","input":"Review this repository.","meta":{"sessionId":"s_1"}}
```

`runId` is optional for start. Success returns `202 Accepted`:

```json
{
  "runId": "run_123",
  "status": "starting",
  "startedAt": "2026-07-07T08:00:00Z",
  "updatedAt": "2026-07-07T08:00:00Z",
  "finishedAt": "0001-01-01T00:00:00Z"
}
```

Resume requires durable event history and accepts
`GET /runs/resume?runId=run_123` or `POST` with `{"runId":"run_123"}`. It also
returns `202` `RunInfo` JSON. `GET /runs/status?runId=run_123` returns `200`;
statuses are `starting`, `running`, `waiting_approval`, `completed`, `failed`,
and `cancelled`. Terminal snapshots may include `error` and `finishedAt`.
`GET /runs` returns `{"runs":[...]}` ordered by newest status update, then
run ID.

Attach uses `GET /runs/attach?runId=run_123&afterSeq=42`. It replays durable
events after the cursor, then follows live appends as SSE. If `afterSeq` is
absent, a non-negative integer `Last-Event-ID` is used; explicit `afterSeq`
takes precedence. Closing the connection or a response-writer failure stops
only that follower. The detached run and pending approval continue.

Cancellation is explicit:

```text
DELETE /runs/cancel?runId=run_123
POST /runs/cancel  {"runId":"run_123"}
```

Success returns `202` with `{"runId":"run_123"}`. Cancelling an already
cancelled run is idempotent; another terminal state conflicts. Manager shutdown
and `RunTimeout` also stop execution, with timeout reported as `failed`.

`MaxActive > 0` bounds active runs and overflow maps to HTTP `429`.
`TerminalRetention` defaults to five minutes; a negative value retains
process-local status until `Forget` or the manager is discarded. Retention
removes only the manager's in-memory status, never durable events or registry
records. Without a registry, status after retention is unavailable from that
manager. With a registry, `Get` and `ServeDetachedStatus` fall back to the
durable registry record.

By default, duplicate reservation is atomic only within one manager. With a
registry, duplicate start/resume is fenced by the registry lease, and stale
owners cannot refresh or release a stolen lease. A second manager can read
durable status and attach by replaying/polling the shared event store, even
though the live event bus remains process-local. Applications still own
reconnect routing, model/tool side-effect idempotency, provider configuration,
auth, and shutdown.

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

For `ServeRun` and `ServeDetachedStart`, trusted access metadata is merged into
`zenforge.Task.Meta` and wins over client-supplied metadata on key conflicts.
The same hook authorizes synchronous and detached resume, status, attach,
cancel, event, and live-event operations by run id. For
`ServeApproval`, the handler resolves the pending approval request first and
authorizes the associated run id before submitting the decision.
`ServeApprovals` authorizes the requested run id before returning pending
approval requests for that run only.

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

Live event request:

```text
GET /live?runId=run_123
```

Live streaming requires `handler.Bus = eventlog.NewBus()` or the bus from an
`eventlog.FanoutStore`. Plain live mode only streams events published after the
client subscribes.

For one race-free replay-to-live stream, configure both `handler.Events` and
`handler.Bus`:

```text
GET /live?runId=run_123&replay=true&afterSeq=42
```

The handler subscribes first, catches up from the durable store, de-duplicates
by sequence, and then follows new appends. On reconnect it also accepts the SSE
`Last-Event-ID` header when `afterSeq` is absent; an explicit query cursor takes
precedence.

Approval query request:

```text
GET /approvals?runId=run_123
```

Approval query returns pending requests for one run:

```json
{
  "approvals": []
}
```

Approval submit request:

```json
{
  "requestId": "approval_123",
  "action": "approve",
  "scope": "once",
  "reason": ""
}
```

Approval submit requires either `handler.ApprovalInbox = ...` or the compatible
single-process `handler.Approvals = approval.NewPendingBroker(...)`.
The host platform still owns the route path, auth, UI payload, and notification
fanout; the handler only translates the neutral decision into the approval
inbox. Conflicting second decisions return `409`; identical retries are
accepted by durable inboxes.

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

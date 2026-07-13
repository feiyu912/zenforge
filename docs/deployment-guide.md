# Deployment Guide

ZenForge is an application-owned harness, not a server control plane. The host
application chooses HTTP paths, authentication, tenancy, provider credentials,
storage services, worker discovery, and load-balancer behavior. This guide
defines the routing and shutdown contract that the host must preserve when it
deploys `server/harnesshttp`.

## Choose A Topology

### One process

For an embedded service or one replica, use `harnesshttp.NewRuntime` with the
application's durable event and checkpoint stores. The default approval inbox,
live event bus, and detached-run ownership are process-local. No sticky routing
is required because every request reaches the only owner.

### Multiple processes on one host

Give every process a unique, stable `RunManagerOptions.OwnerID`, and configure:

- one shared durable event store;
- one shared checkpoint store used by the agent configuration;
- one durable `approval.Inbox`;
- one shared `RunRegistry`.

The built-in SQLite inbox and run registry are suitable for processes sharing
one local host and filesystem. Each process should open its own connection to
the same database path. Do not place SQLite databases on an arbitrary network
filesystem and treat them as a multi-host coordination service.

### Multiple hosts

Implement the exported event, checkpoint, approval, and `RunRegistry`
interfaces over storage with the atomicity and consistency required by those
interfaces. `RunRegistry.Claim` must be an atomic compare-and-claim operation;
`Update` and `Release` must fence stale lease tokens. `OwnerID` must identify a
currently routable worker instance, not merely a deployment or region.

ZenForge does not provide worker discovery or a distributed database. The host
may use its existing platform services while keeping their DTOs and clients
outside the harness packages.

## Route Requests

The following table is the supported detached-run routing contract:

| Operation | May reach any replica? | Requirement |
| --- | --- | --- |
| start | yes | Shared registry atomically selects one owner. Duplicate `runId` is rejected. |
| resume | yes, when no active lease exists | Shared events/checkpoints must be visible; an unexpired owner lease rejects the claim. |
| status and list | yes | The shared registry supplies durable `RunInfo` records and listing. |
| attach | yes | The shared event store supplies replay and polling. The process-local bus only accelerates delivery on the owner. |
| list or submit approval | yes | All replicas must use the same durable approval inbox and the same access policy. |
| cancel | yes with `RunCancellationRegistry` | Built-in memory/SQLite registries persist a request that the lease owner consumes on heartbeat. Otherwise route to `RunInfo.OwnerID`. |
| explicit terminal cleanup | yes with `RunRegistryDeleter` | `RunManager.Forget` removes only the terminal registry status record; events and checkpoints retain their own application lifecycle. |

`RunCancellationRegistry` is an optional extension, so existing custom
registries remain source compatible. Without that extension, an edge service
reads `RunInfo.OwnerID` before forwarding cancel. Treat owner identifiers as
internal routing data: authorize the run before lookup, do not accept an owner
value supplied by the client, and do not use it as a tenancy decision. With the
extension, `RunManager.Cancel` stores a remote request when it has no local run.
HTTP acceptance means the request was stored; terminal status proves the owner
consumed it. A worker that claims a stale run checks an inherited cancellation
before opening `Agent.Resume`, preventing model or tool work in the heartbeat
window.

Attach clients should persist the latest SSE sequence and reconnect with
`Last-Event-ID` or `afterSeq`. A reconnect may use another replica. Replay and
live-follow de-duplicate by sequence, so clients must process events in sequence
order and should make their own projections idempotent.

## Configure Shared Runtime State

All replicas for one run namespace must agree on these values:

- run ID namespace and access-control interpretation;
- event and checkpoint schemas;
- model protocol, compatible base URL, and model behavior;
- tool names, schemas, policies, and skill bundle identity;
- approval tenant/subject identity and rule-grant namespace;
- registry lease duration and a heartbeat interval shorter than that duration.

Do not send the same run to workers with different tool or skill definitions.
Bundle fingerprints and application release identifiers are useful admission
metadata for detecting this condition before execution.

The default live `eventlog.Bus` remains process-local. Cross-replica attach is
durable-store polling, not cross-process pub/sub. Applications that need lower
latency may provide affinity to `OwnerID` or add notifications around their
durable store, but durable replay remains the source of truth.

## Make Side Effects Idempotent

Checkpoint resume replaces an interrupted model attempt and may retry a tool
whose completion was not durably observed. External writes therefore need an
application-owned idempotency record. A practical key is:

```text
tenant + runId + toolCallId + operationFingerprint
```

`tool.Context` supplies `RunID` and `ToolCallID`. Compute the operation
fingerprint from canonicalized, non-secret arguments and the target operation.
Store the key and committed result atomically with the external write when the
target system permits it. A repeated call with the same key should return the
committed result; the same key with different canonical arguments should fail
closed. Never log secrets merely to construct or audit the fingerprint.

Model-provider retries can also duplicate billable requests. Use a provider's
idempotency facility when its selected protocol exposes one; ZenForge's
portable provider interface does not invent a cross-provider idempotency
header.

## Roll And Shut Down

`Runtime.Close(ctx)` rejects new detached starts/resumes, cancels executions
owned by that process, and waits for their drainers. It does not transfer an
active run to another worker and does not close caller-owned stores.

For a graceful rollout:

1. Remove the worker from start/resume routing.
2. Stop accepting new HTTP requests and allow existing attachments to detach.
3. Wait for owned runs to become terminal, or explicitly choose to cancel them.
4. Call `Runtime.Close` with a bounded context.
5. Close the HTTP server, provider clients, registry, approval, checkpoint, and
   event stores in application-defined order.

If a worker crashes, wait for its registry lease to expire before explicitly
resuming the run on another worker. A host recovery loop may call:

```go
results, err := runtime.Manager.RecoverStale(ctx, harnesshttp.RecoveryOptions{
    Max: 32,
})
```

The method only attempts expired, nonterminal records and uses the normal
resume claim, so concurrent recovery workers remain fenced. Lease expiry
prevents two owners; it does not automatically start recovery. Resume begins
from the last committed checkpoint boundary and does not continue a provider
stream mid-token.

## Deployment Acceptance

Before serving production traffic, prove the following in the real deployment:

- two workers racing to start one run produce exactly one owner;
- status, list, approval submission, and attach work through a non-owner;
- cancel through a non-owner is consumed by the owner and reaches a terminal
  event when the registry implements `RunCancellationRegistry`;
- killing the owner expires its lease and an explicit resume completes;
- reconnect after worker change produces a gap-free, duplicate-tolerant event
  projection;
- repeated external tool calls do not duplicate committed side effects;
- rollout waits or cancels according to the application's documented policy;
- provider credentials, compatible base URLs, sandbox services, and durable
  stores are exercised in the target environment.

Repository tests prove the local interfaces and SQLite implementations. They
cannot prove a platform's load balancer, distributed stores, credentials,
Container Hub deployment, or shutdown orchestration.

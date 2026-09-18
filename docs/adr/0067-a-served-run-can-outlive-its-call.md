# ADR 0067: A Served Run Can Outlive The Call That Asked For It

Status: accepted

## Context

`zenforge mcp-server --allow-run` serves `zenforge_run`, and it is a blocking
tool: the call returns the run's answer, and the operator's `--run-timeout`
(default 15 minutes) bounds it. That is a sane default, but it makes the tool
unusable for exactly the task a remote caller most wants to hand over — one
that takes longer than the caller's own call budget — and the parity plan
recorded the missing half: a detached-run registry plus a status tool.

Two things were missing, and only one of them is a tool. A caller needs to be
told *which* run it now owns at the moment the run starts (the agent picks its
own run id, and `Agent.Run` only reports it at the end), and the server needs
somewhere to keep what it knows about a run it started, because the durable
checkpoint store describes a run's progress but not its answer or its
explanation.

## Decision

### `zenforge_run` accepts `detach: true`

The blocking behaviour is unchanged and remains the default — a caller that
wants an answer in one call still gets one. With `detach: true`:

- the call returns as soon as the run has started, with the run id, `status:
  running`, and `detached: true`. It is not an error result: the run started;
- the run keeps working on the server, **bound to the server's context, not the
  call's**. This is the whole point: an MCP server finishes the request it
  answered, so a run bound to that request would be cancelled the moment the
  caller was told it had started. It is still bounded by `--run-timeout`;
- the run slot is still one at a time, and a detached run holds it for its
  lifetime. A second `zenforge_run` while one runs reports that a run is in
  progress and points at the status tool rather than queueing silently. This is
  what makes the shared approval recorder safe: one run at a time means one
  recorder in use.

`Task.RunID` accepts a pre-assigned id, so the server names the run before it
starts. The naming rule moved to an exported `zenforge.NewRunID()` instead of
being copied: a caller that must name a run needs the package's format, not a
second one.

### `zenforge_run_status` reports one run

Read-only, always advertised (it needs no operator grant), and it answers in
three steps:

1. **the registry** — a run this server started, live (`running` with
   `startedAt`) or terminal (its answer, or the explanation the blocking tool
   would have returned, with `finishedAt`). A blocking run is registered too,
   so a caller whose connection died can still ask about it;
2. **the durable store** — a run this install recorded but this server did not
   run: the checkpoint summary's status, phase, step, and save time, marked
   `recorded: true` and saying plainly that the live state is not known here;
3. **`unknown`** — an id in neither place, as a normal result rather than a
   handler error, so a caller can tell "no such run" from "the status call
   broke".

The registry is in-process on purpose: it describes runs that live in this
process, and the durable store already answers for runs that outlive it. It is
bounded at 100 terminal records, oldest evicted first, and a live run is never
evicted — one long run at the head of the order must not stop older records
from being dropped.

### Shutdown cancels and waits

`mcpServerCommand` shuts the registry down after `Serve` returns: it cancels
every live run and waits, bounded at 30 seconds, for them to settle. A server
that exits while a detached run is mid-write into its workspace would leave the
workspace in a state nobody chose.

A detached run cannot be cancelled early by a caller; there is no cancel tool
yet. `--run-timeout` is the bound, and the gap is recorded.

## Consequences

- The C20 gap is closed. A remote caller can hand over work longer than one
  call can hold, then poll for it, and a caller that cannot wait for *any*
  reason can still find out what happened to a run it or another process
  started.
- Blocking and detached runs share one code path for the run itself
  (`runServedTask`), so the two cannot drift in how they classify an approval
  stop, a timeout, a cancellation, or a failure — only in how the outcome is
  delivered.
- A detached run's answer is served from the registry while the server is up;
  after it exits, only the durable summary remains. That is a real limit of an
  in-process registry and it is documented rather than hidden.
- The refactor that unified the two paths briefly dropped the blocking path's
  `--run-timeout`, which the existing timeout test caught. The bound is applied
  per path now, and the test is the reason the regression did not survive.

## Alternatives Rejected

### Make detaching the default

A blocking call is predictable for the caller: it either answers or fails
within a bound it can see. Detaching by default would change the served
contract for every existing caller and make "no answer" the normal outcome.
It is a per-call choice, and the flag defaults to the old behaviour.

### Return the answer later over a notification, without a status tool

The server has no way to reach a caller that has gone away, so an answer
delivered by notification is lost exactly when it matters. A status tool makes
the caller come back on its own schedule, which is also what lets a run be
inspected by a process that did not start it.

### Keep the run's state only in the durable store

Then the answer would have to be reconstructed from events, and a detached run
that is still going would be indistinguishable from one that died, because a
checkpoint is written at step boundaries rather than at every moment. The
registry knows what the process knows.

### A cross-process registry (leases, claims) like the HTTP server's

`server/harnesshttp` has one because several HTTP managers may own the same run
set. An MCP server is one stdio process holding one agent; a lease protocol
here would be machinery for a problem this surface does not have.

## Verification

`go test ./cli/` — `TestDetachedServedRunReportsRunningThenItsAnswer` (the call
returns a run id and `running` before the run can finish, the status tool
reports it live, and then its answer), `TestDetachedRunOutlivesTheCallThatAskedForIt`
(the call's context is cancelled and the run still completes; fails when the
detached run is bound to the call), `TestServedRunStatusReportsDurableRunsAndUnknownIDs`
(a server that did not run it falls back to the durable summary, an unknown id
is `unknown` and not an error, and a missing `runId` is refused),
`TestServedRunRegistryBoundsItsMemoryAndNeverDropsALiveRun`,
`TestServedRunRegistryShutdownCancelsAndWaits`, and
`TestServedRunStatusToolIsReadOnlyAndTheRunToolOffersDetach`. The pre-existing
`TestServedRunReportsATimeoutWithTheRunID` pins the blocking path's timeout.
Plus `go test ./... -count=1`, `go test -race ./cli/`, `go vet ./...`,
`gofmt -l`, and `go test ./docs/...`.
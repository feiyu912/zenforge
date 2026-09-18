# ADR 0068: A Run A Caller Can Stop

Status: accepted

## Context

ADR 0067 let a caller detach a served run: the call returns the run id as soon
as the run has started, and the run keeps working on the server, bounded by
`--run-timeout` (default 15 minutes). The gap that decision recorded is that
the caller has no way to stop it early — a caller that detaches the wrong task,
or whose task turns out to be pointless, has to wait out the operator's bound
while the run holds the single run slot and keeps writing to the workspace.

Everything a cancellation needs already existed: the registry kept a
`context.CancelFunc` per live run and dropped it when the run settled.

## Decision

### `zenforge_run_cancel` stops a run this server is running

It takes a `runId`, calls the registry's cancel, and answers with what it did.
The tool is advertised **only with `--allow-run`**, and without a read-only
hint: the operator's flag is the capability list, so the grant that lets a
caller start work is the grant that lets it stop work, and a conforming client
asks its own operator before calling either.

### Cancellation is asynchronous, and the answer says so

`requestCancel` cancels the run's context and reports that the request was
accepted. The run then has to unwind — a model call has to see the
cancellation, a tool call has to return — so the tool waits up to two seconds
for the run to reach a terminal state and answers with the status it found:
`cancelled` in the usual case, or `cancelling` when the run has not settled
yet, with `cancelled: true` either way, because the request *was* accepted. The
status tool is how the caller checks again.

### Three outcomes, and only usage is an error

| what the registry says | answer |
| --- | --- |
| live, cancellation requested | `cancelled: true`, status `cancelled` or `cancelling` |
| known and not running | `cancelled: false`, `alreadyFinished: true`, its terminal status |
| another process's run (durable record only) | `cancelled: false`, `recorded: true`, its last recorded status |
| an id in neither place | `cancelled: false`, status `unknown` |

Only a missing `runId`, or arguments that are not JSON, is a handler error. A
run that has already finished is not a failure of the caller's intent — the
work is stopped, which is what it asked for — and reporting it as an error
would make retrying a cancel look like a broken call. A run this server cannot
reach is a fact about ownership, and the answer names it.

### Blocking runs are registered too

The blocking path now registers its run at start and names it before it runs,
so a run is visible (and stoppable) while it is in flight rather than only
after it settles. That required separating "detached" from "has a pre-assigned
id", which `runServedTask` now takes as its own argument instead of inferring
from the id being non-empty.

## Consequences

- The operational trap ADR 0067 recorded is closed: a detached run can be
  stopped by the caller that started it, and the cancellation path is the same
  context the run already carried, so no new shutdown machinery was needed.
- A cancel that races the run's own completion is answered with the terminal
  status, not with a spurious success: the registry is consulted under one
  lock, so "requested" and "already finished" cannot both be reported.
- The single run slot is released as soon as the cancelled run unwinds, so a
  caller that cancels can start its next task without waiting for the timeout.
- Cancellation exposed a classification defect the detach batch had not: a
  cancellation that landed during a checkpoint load surfaced as
  `failed: load latest checkpoint: context canceled` rather than `cancelled`,
  because the store's error does not chain to `context.Canceled`. The served
  run now classifies by the run's own context when the bound was hit, which
  fixes the timeout path in the same way. The race build caught it; a
  classification that reads only the error chain is not enough.
- `zenforge_run_cancel` cannot stop a run owned by another process. That is a
  property of an in-process registry (ADR 0067) rather than of this tool, and
  the answer says which case it is instead of pretending to try.

## Alternatives Rejected

### Cancel by killing the run's goroutine

Go has no safe way to do that, and a run holds workspace writes, checkpoints,
and events: cancelling the context is the mechanism the whole harness already
honours, and it lands at a defined point rather than in the middle of a write.

### Wait for the run to settle before answering

Then a cancel would be as slow as the thing it is trying to stop, and a slow
run would time out the cancel call too. A bounded wait with a `cancelling`
answer keeps the tool responsive and the status honest.

### Report "already finished" as an error

The caller's intent is satisfied. `IsError` would tell a model that its call
failed when the work it wanted stopped is already stopped, and the model would
retry a cancel that cannot succeed differently.

### Expose cancellation without `--allow-run`

Then a server that cannot start a run could still be asked to stop one, and the
capability list would have two halves that disagree. One grant, two verbs.

## Verification

`go test ./cli/` — `TestServedRunCancelStopsADetachedRun` (a detached run is
reported running, cancel answers `cancelled: true`, and the status tool then
reports the terminal `cancelled` with the explanation rather than an answer;
fails when the registry's cancel is not called),
`TestServedRunCancelReportsAnAlreadyFinishedRun` (a finished run answers
`alreadyFinished: true` with its status and no error),
`TestServedRunCancelReportsWhatItCannotStop` (another process's run is reported
as `recorded` and uncancellable, an unknown id is `unknown`, and a missing
`runId` is refused), and the extended
`TestMCPServerRunToolAppearsOnlyWithTheOperatorGrant` (the grant adds both
verbs, neither advertised read-only, and neither exists without it). Plus
`go test ./... -count=1`, `go test -race ./cli/`, `go vet ./...`, `gofmt -l`,
and `go test ./docs/...`.
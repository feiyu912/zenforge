# ADR 0069: A Schedule Outlives The Process That Added It

Status: accepted

## Context

ZenForge already had both halves of a repeating task, and neither survived a
restart. `run --schedule "every 1h"` loops inside one process: closing the
terminal ends the schedule. Commands (`cli/commands.go`, ADR 0047) are canned
tasks, and the parity plan's C19 row records what is still missing: schedules
that keep running, and a trigger that can start a run from outside.

The schedule *parser* was already there (`schedule.Parse`), with an interval
form and a subset of cron, so the missing piece was not "when does it run" but
"who remembers that it should".

## Decision

### The schedule is a file, and the timer is external

`schedule add|list|remove` keep schedules in `<checkpoint dir>/schedules.json`:
the spec, the task, the workspace it was added in, when it runs next, and how
the last firing went. The file is plain JSON, written by rename so a reader
never sees a half-written set.

Nothing in ZenForge keeps a timer alive. `schedule run-due` is the other half,
meant to be started by cron, launchd, or a systemd timer: it reads the file,
runs every schedule whose next run is at or before now, records each firing,
and exits. A restart is therefore not a special case — the next invocation
simply finds whatever is overdue, which is what makes a schedule durable
without a daemon to supervise.

### Catch-up counts missed windows instead of replaying them

A schedule whose process was gone for three hours has one overdue window, not
three. `RecordRun` advances the next run from *now* and adds the windows that
passed unrun to `missed`, which `schedule list` and `run-due` report. Replaying
them would turn a weekend of downtime into a burst of runs against the same
workspace; silently skipping them would hide that the schedule was not kept.

### A spec that can never match again is removed

A leap-day schedule (`0 0 29 2 *`) is legal and, once no 29 February falls
inside the parser's five-year search horizon, can never be due again. A firing
that finds no next time removes the entry rather than leaving it in the file
looking active, and `run-due` says so. An entry that is permanently stalled is
worse than no entry: it reads as a schedule that is simply waiting.

### A failed firing is recorded, advanced, and reported in the exit status

One failed firing does not end the schedule — the run is recorded as `failed`,
the schedule advances, and the remaining due schedules still run. But
`run-due` exits non-zero when any firing failed, because for an unattended
timer the exit status is the only channel there is: the runs happened and are
in the checkpoint store, and the status is how cron or launchd notices.

### The workspace is recorded with the schedule

The workspace comes from the standard `--workspace` option and is stored in the
entry, so a firing runs where the schedule was added rather than wherever the
timer happens to start it. A firing gets its own copy of the options and
drains its own closers, so one firing's MCP processes and stores do not leak
into the next.

## Consequences

- C19's durable half is closed. What remains of that row is the signed-webhook
  endpoint (a run triggered from outside) and per-user command directories.
- `schedule run-due` is idempotent per invocation but not concurrency-safe
  against a second copy of itself: two overlapping timer invocations can both
  see a schedule as due. The documented contract is that the timer does not
  run it concurrently with itself.
- Writing `run-due` exposed a real bug in the new code, caught by its own test:
  the shared `scheduleFlags` helper returned the options by value before
  `flag.Parse` had written into them, so every flag was ignored and the run
  went to the default endpoint instead of the test stub. It returns a pointer
  now, and the comment says why.
- `splitScheduleArgs` reads which options take a value from the option flag set
  itself (`optionValueFlags`), rather than from a hand-kept list that would
  silently mis-split the line when a new option is added.

## Alternatives Rejected

### Keep the in-process loop and document it as "schedules"

Then a schedule dies with its terminal, which is exactly the gap the parity row
names. The loop is still there for a person who wants a task repeated while
they watch it; it is not what a schedule is.

### A long-running scheduler daemon

It needs supervision, a lock so two copies do not double-run, and a story for
restarts. An external timer plus a one-shot `run-due` gets the same behaviour
from tools every operating system already has, and leaves nothing running when
there is nothing to do.

### Store schedules in the checkpoint store

The store is an append-only event log for runs, and a schedule is mutable state
with a next-run time. A small file that a person can read, edit, and back up is
the right shape for it, and the runs it produces still live in the store.

### Replay every missed window

It looks more faithful and behaves worse: downtime becomes a queue of runs
firing back to back into one workspace. Counting them is honest about what
happened and lets the operator decide what to do about it.

## Verification

`go test ./schedule/` — `TestStoreKeepsSchedulesAcrossReopen` (a second Open
sees what the first stored, and removal persists),
`TestStoreValidatesWhatItIsGiven`, `TestDueAndRecordRunAdvancePastNowWithoutReplaying`
(a three-and-a-half-hour gap is one firing with two counted skips),
`TestRecordRunRemovesAScheduleThatCanNeverMatchAgain` (from 2097 no 29 February
falls inside the horizon, and 2100 is not a leap year). `go test ./cli/` —
`TestScheduleCommandAddsListsAndRemoves`, `TestScheduleCommandRejectsBadUsage`,
`TestScheduleRunDueRunsAnOverdueScheduleAndReportsWhatItSkipped` (a dry run
consumes nothing; the real firing records a run id that the checkpoint store
then knows), `TestScheduleRunDueJSONIsMachineReadableAndEmptyWhenNothingIsDue`,
`TestScheduleRunDueFailsLoudly`. Plus `go test ./... -count=1`,
`go test -race ./cli/ ./schedule/`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.
# ADR 0058: A Job Is Terminal Only After Its Output Is Drained

Status: accepted

## Context

`jobs.Manager` runs a shell command and keeps its stdout and stderr in
bounded buffers that a caller reads by offset. `Start` returns immediately;
`Run` is the foreground form and returns the job together with one read of
its output. Both take the job's terminal state as permission to read:
`Run` waits for the record's `done` channel and then calls `Output`.

That ordering was wrong. The record was declared terminal by the reaper, as
soon as `command.Wait` returned, in a goroutine entirely independent of the
goroutines copying the pipes into the buffers. The bytes a command wrote are
still in the pipe when its process exits, so `Run` could read the buffer
before the copy had landed — and the race was not merely a read of a
half-written buffer: the pipes were created with `os/exec`'s `StdoutPipe`,
whose read end `Wait` closes. A read in flight when `Wait` closed it lost the
bytes behind it, and the collector ignored the error. CI produced exactly
that: `Run("echo out; echo err >&2; exit 3")` reported exit code 3 and
`stdout = ""`.

## Decision

### The manager owns the pipes, and the parent closes its write ends

`Start` creates each pipe with `os.Pipe`, hands the write end to the command,
and closes the parent's copy of the write end as soon as the child is
started. The read ends belong to the manager and are never closed by
`command.Wait`. This is what makes the drain observable: EOF means the child
is gone, and nothing else can cut a read short.

### A drain is complete only when both streams have ended

`collect` copies stdout and stderr to their buffers and closes a `drained`
channel only after both copies have returned. One stream reaching EOF is not
a drain.

### The reaper announces terminal after the drain, not after the reap

`wait` still reaps the process first and records what only the reap can know
— exit code, end time, terminal status, error — under the manager's lock. It
then waits for `drained` before closing the record's `done` channel. A caller
that sees a terminal job therefore sees its output, which is the contract
`Run` already implied.

### The drain wait is bounded, because a grandchild can inherit the pipes

A background process inherits the command's stdout and stderr, so EOF may
never arrive even though the main process is long gone. `Config.DrainGrace`
(default 2 seconds) bounds the wait; when it expires the reaper closes the
read ends, which unblocks both copies, and the job becomes terminal with what
was captured. A job must not become unreachable because something it started
is still holding a file descriptor.

## Consequences

Benefits:

- `Run` returns complete output for a command that has exited, which is what
  a caller who waited for the job expects;
- the terminal state and the readable output become the same event, so
  polling with `Get`/`Output` cannot see a finished job with nothing in its
  buffer either;
- the ordering is now the manager's own plumbing rather than a documented
  hazard of `os/exec`, and a test can pin it: a command whose background
  subshell writes after the main process exits is reported with that output.

Costs and limits:

- the drain adds a small delay to the terminal transition for a command whose
  pipes are held open; the wait is bounded by `DrainGrace`, and for a
  well-behaved command the copies are already finished when the reap happens;
- a job that leaves a long-lived background grandchild waits `DrainGrace`
  before it is reported, so `Run` on such a job takes that much longer than
  the process did;
- `DrainGrace` is manager configuration rather than per-spec, because the
  bound is about the manager's bookkeeping, not about one command's contract.

## Alternatives Rejected

### Keep `StdoutPipe` And Read To EOF Before `Wait`

It would fix the lost bytes for the common case, but `Wait` is what reaps the
process and records its exit code, so a command that closes its stdout and
keeps running — or hands it to a background grandchild — would never be
reaped. A job stuck in "running" forever is worse than a truncated buffer.

### Keep `StdoutPipe` And Accept The Race

That is the defect. The failure is invisible on a quiet machine and appears
as an empty result on a loaded one, which is the worst kind of reporting bug:
a caller cannot tell it from a command that printed nothing.

### Give The Command A `bytes.Buffer` Instead Of A Pipe

`cmd.Stdout = &buffer` makes `Wait` wait for the copy, but the job's output
model is an offset-addressable ring buffer with drop accounting, streamed to
a reader while the job runs. A `bytes.Buffer` has none of that, and copying
into the job's buffers afterwards would give up streaming entirely.

### Wait For The Drain Without A Bound

Simple, and wrong the moment a command backgrounds a server: the pipes stay
open, EOF never arrives, and the job never becomes terminal for either
`Run` or a poller.

### Announce Terminal At The Reap And Let `Output` Catch Up

The caller can always poll again. But `Run` is a foreground call whose whole
point is to hand back the result, and `Output` returns an offset a caller has
no way to know is provisional. Making the terminal state mean "output is
complete" is the only reading that lets a caller trust one read.
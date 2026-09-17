# ADR 0043: Background Jobs With Offset-Addressed Output

Status: accepted

## Context

A shell tool that blocks on every command cannot run a dev server, a test
watcher, or any long-lived process, and a whole agent turn stalls behind
one `npm run dev`. The reference solves this with a unified execution
model: one interface starts a command, optionally in the background, feeds
it input, reads its output incrementally, and stops it. Codex exposes the
same idea as `exec_command` plus `write_stdin`; the harness exposes it as
background jobs with `job_output` and `job_kill`.

The interesting part is not spawning a process — `os/exec` does that — but
the output protocol. A background command produces output while nobody is
reading, so the output must be buffered with a bound, and a reader that
polls periodically must never re-read bytes, never silently skip bytes,
and never be told it has seen everything when it has not.

## Decision

### Offsets are absolute and never rewind

`jobs.Buffer` is a bounded buffer that tracks two absolute offsets: the
offset of its oldest retained byte and the total written. `Read(since,
max)` answers from `since`, and when `since` is older than what is
retained it returns what it has with `Dropped` set. A polling tool
therefore passes the `Next` offset it received and gets exactly the new
bytes, or an explicit statement that its view has a hole. The same
information appears on the job snapshot as `StdoutDropped`/`StderrDropped`,
so `job_output` can say "output was dropped" instead of pretending the
buffer is the whole truth.

### A background job is detached from the tool call

`Start` builds the job's context with `context.WithoutCancel`, so a job
outlives the tool call that launched it; the manager's own timeout and an
explicit `Kill` are what stop it. A caller's context still bounds the
startup, and the foreground form (`Run`) honours cancellation by killing
the job and waiting for the reaper, so a cancelled turn never leaves a
process behind.

### The job limit fails loudly

Starting past `MaxJobs` returns an error naming the limit rather than
queueing: silent queueing means the model believes a command is running
when it is not, which is worse than an error it can act on (stop a job).

### Close is idempotent and reaps everything

`Close` refuses further starts and kills every live job. Every accessor
checks for an unknown id *before* touching the record: the first draft of
this package dereferenced a nil record and the panic, raised while the
manager mutex was held, deadlocked the deferred `Close` — the regression
test now covers `Write`/`CloseStdin`/`Output`/`Kill` on unknown ids
directly.

## Consequences

Benefits:

- long-running commands stop blocking a turn, and the agent can poll;
- readers cannot re-read or silently lose output, and dropped bytes are
  reported rather than hidden;
- a bounded buffer means a runaway `while true; do echo; done` cannot
  exhaust memory;
- per-job timeouts and an explicit kill bound runaway processes;
- stdin stays open for interactive programs, and closing it is an explicit
  operation.

Costs and limits:

- jobs are process-local: they are not checkpointed, so a resumed run
  cannot reattach to a job the previous process started (its tool metadata
  says what it started, not what is still alive) — the next batch's tools
  document this rather than implying durability;
- output is captured through pipes, so a command that detects a
  non-terminal stdout may buffer differently (`stdbuf`/`script` are the
  user's tools for that);
- a killed job's children are killed only through the process group the
  shell creates; a grandchild that detaches itself survives, as with any
  shell-level kill;
- the ring buffer is per stream, so interleaving between stdout and stderr
  is not preserved (each stream has its own offset).

## Tool Surface

`tools/jobs` follows the reference's naming: `exec_command` starts work
(foreground by default, bounded by a timeout so a stuck command cannot hang
a turn), `write_stdin` feeds it, `job_output` reads it, `job_list` reports
it, and `job_kill` stops it. Two decisions are worth recording. First,
`job_output` returns the next offsets and an explicit dropped-output flag,
so the model's polling is idempotent and its view of the output is never
silently incomplete; a running job with nothing new also returns a hint
naming `waitMs`, because "no output" and "no output yet" are different
facts and the model should not have to guess which one it is looking at.
Second, only `job_list` is declared read-only: listing changes nothing, so
plan mode allows it, while starting, feeding, and killing processes are
exactly the kind of action plan mode must refuse.

## Alternatives Rejected

### Unbounded Output Collection

Simplest, and it turns a chatty background command into an unbounded memory
leak inside the agent process. The bound plus explicit `Dropped` reporting
keeps the failure visible and bounded.

### Re-read From The Start On Every Poll

Then the tool output grows quadratically with the number of polls and the
model re-reads the same bytes; offsets make polling cheap and idempotent.

### Queue Past The Job Limit

A queue hides the fact that nothing is running, which is exactly the
misunderstanding the tool exists to prevent.

### Kill Jobs When The Tool Call Ends

Then a background job cannot exist at all, and the capability is a
foreground shell with extra steps.

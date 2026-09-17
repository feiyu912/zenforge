# ADR 0035: Run Time Travel — Checkpoint Fork And Append-Only Revert

Status: accepted

## Context

A run's event log is its audit trail and its checkpoint history is its
recovery point. Codex's rollout recorder and DSH's session format both
support going back: a rollout has monotonic ordinals, a writer lock so two
processes cannot interleave appends, a fork that seeds a new rollout from
an earlier one, and a revert that rewinds the conversation. ZenForge had
the first two properties but not the second two, and its ordinal
computation rescanned the whole log on every append.

The hard constraint is this project's checkpoint discipline: the event log
is append-only, `run.started` must remain the first persisted event of a
run, and `RunState` changes are additive. A revert implemented by
truncating the log would violate the first; a fork implemented by copying
a parent's events into a child log under a new run id would either violate
the second (no `run.started` first) or require rewriting every copied
event's run id (corrupting the audit trail).

## Decision

### Ordinals stay contiguous, and appends stop rescanning

The JSONL event store already assigned `latest+1` under an in-process
mutex per root and a cross-process `flock`, and refused any event whose
sequence was not exactly the next ordinal. What changed is the cost: the
store now caches each run's last sequence together with the log file's
size, and validates the cache by `stat` before using it. An appending
writer holds the flock, so no write can be in flight; a *different*
process's append changes the file size, which invalidates the entry and
forces a rescan. A `Read` whose cursor is at or beyond the cached tail
returns immediately. Two tests pin the behavior: one interleaves appends
between two store instances with distinct in-process mutexes (so the flock
must serialize), and one asserts the cache is invalidated by the other
instance's append.

### Time travel reads checkpoints, not the event log

`checkpoint.HistoricalStore` is an optional extension with
`LoadAt(ctx, runID, seq)`, implemented by the memory, JSONL, and SQLite
stores (all three already kept history; the memory store now retains it
too). `checkpoint.LoadAt` uses the extension when present and otherwise
falls back to `Load`, failing when the only checkpoint is newer than the
requested sequence — a store that cannot reconstruct the past must not
silently answer with the present.

### Fork creates a child log that starts at `run.started`

`Agent.Fork(ctx, parentRunID, atSeq)`:

1. loads the parent's newest checkpoint at or below `atSeq`;
2. deep-copies the state through JSON (no aliasing of the stored value),
   sets `RunID` to a fresh id, records `ParentRunID` and
   `zenforge.fork.parent_run_id` / `zenforge.fork.parent_seq`, resets the
   phase to `created`, and clears `Approval` because a pending request id
   names the parent run;
3. persists `run.started` for the child — with `forkedFrom` and
   `forkedFromSeq` — as the child log's first event, so the log invariant
   holds without rewriting anything;
4. saves an initial child checkpoint and continues the run.

The parent log is untouched. The child's history is the parent's *state*
(conversation, todos, tool state, workspace metadata), and the lineage in
its own log is what connects the two. This is the same trade-off codex
makes when a fork opens a new rollout file seeded from the parent's
conversation.

### Revert appends a marker instead of deleting events

`RevertRun(ctx, stores, runID, toSeq)` (and `Agent.Revert`, which
delegates):

1. loads the newest checkpoint at or below `toSeq`;
2. appends a `run.reverted` event carrying `toSeq` and the checkpoint
   sequence;
3. saves the rewound state as the run's **newest** checkpoint, at the
   marker's sequence, with `zenforge.revert.*` meta.

Because resume loads the newest checkpoint, the next resume simply
continues from the rewound state, while the log still contains every event
of the abandoned branch — an audit reader can see exactly what was
discarded and when. No event is ever deleted or rewritten.

`RevertRun` needs only the checkpoint and event stores, so the CLI's
`revert` command works without a model, API key, or agent. `resume
--revert-to <seq>` completes the same rewind and then resumes, and `fork
<run> [--at <seq>]` creates and continues a child.

## Consequences

Benefits:

- appends are O(1) amortized instead of O(n) per event, which matters for
  long runs where every tool result adds an event;
- a run can be branched to explore an alternative approach without losing
  the original, and the branch is auditable through its `parentRunId` and
  `run.started` lineage;
- a bad turn can be undone without destroying evidence, and the abandoned
  branch remains readable in the log;
- all three checkpoint stores expose the same time-travel read, so a host
  can switch stores without losing the capability.

Costs and limits:

- a reverted run's log no longer describes a single linear execution:
  readers must consult `run.reverted` markers to know which suffix is
  abandoned, and the harness keeps no separate "effective log" projection;
- a fork copies state, not events, so the child's log cannot be replayed
  into history without the parent checkpoint (the lineage meta says where
  to look);
- per-marker revert chaining (revert, continue, revert again) works
  because each revert saves a new newest checkpoint, but a revert to a
  sequence *before* a previous revert's checkpoint is bounded by what the
  store still holds — the JSONL and SQLite stores keep all history, so it
  is available;
- the tail cache trusts `stat` size; a process that rewrote the log
  in place with the same size could defeat it, which the append-only
  design and the flock make impossible in practice;
- DSH's session v3 features beyond this — FTS search over sessions,
  projections, and on-disk format migrations — are deliberately not
  ported yet; the parity plan keeps them as future work, and the JSONL
  format remains `zenforge.run_state.v1` / `zenforge.checkpoint.v1`.

## Alternatives Rejected

### Truncate The Event Log On Revert

Truncation makes the log linear again and keeps replay simple, but it
destroys the audit trail, races with any reader that is mid-scan, and
cannot be undone. Appending a marker costs reader complexity and keeps
every fact.

### Copy Parent Events Into The Child Log

Faithful rollout forking would copy events, but each copied event's run id
must be preserved for the log to be a true record of that run, and the
child log's `run.started` would then belong to the parent — breaking the
"first persisted event is this run's `run.started`" invariant. Copying
state and recording lineage is the version that keeps both.

### Fork At An Event Sequence Instead Of A Checkpoint Sequence

Events and checkpoints both carry sequences, but only checkpoints carry
recoverable state; forking "at event 17" would require replaying events
into state, which the harness deliberately avoids (its reducer is the
checkpoint, not the log). Using checkpoint sequences keeps fork exact and
cheap.

### A Single `history` Command With Subcommands

`fork` and `revert` have different failure modes, different required
stores (`fork` needs a model; `revert` does not), and different outputs.
Separate commands keep each flag set honest.

### Put The Tail Cache On Disk

An on-disk index would survive process restarts, but it adds a second
format to keep consistent with the log and a corruption mode to handle.
The in-memory cache with a `stat` check is equivalent in cost for the
common long-running process and cannot diverge from the log.
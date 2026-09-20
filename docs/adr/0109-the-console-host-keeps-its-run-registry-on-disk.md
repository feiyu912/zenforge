# ADR 0109: The Console Host Keeps Its Run Registry on Disk

Status: accepted

Relates to
[ADR 0101](0101-the-console-workspace-registry-is-process-local.md) (the console's *workspace*
registry, which stays process-local -- this ADR is about the *run* registry the session list
itself reads), [ADR 0104](0104-a-session-exists-before-its-first-turn.md) (the draft half of the
same list, which stays process-local), and
[ADR 0108](0108-a-sessions-log-is-one-sequence-across-its-turns.md) (the transcript a
listed session opens).

## Context

`session/list` is what fills the console's sidebar. It answers from
`RunManager.List`, and `zenforge serve` built its run manager **without a registry**:

```go
Manager: harnesshttp.RunManagerOptions{
    MaxActive:         16,
    RunTimeout:        config.runTimeout,
    TerminalRetention: 10 * time.Minute,
    OwnerID:           "zenforge-serve",
},
```

A manager with no registry lists `m.runs`, its own in-process records, and
`finishLocked` deletes a terminal record after `TerminalRetention`. Measured on the
operator's host at `127.0.0.1:8787`, whose durable store still held every transcript:

```
$ ls /tmp/.zenforge/runs | wc -l
45

$ session/list          # 22:51, ten minutes after the turns before them finished
listed: 1
   run_1789915659750670000 who are you | running False   # created 22:46
# the conversations created at 22:41 are gone from the list, not from the store

$ session/list          # 22:53, two fresh turns sent through session/create+prompt
listed: 3
   run_1789915960885194000 hello 2 | running False
   run_1789915952818326000 hello 1 | running False
   run_1789915659750670000 who are you | running False
```

The same host's log holds exactly one `zenforge serve listening` line, so the missing
rows were not a restart: they expired at the retention while their logs stayed on disk.
The earlier restart in this window emptied the list outright, which is what prompted the
check.

So the operator's sidebar forgot a conversation ten minutes after its last turn, and a
restart emptied it completely — the console could serve the transcript of a session it
had just stopped listing, and the browser had no row left to click. The harness already
has the mechanism for this: `RunManagerOptions.Registry` with
`harnesshttp.OpenSQLiteRunRegistry`, documented as the way "durable status/list
snapshots" survive a process. The console host simply did not configure one.

A durable registry brings its own question. A record is written when a run is claimed,
before its first event, and a process that dies mid-run leaves an **active** status
behind. Two console answers read that status directly:

- `sessionList` reported `running` from it, so a killed host would show a conversation as
  running forever;
- `session/prompt` steers an active run, so the next prompt would try to steer a run owned
  by a process that no longer exists and fail with `not owned`, instead of continuing the
  conversation.

## Decision

`zenforge serve` opens a SQLite run registry in its state directory and hands it to the run
manager:

- the registry lives at `<checkpoint-dir>/run-registry.sqlite` for the JSONL store, and
  beside the store file when `--checkpoint-type sqlite` names a file rather than a
  directory; the directory is created when it is missing, so a fresh install works;
- it is opened and seeded before the listener, and closed with the command's other
  closers.

The registry also **adopts the runs the state directory already holds**: the store names
its runs (a new optional `eventlog.RunLister` capability, implemented by the memory, JSONL
and SQLite stores, with the checkpoint summaries as the fallback for a store that cannot
enumerate), each run's own log decides its terminal status, and each adopted run is claimed
and released so the record carries the same shape as one the host wrote itself. Without
this, the operator's existing conversations -- every transcript still on disk -- would stay
invisible, because a fresh registry lists nothing until the next turn.

Two rules make the durable records honest:

1. **An expired lease is not a running run.** `RunInfo.Live(now)` is the single answer:
   the status must be one of `starting`, `running`, `waiting_approval`, and a recorded
   lease must not have passed. A manager with no registry records no lease and is judged by
   status alone. Both console paths use it — the list's `running` flag and the prompt's
   steer-or-continue decision — so a conversation interrupted by a crash is listed as
   finished and the next prompt starts its next turn. The MCP run-status tool still reports
   the registry record as it stands: that is the deep API's raw view, and the console is
   where liveness is interpreted.
2. **A record with no transcript is not a listed session.** A start that failed before its
   first event leaves a terminal record with no durable log. `session/page` answers
   not-found for it, so listing it would offer a conversation that cannot be opened. A
   live run is kept even with no events yet: it is between its claim and its first event
   only for a moment, and the console can steer it.
3. **A run another process is executing is left alone.** Adoption claims before it writes,
   so a host that finds a run with an unexpired lease owned elsewhere does not adopt it and
   does not disturb its owner's claim.

`session/list` therefore merges the registry — every run this install served, live or
terminal — with the handler's pending drafts, and reports a session from its newest turn
exactly as before. Drafts stay process-local (ADR 0104); a session's model choice still
lives in the settings document (ADR 0103).

## Consequences

- The sidebar survives a restart and no longer expires conversations at the terminal
  retention, because the listing source is durable. The transcripts were already durable;
  now the rows that point at them are too.
- A conversation whose host was killed is listed as finished and can be continued: its
  newest turn's lease is expired, so the prompt starts `sessionID~k+1` with the history so
  far instead of trying to steer a dead run.
- `zenforge list` and the JSONL store are unaffected: the store's run listing skips
  non-directory entries, so the registry file sitting in the store directory is ignored.
- The registry is per state directory, so a host serving one workspace lists that
  workspace's sessions; two hosts pointed at one state directory share claims, which is
  what the lease already means, and a run started from the same directory by anything else
  that shares the store is listed too -- the store is the host's, so its runs are its
  conversations.
- The first start after this change seeds the registry from the store, so conversations
  served before it appear in the sidebar again. Their rows carry the status their own log
  ended with; a run whose log has no terminal event is recorded as cancelled, which is what
  an interrupted run is.
- A record whose run never wrote an event is dropped from the list, so a failed start does
  not leave a permanent unopenable row.
- The registry is one more file in the state directory; a machine that loses the state
  directory loses the list while the transcripts go with it.
# ADR 0118: A Reconnect Mid-Answer Resumes the Attempt

Status: accepted

## Context

The console renders live prose from the dense `assistant-stream` frames and nothing
else: a delta's durable record is not even served (ADR 0117). So a console that
reconnects — a page reload, a dropped socket, a second tab — has no way to know
that an answer was already half-written unless the host tells it. Upstream tells
it, in the opening snapshot's baseline:

```
assistantStream: { revision, activeAttempt?: { attemptId, startedAfterSeq, turn, step, nextIndex, stream } }
```

`this host answered `{revision: 0}` and nothing else, and the client's reconnect
path had nothing to restore. Measured in the browser against a real model, on a
conversation mid-answer: after a reload the follow snapshot carried
`activeAttempt: null`, the next delta opened a *new* attempt with a `start` frame,
and only the tokens after the reload rendered — the operator watched the partial
answer restart from the middle instead of continuing.

## Decision

**The snapshot hands over an attempt that is still streaming, and the live tail
continues it.**

- The tracker is not built empty. The follow stream replays the newest turn's
  durable log through the same projection and the same tracker the live tail would
  have used (`replayAssistant`), so the state a reconnect restores is exactly the
  state the previous generation reached: the same attempt id, the same frame
  counter, the same accumulated blocks, and `startedAfterSeq` stamped with the
  sequence of the last record before the attempt — derived, never invented.
- The baseline's `stream` is the console's own compaction of the frames sent so
  far: a run of deltas of one block becomes a `text-chunks`/`reasoning-chunks`
  record, and any other frame (a block start or end) is stored verbatim as a
  `chunk` record. The client expands both forms with the same reader, so its
  expansion yields exactly `nextIndex` frames — which is where the client stops
  reading.
- A turn whose answer already settled replays to a tracker with no open attempt, so
  the baseline omits `activeAttempt` — upstream's shape for a settled
  conversation.
- `startTurn` is inert when the tracker is already on the turn it is asked for.
  Without that, the follow path's own "move to this turn" call would discard the
  attempt the replay just restored, and the live tail would announce a second
  `start` for an attempt the client already has open — a rebaseline, which is worse
  than the gap it was meant to fix.

## Consequences

- Measured against a real model: reloading a page mid-answer produced a snapshot
  whose baseline carried `nextIndex: 10` and a two-record compact prefix, **no
  `start` frame followed**, and the partial answer was on screen immediately after
  the reload. Before this ADR the same measurement showed `activeAttempt: null`
  followed by a new `start`.
- Pinned by tests: a replay of a streaming turn hands over an attempt whose
  baseline expands to exactly its `nextIndex` frames (which is what caught a
  block-end being counted twice — once by the block, once by the chunk), a settled
  turn hands over none, and a socket-level test asserts that the delta after the
  snapshot is a continuing `chunk` frame and not a new `start`.
- The replay is one projector pass over the newest turn's log per connection. That
  is the same work the snapshot's window already does, and a turn that already
  settled is the common case, where the replay's only product is "no open attempt".
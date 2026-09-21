# ADR 0117: The Session Log Carries the Console's Vocabulary

Status: accepted

## Context

Two questions in one conversation were enough to make the console offer
`Load earlier`, and the trajectory view's chunk list was empty. Measuring what the
host actually served explained both. One prompt, one tool call, one answer — the
`session/follow` snapshot's 50-record window held:

```
checkpoint.created     22
model.delta            16
session.title           2
user/message            1
step/start              1
model.started           1
model.usage             1
assistant/message       1
turn/end                1
```

Forty of those forty-six records were this host's own bookkeeping: the durable
`checkpoint.created` event, the model lifecycle events, and one record per
streamed token. Upstream's session log contains none of them — its durable
vocabulary *is* the console's (`turn/start`, `step/start`, `assistant/message`,
`tool/call`, `tool/result`, …), and the tokens travel only on the dense
`assistant-stream` channel. This host's log is deeper than the console's, and it
was serving all of it: each token took a window slot, a sequence number, and a
paging round trip, and the answer's byte-exact timeline was nowhere, because the
only place upstream keeps it is inside the settled message's compact `stream`.

The delta records were also a leftover: they existed because the console cannot
read a delta, so the projection forwarded them marked `ignorable`. But `ignorable`
is not free. The console's journal stream counts every delivered record toward
`maxMessages`, so an answer that was one console message consumed sixteen of the
fifty records the operator can scroll through.

## Decision

**The served session log is the console's log, not the host's.** `dshwire` projects
a durable event into a record only when the console knows its type
(`KnownEventTypes`), and numbers the records it serves:

- An event outside the console's vocabulary — this host's `checkpoint.created`,
  the model lifecycle and delta events, and anything a future adapter adds that the
  console cannot read — produces **no record**. It still advances the projector's
  state (the step's accumulated content, the call-to-step map), so the events that
  do produce records are unaffected.
- The wire never carries `ignorable`, so the field is gone: a record this host
  serves is one the console must read.
- A console event this host has no mapping for still passes through under its own
  name and payload, so nothing the console can read is lost. The only names served
  that way are the console's own.
- **The served sequence counts records, not durable events.** A turn's records are
  numbered `1..n` and the next turn's offset is the number of records before it, so
  the session sequence stays what the console requires of it — one contiguous
  number line that a resumed stream cites at or ahead of what the client applied
  (ADR 0108) — while no number is spent on a record the console would skip.
  `SessionLog.NewestTail` still reports the *durable* tail, which is the coordinate
  the run manager's follower speaks.

**The settled message carries the step's timeline.** The deltas no longer appear in
the window, so the projector keeps each block's chunks with their times and emits
them as the console's compact stream on the assistant message
(`{type:"text-chunks", time0, index, dt, texts}` per block, and the same for
reasoning). That is upstream's own compaction, and it is where the console's
trajectory view reads the byte-exact answer from — the field it had been given as
`[]`.

**The dense frames mirror a provider's.** An attempt's blocks are now streamed the
way a provider streams them: `block-start` opens the block, its deltas follow with
that block's index, and the block closes with `block-end` carrying its final
content — including the block an attempt ends inside, which closes just before the
end frame releases the settlement. A chunk's index is the block it belongs to, so
reasoning that resumes after an answer opens a third block instead of appending
itself to the text.

## Consequences

- The same turn now serves **ten** records instead of forty-six: the prompt, each
  step's start and end, the two settled messages, the call, its result, and the
  turn's end. A conversation of several turns fits in the console's window again,
  which is what `Load earlier` was being used for.
- The answer still streams, and the console can now replay it byte-exactly from the
  record it settles into. Verified against a real model: one answer streams as
  `{start, block-start, chunk×N, block-end, end}`, and the settled
  `assistant/message` in the window carries the same text as `text-chunks`.
- Pinned by tests: the window is exactly the console's events, the served sequence
  is the record ordinal across turns, no zenforge event name is one the console
  knows, and the settled message's compact stream reproduces the deltas with their
  timing.
- Because this changes the numbering a console page already holds, a page that
  stays open across the host upgrade can be told its stream resumed behind what it
  applied (ADR 0108's guard) and reloads. A refresh is enough; the host does not
  keep the old numbering alive.
- Still not upstream-exact, and documented: the dense stream has no
  `tool-call-delta` (this harness emits complete `tool.call` events, so tool
  arguments arrive with the call record), no `usage` chunk (the accounting rides
  the settled message), and a reconnect in the middle of an answer does not
  re-baseline the open attempt (`assistantStream.activeAttempt`).
# ADR 0116: The Answer Streams as Assistant-Stream Frames

Status: accepted

## Context

The operator watched an answer appear whole and asked why it was not streaming:

> 为什么不是流式输出，太奇怪了吧

The model's output was already arriving token by token and was already durable: the
harness writes `model.delta` for every chunk it receives. What the console rendered
was nothing until the step settled, for two reasons that met in the middle:

- The delta's console record is an **ignorable** record. The projection keeps
  `model.delta` as a pass-through event (`ignorable: true`) because the console has
  no meaning for it, and the client's surface skips exactly those records.
- The channel the console renders live text from is a *different* item on the same
  connection: `{type:"assistant-stream", frame}` carrying `start`, `chunk`, and
  `end` frames, which this host never sent. The console asks for them
  (`assistantStream: true` is hard-coded in the client's follow request) and the
  opening snapshot has to answer with a baseline; the frames themselves were the
  missing half.

So the transcript's answer arrived with its settlement, and the only reason it had
looked live at all earlier in this project is that the console was reconnecting
constantly (ADR 0114) and re-reading the log.

## Decision

The follow stream mints the console's dense assistant-stream frames from the same
durable events it already forwards, in `internal/dshstream/assistant.go`:

- **start** opens an attempt when its first delta arrives, naming the attempt id and
  step from the model events, the conversation's turn, and `startedAfterSeq` — the
  sequence the console already holds — so the settlement it eventually releases is
  never mistaken for one already applied.
- **chunk** carries one delta: `text-delta` from `model.delta`, `reasoning-delta`
  from `model.reasoning`, each in its own content block, with the chunk index
  contiguous from zero within the attempt.
- **end** closes the attempt. A step's settlement (the record `model.done` projects
  to) is sent first and then released by `{kind:"committed", eventType, seq}`,
  because the client stages a settlement while its attempt is open and publishes it
  only when the end frame names it. A replaced attempt (a retried step) and an
  attempt left open when a turn ends are closed as `{kind:"abandoned"}`, so partial
  text never renders under a settlement that will not come.
- **`revision` is a per-frame counter, not a per-attempt one.** The client's session
  wire requires every frame to cite exactly one more than the last and treats a
  mismatch as a **carrier failure** — it tears the stream down and reopens it, which
  is a restart loop rather than an error message. The counter starts at the
  snapshot's baseline revision, 0.
- Every field is derived from the durable events and the sequence this stream
  already served. Nothing is buffered twice and nothing is invented: a log that
  cannot be projected cannot be streamed either.
- A client that does not opt into the assistant stream gets no frames and no
  baseline; the capability is one thing.

## Consequences

- The answer renders as it arrives. Measured in a browser against a real model, one
  answer produced `{start: 1, chunk: 16, end: 1}`, revisions `1…18` in order,
  ascending chunk indexes, and one committed end naming the settlement's sequence;
  the page's rendered text grew in six visible steps instead of jumping once at the
  end. Before this, the same measurement showed the settlement published with no
  live text at all.
- The wrong `revision` semantics were found the hard way (a 100 ms restart loop with
  a new generation per chunk) and are pinned by a test that asserts the frame
  numbers are consecutive. It is the kind of trap a future window should not have to
  re-discover.
- Honest limits, all consequences of what the durable log carries: a reconnect
  **mid-answer** does not yet re-baseline the open attempt, so the partial text
  appears when its settlement arrives (the snapshot answers with `revision: 0` and
  no `activeAttempt`); tool-call arguments do not stream, because this harness
  produces a complete `tool.call` rather than deltas for them; and `usage`/`finish`
  chunks are not sent. The delta records stay in the session window too, so a long
  answer still consumes window slots that `Load earlier` then has to refill
  (ADR 0114).
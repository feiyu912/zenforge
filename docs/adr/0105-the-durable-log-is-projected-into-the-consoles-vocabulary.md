# ADR 0105: The Durable Log Is Projected Into The Console's Session Vocabulary

Status: accepted

Amends [ADR 0104](0104-a-session-exists-before-its-first-turn.md): that ADR made a
created session's history *loadable*, and this one makes it *readable*. It supersedes
the wire-shape claim in `docs/dsh-console-protocol-recon.md` ("every durable event
appended with `surfaceOp:"append"`"), which this console refuses.

## Context

The host that ADR 0104 had just restarted still could not hold a conversation. The
operator sent `hello` and the page answered:

```
Failed to load history: session event "run.started" is not surface-eligible and cannot carry surfaceOp (gateway/internal)
```

That message is the console's own, thrown while it reads a history page. Two facts
about the wire were wrong, and the second one had been wrong since the follow stream
was written.

### `surfaceOp` is only legal on four event types

`internal/dshstream` and `internal/dshapi` wrote `"surfaceOp":"append"` onto **every**
record, on the theory (recorded in the protocol recon) that "every durable event is an
append on the surface" was the simplest legal encoding. The console disagrees, and it
says so in code it runs on every record:

```js
if (!isSurfaceEligibleType(event.type)) {
  if (!KNOWN_SESSION_EVENT_TYPES.has(event.type) && event.ignorable === true) return;
  if (raw.surfaceOp !== void 0) throw new Error(`session event "${event.type}" is not surface-eligible and cannot carry surfaceOp`);
  ...
}
```

`SURFACE_EVENT_TYPES` is exactly `system/message`, `user/message`, `assistant/message`,
`tool/result` — the four message-producing types of the model-visible surface. Every
other type must omit the field. `run.started` is not one of the four, so the first
non-empty history the console ever read ended in that throw.

The recon note was wrong in a second way: an unknown event name is *tolerated* only
when it carries the envelope's `ignorable: true` marker. The console's own comment
explains the rule's purpose — "the persistence read path refuses to interpret a log
containing a type outside this set unless the event carries the envelope's `ignorable`
marker: such a log was likely written by a newer harness, and silently skipping a
required event would reconstruct a wrong session" — and zenforge's names
(`run.started`, `model.delta`, …) are all outside that set.

### The console's transcript is built from its own vocabulary, not from ours

Fixing legality alone would have loaded an empty conversation. The transcript is
derived from surface events: a chat row is a `user/message` or an `assistant/message`,
a tool card is a `tool/result` paired with its `tool/call`, and the step/turn brackets
(`step/start`, `step/end`, `turn/end`) carry the boundaries the view groups by.
zenforge logs its own events (`model.delta`, `model.done`, `tool.call`, …), so a log
served in its own names renders nothing at all. The recon note said as much in its risk
paragraph — "mapped names (`user/message`, `assistant/message`, `tool/call`,
`tool/result`) are what actually produce a transcript" — and then chose the encoding
that produces none.

## Decision

### `internal/dshwire` is the one projection

Both read paths (`session/page` in `dshapi`, `session/follow` in `dshstream`) serve the
same projected records, produced by one package. The two packages' duplicated
`wireEvent`/`newWireEvent` functions are gone: an event shape that two packages write
separately is an event shape that drifts.

### One durable event in, one wire event out, with the durable `seq`

The projection is a mapping, not a filter and not a renumbering:

- every durable event produces exactly one wire event, so sequence numbers stay
  contiguous, which the console's session format documents as an invariant;
- the wire `seq` **is** the durable `seq`, so the console's cursor, its `throughSeq`
  and `beforeSeq` paging, and the live tail's `afterSeq` all speak the log's own
  sequence with no second coordinate to keep consistent;
- an event with no console meaning keeps its own name and payload and is marked
  `ignorable: true`, so the wire still carries the whole log and the console skips it
  deliberately.

### The mapping

| zenforge | console | surface |
| --- | --- | --- |
| `run.started` (`input`) | `user/message` | append |
| `request.steer` (`input`) | `user/message` | append |
| `step.started` / `step.done` | `step/start` / `step/end` | — |
| `model.delta` / `model.reasoning` | accumulated into the step's content | — |
| `model.usage` | the step's `usage` | — |
| `model.done` | `assistant/message` (or `assistant/attempt` when nothing was produced) | append |
| `tool.call` | `tool/call` | — |
| `tool.result` / `tool.error` | `tool/result` | append |
| `run.done` / `run.error` / `run.cancelled` | `turn/end` (`completed` / `error` / `aborted`) | — |
| anything else | its own name and payload, `ignorable: true` | — |

A step's assistant message is the settlement of the deltas that preceded it, so the
projector is stateful: it accumulates the step's content blocks in arrival order
(reasoning and text each merge into the previous block of their kind) and emits them at
`model.done`. Both read paths project the **whole** log and then select the window they
serve, because a window that opens mid-step still needs the step's accumulated text; the
live tail continues the same projection rather than starting a new one.

A run is one console turn (`turn: 1`): this host maps one console session to one
zenforge run (ADR 0101), and a queued prompt is a steer inside the running turn.

### Provenance is the host's own answer

An assistant message carries the provider and model it came from. The projection stamps
the session's own chosen model where the selection store can name it
(`SessionModelIdentity`, an optional half of the store), and otherwise the host's
configured route and model through a `ModelDefault` seam. Both read paths resolve it
the same way, so the live transcript and a reloaded one read identically. An empty
identity is legal and leaves the label unset rather than inventing a route.

### The console's vocabulary is pinned against the console

`internal/dshwire/vocabulary.go` copies the console's `KNOWN_SESSION_EVENT_TYPES`, and
`boundary_test.go` re-extracts that list *and* the surface set from the vendored client
bundle and fails when either drifts. It also re-implements the client's envelope rules
over a projected turn: allowed fields only, `ignorable` absent or literally `true`,
`surfaceOp` present exactly on the four eligible types, and no zenforge event name
colliding with a console type. The mapping's correctness is therefore checked against
the bytes the browser runs, not against this document.

## Consequences

- A conversation is visible. Verified live on a scratch host carrying a copy of the
  operator's document (throwaway `ZENFORGE_CONFIG_DIR`, its own port) against the real
  provider: the page served `user/message` at seq 1 with `surfaceOp: "append"`, the
  step's `assistant/message` with the model's own text ("Hi there, friend!"), its
  provenance (`openai` / `qwen-plus`) and token usage, a `tool/call` and its
  `tool/result`, a `turn/end`, and every other record `ignorable: true`. No record
  carried `surfaceOp` outside the four eligible types.
- `assistant/message` carries an empty `stream` array. The console's `stream` is its own
  compact timed raw stream; this host has already settled the text into content blocks,
  and the trajectory view's byte-exact chunk list is a separate gap
  (`docs/limitations.md`).
- Live token streaming is not sent: the durable `model.delta` records arrive as
  ignorable events, and the answer appears when the step settles. The console's
  `assistant-stream` frames (a revision-counted, cursorless channel) are the follow-up
  for token-by-token rendering.
- A tool result states failure only through its `tool-result` block's `isError`, with no
  structured `error` envelope: the host knows a tool's exit state, not the console's
  failure identity (name and code). The console's own validator allows the absence.
- The conversation is one turn per session, because one console session is one run.
  Queued prompts inside a run are steers; a second turn needs the multi-turn folding
  ADR 0086 already records as missing.

## Alternatives Rejected

### Keep passing the raw names through with no surfaceOp

It is legal and it loads, but it renders nothing, which is the state the operator was in
before this ADR. The console's transcript is not a generic event viewer.

### Map names without assembling the assistant message

`model.delta` is not a console event, and `model.done` carries only a step number and a
tool-call count, so the answer text has to be assembled from the deltas. Emitting the
deltas as individual `assistant/message` events would put one message per token on the
model-visible surface and change what the console believes the model said.

### Renumber the wire events densely

A dense projection of only the mapped events would make `seq` a second coordinate that
the page's `throughSeq`, the follow's `afterSeq` and every reconnect would have to
translate, and would break the contiguity the console's log format documents. The
durable sequence already has every property the wire needs.

### Write the console's vocabulary into the durable log

The zenforge log is the product's own format, read by the CLI, the headless protocol and
the zenmind adapter. The console is one more reader of it (ADR 0099's layer rule), so the
translation belongs on the console's side of the boundary.

### Drop unmappable events instead of marking them ignorable

The console would then see a log with holes and no way to tell a deliberate omission
from a lost event. The `ignorable` marker is the console's own mechanism for exactly
this, and it keeps the served log lossless.

## Verification

`go test ./internal/dshwire/` — the projection's unit tests (one record per event with
the durable seq, the turn rendered as a transcript, surface/ignorable legality, attempts,
turn-end reasons, live continuation, windows) and the boundary tests that re-read the
vendored client bundle.

`go test ./internal/dshapi/ ./internal/dshstream/ ./internal/dshmount/` — the read paths
serve projected records, the paging assertions now name the console's types, and the
follow stream's live frames carry no `surfaceOp` on a step boundary.

`go test ./cli/` — the serve wiring compiles the seams (`ModelDefault`, the selection
store's `SessionModelIdentity`) into the mount and the stream.
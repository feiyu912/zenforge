# ADR 0108: A Session's Log Is One Sequence Across Its Turns

Status: accepted

Corrected by [ADR 0110](0110-a-projected-message-is-identified-by-the-session-sequence.md):
the message identity is the session sequence's, and the first implementation left it on the
run's own number.

Closes the gap ADR 0086 recorded ("This host does not yet merge a session's turns into one
paged log") and relates to
[ADR 0105](0105-the-durable-log-is-projected-into-the-consoles-vocabulary.md) (the record
vocabulary this sequence carries) and
[ADR 0106](0106-a-session-title-names-the-operators-task.md).

## Context

The operator sent `hello` in the console, read the reply, and typed a second message,
`who are you`. The history pane answered instead:

```
Failed to load history: session event stream resumed at a cursor behind the last
applied entry (gateway/internal)
```

Their transcript showed `Load earlier`, `I'll greet the user and confirm readiness to
assist.`, `Tool call todo_update · greeting`, the greeting itself, `Could not load
feedback`, then `hello` and `who are you`. The first prompt worked; the **second** one broke
history loading.

A console session is a conversation, and each prompt after the first is a separate zenforge
run: turn one is the session id, turn *k* is
`dshsession.ContinuationRunID(sessionID, k)` (`sessionID~k`). Every run numbers its own
durable log from one — that is what the run manager and the durable store do — and both read
paths served **only the newest run's log**:

- `session/page` read `h.currentRun(ctx, sessionID)` and windowed its events;
- `session/follow` did the same, so a snapshot opened after a second prompt cited that
  run's cursors.

The shipped console keeps **one** cursor for the whole conversation and enforces it in three
places (`api/gateway/src/client/journal-stream.ts`):

| Rule | Requirement | Broken by a per-run log |
| --- | --- | --- |
| `follows(left, right)` | every live entry is exactly one past the last applied one | turn 2 streams `1, 2, 3…` after the conversation reached `42` |
| `assertPageThrough` | a snapshot window's last record ends exactly at `frame.cursor` | satisfied within a turn, but the cursor itself moves backwards |
| `opening(item, resumed)` | a resumed generation's cursor is at or ahead of the last applied entry | turn 2's snapshot cites `5` after `42`, which throws the error above |

A second consequence was quieter: the earlier turn became unreachable. `Load earlier` paged
within the newest run, so scrolling back through a conversation could never show the
previous prompt's transcript.

## Decision

A session's served log is the concatenation of its turns in one session-wide sequence.

`internal/dshwire/session.go` builds it. A `Source` — implemented by both read paths over
the same run manager and event store — reports a session's runs in turn order and reads each
one's durable events. For turn *k*, the projector is stamped with `Turn: k` and
`SeqOffset: <number of durable events in turns 1..k-1>`, so turn *k*'s wire sequence is its
durable sequence shifted past every event before it. `SessionLog` then exposes:

- `Records` — every turn's projection in session sequence, strictly increasing and
  contiguous across the turn boundary;
- `Cursor()` — the newest sequence, which the snapshot cites and the console's cursor
  becomes;
- `Window(maxMessages)` — the newest records, which may span turns;
- `Through(cursor, beforeSeq, hasBefore, maxMessages)` — the page `session/page` answers
  with, including the console's exclusive upper bound and its `hasMore`;
- `Newest`, `NewestRun`, `NewestTail`, `NewestTurn` — the newest turn's projection and its
  **own** durable tail, which is where the live follower attaches.

Two coordinates therefore exist and are not confused: the session sequence, which the
console cursors on, and each run's durable sequence, which the run manager's follower
speaks. `session/follow` snapshots the session sequence and attaches at the newest turn's
durable tail; each live event is projected with that turn's offset, so it lands exactly one
past the snapshot cursor.

The shift is **derived, never stored**. A turn's event count is fixed once the turn has
ended, and a continuation run only starts after the previous turn reached a terminal event,
so the offset for every turn is stable and any process rebuilds the same numbers. Nothing
new is written to disk, and a session's log stays exactly as durable as its runs.

Turn numbering follows the same rule: the console groups a transcript by `turn`, so turn *k*
carries `turn: k` rather than every turn claiming turn 1.

**Corrected by ADR 0110:** a message id must be derived from the session sequence too. The
first implementation said `msg-<seq>` but passed the run's own seq into `messageID`, so every
turn's first prompt was `msg-1`; the console matches a `user/message` node on that id, and a
second question rendered as the first one. Identities now come from the projected sequence
(`msg-1`, `msg-<offset+1>`, ...).

## Consequences

- A second (and third, and *k*th) prompt in one console session loads history instead of
  failing, and the resume rule holds: a reopened stream cites a cursor ahead of the one the
  client already applied.
- `Load earlier` reaches the conversation's earlier turns; the page and the snapshot serve
  the same sequence, so a page always meets the window it extends.
- A live stream still tails exactly one run — the newest turn. That is the shipped
  console's design: a prompt restarts the stream generation, so a turn that begins while a
  stream is open is picked up by the restart, not pushed into the old generation.
- `dshwire.Session` projects each turn with a fresh projector, which is required rather than
  incidental: the projector accumulates a step's streamed content, so a projector reused
  across turns would settle turn 2's message from turn 1's deltas.
- The projected record vocabulary, surface eligibility, and the `ignorable` rule are
  unchanged (ADR 0105). So is everything the console still does not get: no
  `assistant-stream` frames, no `turn/start`, and `messageFeedback/*` remains unserved.
- The one visible consequence of an unmapped event is now consistent: a passthrough record
  carries the session sequence like every other record, so an uninterpretable checkpoint
  cannot stall the console's `follows` check.
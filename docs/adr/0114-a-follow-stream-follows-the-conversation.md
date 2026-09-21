# ADR 0114: A Follow Stream Follows the Conversation, Not One Turn

Status: accepted

Corrects the lifetime [ADR 0108](0108-a-sessions-log-is-one-sequence-across-its-turns.md)
gave the stream it made one sequence for, and closes what
[ADR 0112](0112-the-coverage-ledgers-gaps-are-a-dated-audit.md) could not see in prose.

## Context

The operator asked a second question in one conversation and reported:

> 怎么会问了第二个问题就会出现 load earlier，然后还惦记不了

"Load earlier" appeared after the second question and clicking it did nothing. Driving the
served console in a headless Chrome over the DevTools protocol reproduced both halves:

```
# after opening the conversation: "Load earlier" is rendered
$ click "Load earlier"
POST /api/session/page {"address":{...},"throughSeq":56,"beforeSeq":7,"maxMessages":50}
200 {"hasMore":false,"records":[seq 1..6: the first turn's prompt, titles, step/start, checkpoints]}
# and yet: the transcript did not change and the button was still there
```

At the moment the page was applied, the controller's own state was right and the store the
component reads was not:

```
prependWindow  entries: 6  hasMoreArg: false  field.hasMore: false  snapshot.hasMore: true
after calling notifier.markDirty():           snapshot.hasMore: false  buttons rendered: 0
```

The component was not failing to update: the window it had was being **replaced** underneath it.
Counting the frames the host sent on the session's WebSocket stream while the page sat idle:

```
WS-IN {"type":"item","streamId":"<uuid>","value":{"type":"snapshot",...}}   \
WS-IN {"type":"end","streamId":"<uuid>"}                                    /  a fresh pair,
... every few milliseconds, each with a new streamId, indefinitely
```

One snapshot and one end, over and over. The reason is this server code:

```go
case event, ok := <-live:
    if !ok {
        return nil            // the followed run's log ended -> the mux sends end
    }
```

`manager.Attach`'s channels close when the run's log ends, and `runFollow` treated that as the
stream's end. The console's client does not:

```js
ended: (accepted) => accepted
    ? new RemoteStreamCarrierError(`${options.name} ended without a terminal result`)
    : protocolViolation$1(...)
```

A stream that ends after its opening snapshot is a **carrier failure**, so the reconnecting
transport opens a new generation immediately, whose snapshot re-installs the newest window
(`installWindow(..., hasMore: true)`) -- discarding every page "load earlier" had fetched. The
button renders from `hasMore`, so it is drawn again, and a click is answered by a page that the
next reconnect throws away. Two turns are needed to notice because one short turn fits in the
opening window: the second question is what makes the window stop covering the conversation.

The same loop ran for every finished or cancelled conversation the console had open, not only
for the one being paged: hundreds of snapshot/end pairs per second, each rebuilding the window.

## Decision

A `session/follow` stream follows **the conversation**, and stays open until the client goes
away:

- A turn's log ending is not the stream's end. When the followed run's log reaches its end, the
  stream waits for the conversation's next turn and continues the same sequence from that turn's
  first event, rather than returning and letting the mux send `end`.
- The next turn's projection is stamped with `dshwire.SessionLog.NewestIdentity` -- the turn
  number and sequence offset `dshwire.Session` derived from the durable shape -- so the offset
  that carries one run's numbering onto the session's is never reinvented at the attach point
  (ADR 0108).
- Waiting for a turn is a poll (250 ms), for the same reason the draft stream polls for its first
  run (ADR 0104): the run registry has no "a turn was created" notification, and the follower a
  finished turn leaves behind speaks only that run.
- Frames the console requires keep their shapes: the snapshot is unchanged, live events are the
  next session sequence numbers, and the stream still reports a failed open as its terminal
  `error` frame.

## Consequences

- The reconnect loop is gone. Measured on the operator's host after the fix, with the same
  conversation open: **zero** WebSocket frames in six seconds of idling, where the same
  measurement before the fix produced a fresh snapshot and end every few milliseconds.
- `Load earlier` works. The same click now sends its page and the transcript begins with the
  first turn's `hello`, while the button disappears -- measured in the browser, on the same
  conversation that reproduced the failure.
- A conversation's next question arrives over the connection that is already open, so the
  console does not depend on a reconnect to see a second turn. Verified end to end: a new chat,
  "hello there" answered, "and again" answered, both prompts rendered exactly once, and zero
  frames while idle afterwards (which is also the ADR 0111 echo fix seen from the browser).
- The host now holds one open stream per conversation the console is showing, waking four times
  a second only while it waits for a next turn; the churn the loop caused -- a full snapshot
  serialization per reconnect -- is gone with it.
- An end frame is still what a stream that must end sends (a refused open is an `error`, and the
  client's own disposal closes the socket), so nothing about the mux's framing changed.
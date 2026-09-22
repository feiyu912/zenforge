# ADR 0130: The Pending Queue Is Projected as the Console's Own Inbox Cell

Status: accepted

## Context

Three console surfaces are built from one projection. `ui-conversation`'s
queue dock lists the messages waiting for the turn to end and gives each row its
own actions (`useProjection("inbox")["next-turn"]`, with `steerQueue` sending the
edit, the removal or the steer); `ui-chat` renders `inbox["next-step"]` as the
strip of input already promised to the running turn; and the submission observer
retires a local echo as soon as the message it submitted appears in **either**
list under its own `source.rpcId`. A session whose `inbox` cell is missing or
stale therefore shows the operator's own words in the wrong place, and the rows
the dock draws cannot be edited at all.

The mutation behind those rows is one method:

```
session/updateQueue  {sessionId, itemId, action}
action = {kind: "edit", content: ContentBlock[]} | {kind: "remove"} | {kind: "steer"}
→ {accepted: true}
```

The reference host (`@deepseek-ai/dsh-api-session-controller`, `session/updateQueue`)
answers with the client's own codes and sentences, word for word:

- an edit carrying anything but text → `session/attachment-invalid` "queue edits
  accept text content only" with `{reason: "QUEUE_EDIT_NON_TEXT"}`;
- an edit whose text is only whitespace → `gateway/bad-request` "queue edit
  content must include non-whitespace text";
- an `itemId` the queue no longer holds (a delivered or dropped message, or a
  session the host does not have) → `session/queue-item-not-found` "queued item
  is no longer pending" with `{itemId}`;
- `steer` on something that is not a queued turn, or a turn that no longer accepts
  steering → `session/steer-unavailable` "current turn no longer accepts
  steering" with `{itemId}`; `steer` itself is `inbox.remove` followed by
  `agent.steer(message)`.

Three upstream facts shape the projection. `SessionProjectionMap.inbox` is
`{next-turn: UserMessage[], next-step: UserMessage[]}`, keyed literally `"inbox"`,
and a **`SessionQueuedItem`'s id is the message's id** -- the same value
`updateQueue.itemId` is matched against. The reference publishes the cell on every
inbox change *and* broadcasts a `{type: "queue", sessionId, items}` frame beside
it. And the pinned client's `replaceControlBaseline` reads only `baseline.jobs`
and `baseline.projections`: `queues` is declared in the type but never read by the
bundle this host ships, which is why this host's baseline already carries an empty
`queues` map and says "no queue mirror yet".

What this host has today is the other half: `session/prompt` maps both `mode:
"queue"` and `mode: "steer"` onto the running turn's steer queue, the steer id is
the console's own `requestId` (ADR 0111), and `dshwire` already projects a drained
steer as the console's `user/message`. Nothing reads that queue back out.

## Decision

**The pending queue is one cell, read from the run queue, and it is the only copy
of undelivered work.**

- **`inbox` is a projection cell, not a baseline field.** The follow snapshot
  seeds it for the session it opens (`values.inbox`, both lists always present,
  empty lists included: an absent key is how the client learns the capability does
  not exist), and later values arrive as `projection` frames on the control
  stream. It is deliberately **not** folded into the control baseline's
  projections block: that block carries one watermark for cells it shares, and the
  queue numbers its frames with its own wall-clock-anchored counter, exactly as
  the goal cell does for the same reason (ADR 0125). `queues` stays `{}`.
- **The row's identity is the message's identity.** A queued row's `id` and its
  `source.rpcId` are both the console's `requestId` -- the steer id the prompt path
  queued it under -- so the dock's `itemId` addresses the very message whose echo
  the console is holding, and a delivered message is recognised by the durable
  `user/message` it becomes (ADR 0111).
- **Placement mirrors the mode the console chose.** `mode: "queue"` lands in
  `next-turn`, `mode: "steer"` in `next-step`: the two lists are the console's own
  intention, so the host does not reclassify them. The mode is recorded when the
  prompt is queued; a message whose mode is unknown is treated as steering, which
  is the weaker claim.
- **The queue is read, never consumed.** `RunController` gained `PendingSteers`,
  `ReplaceSteer` and `RemoveSteer` (with `RunManager` and `Agent` wrappers). A peek
  hands back a copy, an edit rewrites the message in place and keeps its position,
  and a removal drops it so the run never sees it. Both mutations report `false`
  for an id that is no longer pending, which is the answer `session/updateQueue`
  turns into `session/queue-item-not-found`.
- **A mutation republishes the cell.** The console holds rows, not a snapshot it
  re-reads, so an accepted edit or removal derives the cell again and announces it
  on the control stream. Delivery is noticed the same way from the other side: the
  follow stream reconciles the run queue on every record and once more when the run
  ends, so a message the run was handed -- or that died with the run -- leaves the
  cell and retires the row.
- **`steer` on a queued turn is a promotion.** It moves the row into `next-step`
  (the visible list move the chat view renders) and leaves the message where it was
  in the queue. `steer` on a row already in `next-step`, or on a run that is no
  longer live, is `session/steer-unavailable` -- the reference's refusal for a
  boundary that has passed.

Four deviations are recorded rather than hidden:

1. **Both lists are delivered at the same boundary.** The reference consumes
   `next-step` at the next model step and one `next-turn` message at the next
   turn. This host's prompt path already hands both to the same next model-turn
   boundary (ADR 0111), so promotion changes where the row is shown and not when
   the run receives it.
2. **The pending queue is process state.** Upstream owns a *durable* inbox fold:
   pending input is spliced into the session's events and reconstructed from them,
   which is why its cell can live in the baseline block at all. Here the queue is
   the live run controller's, so a message queued for a run that ends before the
   boundary is dropped with the run instead of being carried into the session's
   next turn, and a restarted host has nothing to restore. The console is told the
   truth -- the cell empties, the row leaves -- and the durable fold is named as
   remaining work in `docs/dsh-console-handover.md` rather than implied to exist.
3. **`steer` does not reorder.** Promoting a queued turn past the messages ahead
   of it was not asked for by the client and is not done: the queue keeps its
   order, and the operator's `next-turn` rows are handed over one turn at a time.
4. **The `queues` mirror stays empty.** It is a second, newer shape of the same
   data that nothing in the pinned bundle reads; serving one path keeps the two
   from disagreeing.

## Consequences

- Pinned by `TestSessionPromptProjectsThePendingQueue` (both lists, the row id and
  its `source.rpcId`, the announced sequences) with
  `TestPendingQueueForgetsDeliveredMessages` (a delivered message leaves the cell),
  `TestSessionUpdateQueueEditsTheQueuedMessage`,
  `TestSessionUpdateQueueRemovesTheQueuedMessage`,
  `TestSessionUpdateQueueSteersAQueuedTurn`,
  `TestSessionUpdateQueueValidatesTheAction` (all five refusals, code, sentence and
  details), `TestSessionUpdateQueueRejectsUnknownArguments`, and
  `TestSessionUpdateQueueEnvelopeMatchesTheVendoredConsole` (the request keys, the
  three-kind union and the `{accepted}` result re-derived from the vendored
  bundle, plus the handler's own cases). `harness` pins the controller's read,
  replace-in-place and removal semantics, `RunManager` pins the run-level answers
  (`ErrSteerNotFound`, `ErrSteerUnavailable`, terminal runs) and compiles the
  production agent against the same shape, and `dshstream` pins the snapshot cell,
  its empty-but-present arm, and the out-of-baseline control frame.
- Live on a scratch host whose model endpoint holds the connection open (so the
  turn stays live without credentials): two queued prompts appeared in the follow
  snapshot's `inbox` under their request ids, an edit republished the row with its
  new text, `steer` moved it to `next-step`, removal emptied the list, every
  refusal answered with the reference's own code and sentence, and cancelling the
  turn emptied the cell.
- The ledger reads **48 served / 4 streams / 17 refused / 40 unserved** of 109, and
  the next-up list narrows to the skills/subagents cluster.
- The cost is one read of the run queue per followed record and per mutation, and
  one projection frame per change; no new store, no new durable state, and nothing
  in the queue path is consulted while serving a transcript.
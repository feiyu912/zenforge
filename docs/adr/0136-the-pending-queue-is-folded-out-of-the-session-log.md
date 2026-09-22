# 0136. The pending queue is folded out of the session log

- Status: Accepted
- Date: 2026-09-22
- Related: 0111 (both prompt modes reach one boundary), 0130 (the queue as the
  console's `inbox` cell, whose deviation 2 this ADR closes), 0133 (the feedback
  fold it borrows its shape from)

## Context

ADR 0130 served the console's `inbox` cell from the live run queue and recorded
the consequence as deviation 2: **the pending queue was process state.** A message
queued for a run that ended before its model-turn boundary was dropped with the
run, and a restarted host had nothing to restore -- the console was told the
truth (the cell emptied, the row left) but the operator's words were gone.

Upstream does not have that hole. `@deepseek-ai/dsh-agent-loop` keeps pending input
*in the session's own event log*: every acceptance, edit, removal and delivery is
one `agent/inbox/spliced` event, and the `inbox` projection cell is a fold over
those events (`inboxProjectionDefinition.apply`). Three facts made porting that
shape cheap here and expensive to skip:

- the event type is already in the pinned console's vocabulary
  (`KNOWN_SESSION_EVENT_TYPES`), so the host's projector serves it as a type the
  console knows -- no `ignorable` marker, no window damage;
- the console *reads* it: its submission observer treats `splice.inserted` as
  durable acceptance and retires a local echo from it, which is the same
  acceptance this host was already proving through the cell;
- the fold's validation is fully specified, and the payload carries no session id
  (the log is the session), which is exactly how this host's `sessionEvents`
  already collects a session's turns.

## Decision

**The queue is session-log state and the cell is a fold over it.** Every mutation
is recorded as the reference's own splice before the cell is derived:

| Change | Splice |
| --- | --- |
| `session/prompt` accepted (either mode) | append to `next-turn` (mode `queue`) or `next-step` (mode `steer`) |
| `session/updateQueue` `edit` | remove 1 + insert the new text at the same index |
| `session/updateQueue` `remove` | remove 1, `outcome: "canceled"` |
| `session/updateQueue` `steer` | cancel in `next-turn` + append to `next-step` |
| the run took a message | remove 1, **no** `outcome` -- a delivery is not a cancellation |

`removedCount` is omitted when nothing was removed and `outcome` appears only on a
cancellation, exactly as the reference writes them, so the console's own reading
of the event is not surprised. The fold is `inboxProjectionDefinition.apply`,
validations included: a splice whose range leaves its list is rejected, and an id
that would be pending in both lists at once is rejected.

Four things follow, and each is a decision rather than an accident:

- **The run queue is consulted only for delivery.** A folded row the live run no
  longer holds was handed to the agent, so the store records a claim at that
  moment -- the harness's claim is not instrumented, and the run queue is the
  authority on what the run holds. A run this process no longer holds proves
  nothing, so its rows stay pending. That distinction is the whole difference
  between "delivered" and "the process that held it is gone".
- **`sessionUpdateQueue` records the operator's intent, not an inferred
  delivery.** `DropSteer`/`EditSteer` run first (so a refusal is still the
  reference's own), and the splice is then written from a fold that does *not*
  infer delivery -- inference would read the just-dropped row as "the agent
  received it" and record a claim where the log owes a cancellation. Rows are
  located by identity at write time, never by a position read earlier.
- **A restored row is handed to the session's next turn.** `session/prompt` calls
  `restore` once the run exists: every folded row the run does not hold is steered
  into it under the console's own id, and stays pending until the run claims it.
  This covers a restart, a lease that expired with the process that held it
  (ADR 0109), and a run that ended before its boundary -- including the
  continuation branch, where the new turn is handed the rows before it runs.
- **The cell's frames keep their own sequence.** The rows now have log positions,
  but the frames still carry the wall-clock-anchored counter ADR 0130 chose: the
  console's history seed installs its projections at the cursor's watermark and
  discards a frame numbered at or below it.

## Deviations, recorded rather than hidden

1. **A restored row waits for the session's next prompt; it does not start a turn
   of its own.** Upstream's loop claims one `next-turn` message per turn and keeps
   looping, so a queued message becomes its own turn. Here the row is delivered at
   the next turn's boundary, whoever starts it. The console shows it pending in
   both cases, so nothing it renders is untrue; what differs is liveness, not
   truth, and starting runs on the host's own initiative is a session-lifecycle
   change this chain does not make.
2. **Promotion is two splices, not remove-then-steer.** The reference removes the
   row and hands the message to the agent outside the inbox. Here both halves are
   delivered at the same boundary (ADR 0111, and ADR 0130's deviation 1), so the
   row is canceled in `next-turn` and appended to `next-step`: the log describes
   where the console shows it, which is the only thing promotion changes.
3. **A restored row is served but not deliverable while the previous run is still
   listed as active.** A host restarted after a hard stop adopts that run from the
   registry with a 30-second lease, and the prompt path refuses to deliver into a
   run it does not own (ADR 0109). The row is still served, truthfully, and the
   refusal names its reason; once the lease expires the next prompt delivers it.
4. **A fold that cannot be replayed answers an empty cell and refuses every
   mutation.** Reads have no error arm in the transport seam, so a corrupt inbox
   history is the one case where the empty cell is not the truth; both writes and
   the refusal say so with `gateway/internal` and the fold's own message. This
   host cannot write such a log -- the validation is the reference's, and every
   splice is built from a fold of the same log -- which is why the check is a unit
   test rather than an operator-facing path.

## Consequences

- Pinned by `TestQueuedMessagesAreSplicedIntoTheSessionLog` (the reference's event
  type, payload and omitted keys), `TestPendingInputOutlivesTheProcessThatQueuedIt`
  (a second handler over the same store, with a manager that holds no run, restores
  both rows), `TestRestoredPendingInputIsHandedToTheNextTurn` (the restored row is
  steered into the new run, under the console's id, and the prompt that started the
  turn is the turn's input rather than a pending row),
  `TestQueueEditsAndRemovalsAreRecordedAsSplices`,
  `TestPromotionMovesTheRowInTheLog`,
  `TestDeliveredPendingInputIsClaimedInTheLog` (a claim, no `outcome`, and the
  restarted host does not bring the row back), `TestTheFoldedCellMatchesTheRunQueue`
  (the cell and the run queue agree on which rows are pending), and
  `TestInboxFoldRejectsAnInvalidLog` (the reference's two validations and three
  malformed-payload arms).
- Live, on a real host over a real jsonl log:
  - queued behind a live turn, the cell showed the row and the log held
    `{"target": "next-turn", "start": 0, "inserted": [{"id": "req-live", ...}]}`;
  - the host was stopped mid-turn, so the run, its queue and every byte of process
    memory were gone;
  - a fresh host serving the same directory -- which never held the run -- answered
    the follow snapshot with `inbox.next-turn = [{id: "req-live", text: "queued
    behind the running turn"}]`, folded from the log alone;
  - after the adopted run's lease expired, the next prompt was accepted, the
    restored row was delivered as a `user/message` in the new turn, and the log
    gained the claim `{"target": "next-turn", "start": 0, "removedCount": 1}` with
    no `outcome`, after which the cell was empty.
- The console's `inbox` cell is now a *view* of durable state rather than a mirror
  of a map, which is what ADR 0130's deviation 2 named as missing work; the
  handover's next-up list moves on to what the refused families would need
  (ADR 0135).
# ADR 0113: Cancel Reaches the Turn That Is Running

Status: accepted

Relates to [ADR 0108](0108-a-sessions-log-is-one-sequence-across-its-turns.md) (the chain of
turns this ADR resolves), [ADR 0109](0109-the-console-host-keeps-its-run-registry-on-disk.md)
(the lease rule the same path reads) and [ADR 0112](0112-the-coverage-ledgers-gaps-are-a-dated-audit.md)
(the ledger entry this closes).

## Context

The operator pressed Stop on a conversation whose second turn was waiting on an approval. The
console answered:

```
session "run_1789915960885194000" already finished with status "completed"; cancel is a no-op
```

and the second turn kept running. Cancelling that turn by its own id (`<session>~2`) worked, so
the defect was only in which run the method named:

```go
if err := h.manager.Cancel(sessionID); err != nil {
```

`sessionCancel` cancelled the id its caller sent. The console sends the session it has open,
whose id is the conversation's **first** turn (turn one is the session id; turn *k* is
`dshsession.ContinuationRunID(sessionID, k)`, ADR 0108), so in a multi-turn conversation Stop
cancelled a run that had already finished and reported that as a conflict. The turn the
operator was watching — the newest one, the only one that can be running — was never named.

This was recorded in `docs/limitations.md` when it was found, and the coverage ledger's
`session/cancel` row said the same thing: the gap was known and deliberately left.

## Decision

`session/cancel` cancels the **newest turn** of the conversation its argument names.

- The argument may name any turn; the handler resolves the conversation the way every other
  session method does (`resolveSession`, then the turn chain) and cancels the last turn in it,
  because a continuation only starts after its predecessor reached a terminal event, so the
  newest turn is the only candidate for a running one.
- A session with no turns yet (a draft the console has created but not prompted) keeps the id
  it was named by, which the run manager answers as `session/not-found` exactly as before.
- The idempotence rule is unchanged: cancelling an already-cancelled run is accepted, and any
  other terminal state is a conflict.
- The conflict names the turn it refused, and its details carry it as `turn`, while
  `sessionId` stays the id the caller sent — the answer is about the run, the envelope is about
  the request.

## Consequences

- Stop stops the turn that is running. Verified on the operator's host while cleaning up a
  verification session: the base id answered the conflict above, the same conversation's
  newest turn was accepted, and the sidebar row stopped reporting `running`.
- Stopping a conversation no longer depends on the console knowing the continuation id, which
  it does not: it opens a session, not a turn.
- A conflict for a finished conversation now names `…~<k>`, which is the run whose status the
  message reports. The operator's earlier confusion — a message about turn one while turn two
  was visibly running — cannot recur.
- Nothing about the run manager changed: this is the adapter resolving which run the console's
  Stop means, exactly as it resolves which turns a page is built from.
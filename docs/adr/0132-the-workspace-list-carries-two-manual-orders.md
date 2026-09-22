# 0132. The workspace list carries two manual orders

- Status: Accepted
- Date: 2026-09-22
- Related: 0094 (the registry is process-local), 0101 (which serves the rest of
  the namespace), 0131 (the chain before it)

## Context

Two methods mutate the order the console draws, and both were unserved:

- `workspace/insertBefore({workspaceId, beforeWorkspaceId?}) →
  {workspaceIds: string[]}` moves one registration within the workspace list.
- `workspace/insertSessionBefore({workspaceId, sessionId, beforeSessionId?}) →
  {workspace: WorkspaceView}` moves one session within a workspace's list.

They are what the console's drag-and-drop sends: the first when an operator
reorders the sidebar's workspaces, the second when a session row is dragged inside
one. The reference reads both as *DOM-insertBefore*: the moved id lands
immediately before its anchor, an absent anchor appends, and an anchor naming the
moved row is a no-op. A reorder it cannot perform is a refusal -- for a workspace,
`workspace/not-found` with the offending id (the reference maps its own
`WorkspaceOrderInvalidError` that way); for a session, `workspace/move-invalid`
with `{workspaceId, sessionId, beforeSessionId?}` and the sentence
`cannot move session '<id>' in workspace '<path>': the session is not accounted`.

This host already had the data. `consoleWorkspaces` holds `order []string` for the
registrations and `sessionIDs []string` per row, and the follow stream's vocabulary
already declares an `order` frame (`WorkspaceUpdate{Kind: "order", WorkspaceIDs}`)
that nothing published. The chain is therefore the mutation, its two refusals, and
the frames the other console surfaces need to stay in step.

## Decision

**The registry owns both orders and both moves.** `InsertBefore` and
`InsertSessionBefore` join the `WorkspaceRegistry` face; the unary namespace parses
the request (the console wraps the fields in its `request` parameter, which the
dispatcher already flattens) and returns the protocol's value unchanged.

- `InsertBefore` returns the **complete resulting order**, not a delta: the client
  replaces its list, so an answer that disagreed with the host would be
  unrepairable.
- `InsertSessionBefore` returns the **whole row**, because the console renders
  `sessionIds` in the row's own order.
- An absent anchor appends; an anchor naming the moved row returns the list
  unchanged. Both are the reference's semantics, and neither publishes a frame --
  a no-op that notified would make the client redraw for nothing.
- An accepted workspace move publishes an `order` frame carrying the new order; an
  accepted session move publishes the changed row as the `upsert` frame it is.
- Either id being unknown is `workspace/not-found`, the reference's own mapping,
  and a session or anchor that workspace does not account is `workspace/move-invalid`
  with upstream's sentence and details. A blank `workspaceId`/`sessionId` is this
  namespace's existing `gateway/arguments-invalid` for a missing field.

The splice itself (`movedBefore`) is the reference's
(`dsh-workspace/lib/index.js:409-428`): remove the id, find the anchor in what
remains, insert. Removing first is what makes a downward drag behave -- an anchor
that sat after the moved row keeps its meaning once the row is gone.

## Deviations, recorded

1. **The initial session order is the host's accounting order.** Upstream prepends a
   newly accounted session (`sessionIds: [sessionId, ...]`), so its fresh rows read
   newest-first; this host appends, because that is what ADR 0101 shipped. The two
   agree the moment an operator drags, and the served order is always the host's
   own -- but a freshly created session appears last here and first there.
2. **The order is as durable as the registry.** ADR 0094 made the registry
   process-local console state with no durable home, and a manual order inherits
   that: a restart begins from the host's own workspace in its construction order
   and with sessions in accounting order. Serving the move does not make the order
   survive, and the console is told only what the host knows.

## Consequences

- `workspace/insertBefore` and `workspace/insertSessionBefore` are served,
  taking the ledger to 51 served, 4 streams, 17 refused, 37 unserved of 109.
- The `order` frame the follow stream declared now has a publisher, so a second
  console tab sees a reorder instead of a stale list.
- Live evidence, from a `zenforge serve` on this commit holding three
  registrations and three sessions, with the console's `workspace/follow` stream
  open:
  - `workspace/follow`'s baseline listed `[ws-681e6…, ws-80d71…, ws-45c7a…]`;
  - `workspace/insertBefore` moving the third before the second answered
    `{"workspaceIds": ["ws-681e6…", "ws-45c7a…", "ws-80d71…"]}`, and the stream
    delivered the matching frame
    `{"type": "order", "workspaceIds": ["ws-681e6…", "ws-45c7a…", "ws-80d71…"]}`;
  - the same call with no anchor answered the order with the moved row appended, and
    onto itself answered the identical order;
  - an unknown row and an unknown anchor both answered
    `workspace/not-found` with `{"workspaceId": "ws-nobody"}`;
  - `workspace/insertSessionBefore` moving the last session before the first
    answered the whole row with `sessionIds` in the new order, an absent anchor
    appended, an unaccounted session answered `workspace/move-invalid` with
    `cannot move session "run-nobody" in workspace "/private/tmp/…/ws": the session
    is not accounted` and `{sessionId, workspaceId}` (no `beforeSessionId`, because
    none was sent), an unaccounted anchor answered the anchor sentence with all
    three ids, and an unknown workspace answered `workspace/not-found`.
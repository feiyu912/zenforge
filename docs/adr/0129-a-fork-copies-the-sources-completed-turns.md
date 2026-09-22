# ADR 0129: A Fork Copies the Source's Completed Turns Into a Child of Its Own

Status: accepted

## Context

Two console affordances fork a conversation: the sidebar's row action
(`ui-workspace`: `forkSession = (sessionId) => sessions.fork({sessionId,
increaseTitle: true})`) and a message's own action (`ui-chat`: `forkAt = (seq) =>
sessions.fork({sessionId, atSeq: seq, increaseTitle: true})`). The wire request
they produce carries exactly two fields -- `sessionId` and an optional `atSeq`,
the console sequence of the message to fork at (`SessionSeq(Math.floor(...))`;
`increaseTitle` never leaves the page) -- and the answer is the child's
`sessionId`.

The reference host implements it by seeding a session with a slice of the
source's events (`ApiSessionList.fork`):

- `atSeq` is validated as a non-negative safe integer, else
  `gateway/bad-request` "atSeq must be a non-negative safe integer";
- the source is observed, and a session the store does not hold is
  `session/not-found` with `{sessionId}`;
- the boundary is the **first `turn/end` at or after `atSeq`**, or -- when `atSeq`
  is absent or past the log -- the **last `turn/end`**; no boundary is
  `session/fork-unavailable`, with "has not completed the turn containing event N"
  or "has no completed turn to fork from";
- the cut walks past the boundary to the next `turn/start`, so the child inherits
  whole turns;
- the child is created with `seed: events[0, cut)` and
  `inheritedEventCount: cut`, inheriting the source's working directory and
  preset, composed with the host's **default** model rather than the source's
  choice;
- it is then attached to the source's workspace; a failure there is
  `session/workspace-attach-failed` with `{sessionId, workspaceId}`, and the
  console reads exactly that `sessionId` out of the error to open the child
  anyway (`workspaceAttachSessionId`). The console also records the child
  optimistically, with `parentSessionId` set to the source.

None of that seeding exists here: a session is a chain of run logs, and there is
no "create a session from someone else's events" path.

## Decision

**A fork is a copy, materialized once.** The source's turn logs up to the boundary
are written into the child's own run chain (`<child>`, `<child>~2`, … -- the same
ids any conversation uses, so every reader of the chain works unchanged), and the
child then owns its history: renaming it, deleting the source or continuing the
source cannot reach it.

- **The boundary rule and its refusals are the reference's, word for word.** The
  search is over the *projected* log, because `atSeq` is a console sequence (the
  number the page shows for a record), not a durable sequence. `dshwire` gained
  one structural field for it -- `SessionLog.TurnRecords`, the count of records
  each turn contributed -- because a turn that contributed no records repeats the
  continuation point of the one before it, so the served sequence alone cannot
  name the turn a record belongs to. `TurnContaining` maps a record back to its
  turn, and the child inherits `Runs[:turn]`: whole turns, which is what the
  reference's "advance to the next `turn/start`" also lands on.
- **The child is registered as a run that happened.** The console's session list
  is built from run records (`Record`/`List`), and a conversation the host holds
  but does not list is one the sidebar cannot show. `RunManager.Record` adds a
  terminal record for a run this host materialized rather than executed, using the
  same claim-and-release the boot adoption of stored runs uses, so the record is
  durable in the registry and visible to a restarted host. A nonterminal status or
  an id the host already knows is refused rather than written.
- **The lineage travels in the log.** The reference keeps `parentSessionId` in
  session metadata. This host has no metadata plane, so the child's first turn
  opens by naming its source in the event payload (`parentSessionId`), and
  `dshwire.SessionLog.ParentSessionID` reads it off that turn -- which
  `session/list` serves as the row's `parentSessionId`, the field the console's
  `flattenLineage` nests a child under its source with. The projection ignores the
  field, so the transcript is unaffected. A conversation nobody forked omits it.
- **The child lands where its source is.** This host runs every conversation in
  one workspace, so "the source's workspace" is that one; the attach failure is
  the reference's `session/workspace-attach-failed` with the child's id in the
  details, and the child's model projection is seeded the way a created session's
  is (ADR 0102) -- the child runs on the host's default model, which is what the
  reference composes it with.

Two deviations are recorded rather than hidden:

1. **The child gets an ordinary id.** `run_<nanos>`, collision-probed, rather than
   `session-<uuid>`: this host issues run ids and a session is its first turn, and
   a second id shape would have to be taught to every reader of the chain.
2. **The fork is a snapshot.** Later turns of the source do not appear in the
   child, and the copied records keep their original times; only the record the
   fork creates is stamped with the fork's own moment, so the child sorts as the
   newest conversation instead of behind its source.

## Consequences

- Pinned by `TestSessionForkCopiesCompletedTurnsIntoAChild` (record-for-record and
  sequence-for-sequence equality with the source, the copied title, the child
  listed and not blank, its `parentSessionId`, and the source's row carrying
  none), `TestSessionForkAtSeqKeepsTheTurnsUpToIt` (a cut inside turn 1 keeps turn
  1; a sequence past the log means the last completed turn),
  `TestSessionForkRefusesAnUnfinishedTurn` (the exact fork-unavailable message,
  the running turn left out of an unanchored fork, and a running-only source),
  `TestSessionForkRefusesUnknownSessionsAndBadArguments` (not-found, a draft, and
  every argument refusal including the reference's own sentence),
  `TestForkedSessionContinuesTheInheritedConversation` (the child's next turn
  carries the inherited exchange), `TestForkedSessionSurvivesARestart` (the
  child's records and lineage survive a host restart), and
  `TestSessionForkEnvelopeMatchesTheVendoredConsole`. `RunManager.Record` has its
  own tests for the in-process and registry-backed paths,
  `dshwire` pins `TurnContaining` across an empty turn and the lineage read, and
  the ledger's coverage test keeps the route and the roster in step.
- Live on a scratch host with no model credentials: a prompt recorded and then
  forked produced a child whose page carried the inherited turn, whose list row
  named the source, and which continued the conversation at turn 2 in one
  sequence; `atSeq: 2` on a two-turn source produced a child with turn 1 alone;
  a draft session answered "has no completed turn to fork from", `atSeq: -1`
  answered the reference's bad-request, and an unknown id answered
  `session/not-found`.
- The ledger reads **47 served / 4 streams / 17 refused / 41 unserved** of 109,
  and its next-up list narrows to `session/updateQueue`.
- A fork costs what the copied turns cost: one read of each inherited turn's log
  before anything is written (so a source the host cannot read leaves no child
  behind), then one append per copied event. A write failure mid-way names the
  partially written child in the error so the operator can delete it with
  `session/delete`.
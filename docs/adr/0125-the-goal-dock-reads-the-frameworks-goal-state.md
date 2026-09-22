# ADR 0125: The Goal Dock Reads and Mutates the Framework's Goal State

Status: accepted

## Context

The console declares a goal surface and this host served none of it: the seven
`goals/*` methods had no route, the `goal` projection had no producer, and
`goal/activation-changed` was never forwarded. The composer's goal bar was
therefore permanently empty -- `useProjection("goal")` returned nothing and no
mutation could ever be called.

What the client actually needs, from the bundles the page runs:

- **The dock renders the projection, not a read.**
  `GoalDock` reads `useProjection("goal")` and takes `.goal` off it
  (`client/ui-goal/client.js`, `GoalDock`); the bar renders nothing for
  `undefined` (not loaded), `null` (no goal) or a `complete` phase
  (`GoalBar`'s own doc comment). Pause appears only for
  `phase === "active" && activation === "armed"`, resume for `paused` or
  `active && disarmed`, and the activation is matched **per `{id, revision}`**
  -- `useGoalActivation((next) => next.id === goalId && next.revision === revision
  ? next.activation : undefined)` -- so a stale activation is indistinguishable
  from none, and the actions disappear.
- **Activation is read and pushed.** `createGoalActivationSource` reads
  `goals.get(sessionId)` whenever the projection shows an **active** goal
  (`activeRef`), publishes `{id, revision, activation}` from the read, and
  overwrites it with whatever `goal/activation-changed` carries
  (`ctx.remote.$on("goal/activation-changed", …)` in the `ui-goal` inject).
- **The read distinguishes "no goal" from "a goal".** The client tests
  `goal === void 0` and otherwise publishes `{id: goal.id, …}`; the transport
  passes `result.value` straight through, so a missing `value` key is
  `undefined` (`client/connection/client.js` `parseConnectionResponse`). A JSON
  `null` would reach `goal.id` and throw inside the `.then`.
- **Creation is not on the dock.** `GoalBar`'s comment says it outright: "Goal
  creation lives on the `/goal` command, not here". The dock calls `edit`,
  `pause`, `resume` and `clear` (each with the `{id, revision}` ref it read),
  and `goals/get`; `goals/create` and `goals/complete` are the host-side half of
  the contract.

The framework already owns the rules and the state: `goals.Create/Edit/Pause/
Resume/Complete`, a durable `goals.State` document per session, and the same
`<checkpoint-dir>/goals` directory the command line and the goal tools use.

## Decision

- **The seven methods are an adapter over the framework's state machine**, not a
  second implementation: `cli/consolergoals.go` implements `dshapi.GoalStore`
  over `goals.FileStore` and `goals.Create/Edit/Pause/Resume/Complete/Delete`,
  and `internal/dshapi/goals.go` maps the HTTP envelopes onto it. A create while
  a goal is current is `GOAL_ALREADY_EXISTS` (the machine reports the same rule
  as an invalid transition), a mutation names the exact `{id, revision}` it read
  and is refused `GOAL_STALE_REVISION` when the store has moved past it, and
  `goals/get` with no goal answers `{ok: true}` with **no value key at all**.
- **Envelope shapes are read from the vendored bundle, not typed twice.**
  `TestGoalEnvelopesMatchTheVendoredConsole` extracts each method's parameter
  wire names, its object parameters' keys, its result keys, whether the result
  admits `undefined`, and compares them with what this host's own handler accepts
  (parsed from `goals.go`). A console upgrade that renames a wire or widens a
  result fails a test instead of reaching the dock as a runtime rejection.
- **The projection cell travels with the session's own snapshot, not the control
  baseline.** The baseline carries one watermark per session block, and the
  model-selection cell in that block is numbered by its own store's sequence; a
  goal frame must outrank the **session cursor** (the history seed installs the
  block at the cursor and the client discards any frame numbered at or below it),
  so folding the goal in would freeze the model picker. The follow snapshot
  carries the cell -- which also keeps the client's seed from clearing it on a
  reconnect -- and every later value arrives as a control frame whose sequence is
  `consoleGoals`'s own wall-clock-anchored monotone counter. That counter is what
  makes a `resume` visible: if the frame lost to the cursor, `activeRef` would
  still see a paused projection and the dock would never re-read activation.
- **Activation rides the store's change feed twice.** The same `GoalUpdate` feed
  produces the control frame and the `$events` `goal/activation-changed` emit, so
  the two cannot disagree: `{sessionId, goal: {id, revision, activation}}` for a
  live goal, and `{sessionId}` with no `goal` after a clear -- exactly the shape
  `onActivation` reads as "no live goal".
- **Activation is derived from durable state.** `armed` means the phase is
  `active` and the round budget is not spent. The reference derives it in a live
  scheduler; this host has no served goal-continuation loop, so deriving it keeps
  every reader's answer identical and surviving a restart instead of inventing a
  process-local claim.
- **`clear` removes the document.** The reference keeps a tombstone plus the set
  of identities already created in the session, so reusing an id or mutating with
  a pre-clear ref fails as duplicate/stale. This host drops the state document,
  so both report `GOAL_NOT_FOUND`. The dock cannot observe the difference -- it
  hides on the null cell and offers no mutation without a current goal -- and the
  deviation is recorded here rather than keeping an id table for a client that
  never sends one.

## Consequences

- Pinned by `TestGoalEnvelopesMatchTheVendoredConsole` and
  `TestGoalEnvelopeValuesMatchTheVendoredConsole` (the bundle boundary, including
  the accepted-argument names), the seven method tests in
  `internal/dshapi/goals_test.go` (no-value read, request splice, empty edit,
  malformed refs, unknown session, store code passthrough, missing store),
  `internal/dshstream/goals_test.go` (the follow cell and its null arm, the
  deliberately goal-free control baseline, a projection frame per mutation, the
  cleared null cell, and both emit shapes) and `cli/consolergoals_test.go` (the
  whole lifecycle over a real file store, the stale-ref refusal, the missing
  cases, id minting, and the publish feed).
- Live on a scratch host (`--addr 127.0.0.1:8799`, throwaway
  `--checkpoint-dir`/`--settings-file`), a created session walked
  `get`(no goal)→`create`→`edit`→`pause`(stale ref refused)→`pause`→`resume`→
  `complete`→`create`→`clear`→`get`(no goal). The served line of the no-goal read
  is `{"ok":true}`; the stale ref is
  `GOAL_STALE_REVISION … is at revision 2, not 1`; the projection frame is
  `{"type":"projection","key":"goal",…,"seq":1790044141036}` with no `activation`
  in the cell; the emit is
  `{"type":"emit","event":"goal/activation-changed","args":[{"sessionId":…,"goal":{"id":…,"revision":3,"activation":"armed"}}]}`,
  and the cleared one carries `sessionId` alone. The status document for the last
  goal is gone from `<checkpoint-dir>/goals` after a clear.
- Live evidence also caught a defect in the id minting: the first version
  suffixed **every** goal after the first one (`goal-<nanos>-1`) because it tested
  the suffixed candidate and never the free base. Fixed and pinned by
  `TestConsoleGoalIdentifiersSuffixOnlyOnCollision`.
- Still missing, and named as such in `docs/limitations.md`: the composer's
  `/goal` command is a built-in **host** command
  (`@deepseek-ai/dsh-command-goal`) that this host does not implement, so the
  dock's own creation path is absent -- a goal is created host-side (`serve
  --goals`, the `create_goal` tool, or the goal state document). `goals/complete`
  is served but the shipped dock never calls it, and the coverage ledger's next-up
  list moves past `goals/*`.
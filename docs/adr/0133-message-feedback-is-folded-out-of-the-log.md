# 0133. Message feedback is folded out of the session's own log

- Status: Accepted
- Date: 2026-09-22
- Related: 0110 (the console message id is the session sequence), 0117 (the
  projected window and the sequence), 0132 (the chain before it)

## Context

Four methods make up the feedback surface:

- `messageFeedback/put({sessionId, messageId, rating, note?, category?,
  ifVersion})` stores or replaces the Like/Dislike on one finalized assistant
  message;
- `messageFeedback/list({sessionId})` returns every current item;
- `messageFeedback/delete({sessionId, messageId, ifVersion})` removes one;
- `sessionFeedback/record({sessionId, text?, category?})` records one remark about
  the conversation itself.

Two things about them are unlike the rest of this surface, and both come from the
vendored schema rather than from a choice here:

1. **The outcome rides inside the value.** Each result is a union of
   `{ok: true, value: {...}}` and `{ok: false, error: {code: ...}}`, and the method
   itself succeeds. The console reads the union (`carried.ok`, then `result.ok`,
   then `result.error.code`), so a business refusal must not be a method-level
   error envelope.
2. **The business codes are the schema's five.** `session-not-found`,
   `target-not-found` (with the message id), `version-conflict` (with the item the
   client lost against, or null), `note-blank`, and `note-too-large` (with
   `maxBytes` and `actualBytes`). `messageFeedback/list` has only the first, and
   `messageFeedback/delete` only the first and the third.

The reference keeps the state as **session-log events**: `feedback/message-put`
carries `{sessionId, item}` and `feedback/message-delete` carries
`{sessionId, messageId}`, and every read replays them into the current item set
(`dsh-message-feedback/lib/index.js`, `currentItems`). That is what makes the
answer durable without a store, and this host's vocabulary already reserved all
three types (`internal/dshwire/vocabulary.go`), so the same shape is available
here.

## Decision

**No store: the session's own log is the state.** `put` and `delete` append one
event each; `list` replays the log. `sessionFeedback/record` appends
`feedback/record`. Nothing is held in process memory, so a restart changes nothing
about what a conversation's feedback says.

- **The fold replays the raw events of every turn.** It cannot read the *projected*
  window: a feedback event projects to a console record no transcript renders, so
  the projected records are not the input the fold needs. The fold also checks each
  event's own `sessionId`, so a log that carries another session's events cannot
  contribute to this session's answer.
- **`put` validates the target the way the reference does**: the message id must
  name a finalized assistant message in the session's projected log, which is what
  stops a rating from attaching to an id the console invented.
- **`ifVersion` is the concurrency token.** `null` means "the client observed
  nothing"; a mismatch is `version-conflict` carrying the live item, so a client
  reconciles without a second read. A write that repeats the stored judgment keeps
  the version and appends **no** event, because the Like button is idempotent and
  an event per click would fill the log with duplicates. `delete` of an item that is
  already gone succeeds without an event, which is the reference's own
  postcondition.
- **The note policy is the reference host's**: 8192 bytes
  (`dsh-web-app/cordis.patch.yml` sets `maxNoteBytes: 8192`), a whitespace-only note
  is `note-blank`, an oversize one is `note-too-large` with both numbers, and the
  stored note keeps the whitespace the operator typed -- only the blank check trims.
- **A malformed request is still an argument error.** A rating outside the two
  literals, a category outside the seven, a missing `ifVersion`, and an unknown
  argument are `gateway/arguments-invalid`: the union is for outcomes, not for
  nonsense the console could not have sent.
- **The events are appended to the session's newest turn** with the moving-tail
  retry `appendTitle` already uses, and they need no marker: `feedback/*` is in the
  console's own known-event set, so the projector passes each through with its own
  name and payload, without `surfaceOp` (it is not a model-visible surface type) and
  without `ignorable` (the console does know it).

## Deviations, recorded

1. **The newest turn's log, not a live session object.** Upstream appends to the
   live session it holds; a session whose run has ended cannot take a remark there
   at all (`sessionFeedback/record` requires `ctx.sessions.get`). This host has no
   live-session object -- a session is its durable log -- so it appends to the
   newest turn of a session it knows, and the fold reads every turn. The item is the
   session's, not the turn's, so the answer is the same either way; what differs is
   that a remark about a finished conversation is accepted here rather than refused.
2. **The version is minted by the host.** Upstream uses `randomUUID()`. This host
   mints a v4 UUID from `crypto/rand`, falling back to a timestamp token if the
   entropy source fails: the console only compares versions for equality, so the
   contract is "unique per write", not "random".

## Consequences

- `messageFeedback/put`, `messageFeedback/list`, `messageFeedback/delete` and
  `sessionFeedback/record` are served, taking the ledger to 55 served, 4 streams,
  17 refused, 33 unserved of 109.
- Live evidence, from a `zenforge serve` on this commit whose provider was a local
  scripted endpoint, so a turn actually produced a finalized assistant message
  (`msg-9`):
  - `messageFeedback/list` first answered `{items: []}`; `put` with
    `rating: "positive"`, a note and `category: "task-result"` answered the item
    with a minted `version`;
  - a second `put` naming the observed version replaced the item (new version, same
    `createdAt`), a `put` naming the **stale** version answered
    `{code: "version-conflict", current: {...}}` with the live item, and a `put` for
    `msg-9999` answered `{code: "target-not-found", messageId: "msg-9999"}`;
  - `note-blank` and `note-too-large` (`maxBytes: 8192`, `actualBytes: 8193`)
    answered their own arms; an unknown session answered `session-not-found`; and a
    bad rating answered the *method-level*
    `gateway/arguments-invalid: "rating" must be "positive" or "negative"`;
  - `delete` with the observed version answered `{absent: true}`, a second delete
    answered the same postcondition without a second event, and a delete naming a
    version that is not current answered `version-conflict` with the live item;
  - `sessionFeedback/record` answered `{recorded: true}` with text and category, and
    again with neither; an unknown session answered `session-not-found`;
  - the session's own log afterwards read
    `… assistant/message, step/end, turn/end, feedback/message-put,
    feedback/message-put, feedback/message-delete, feedback/record,
    feedback/record` -- the state is visible in the log the console already reads.
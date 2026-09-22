# ADR 0127: The Sidebar's Search Reads the Logs the List Already Shows

Status: accepted

## Context

The workspace browser renders a search box over the session list. It calls
`session/search` with one literal phrase and a cancellation signal
(`client/ui-workspace`: `searchSessions = (query, signal) => sessions.search(query,
signal)`), and the session manager documents the call as "Search visible session
message content without adding transient query state to the list snapshot", with
`query` a "non-blank literal phrase" and a client-side `searchResultLimit = 20`
(`api/session-controller/src/client/sessions/manager.ts`).

The reference host answers it from a mounted index provider
(`@deepseek-ai/dsh-api-session-controller`, `ApiSessionList.search`), and the
semantics the client depends on are all there:

- the query is normalized before use (`normalizeSearchQuery`): trimmed; empty is
  `gateway/bad-request` "session search query must not be empty"; more than 500
  UTF-16 code units is the same code with "must contain at most 500 UTF-16 code
  units"; a NUL is refused as "must not contain NUL";
- without the provider it answers `gateway/internal` "session search is
  unavailable: this deployment does not mount `@deepseek-ai/dsh-session-query`";
- it searches only *visible* sessions (those with a working directory), only
  events of type `user/message` and `assistant/message`, and only on the
  `current` surface;
- **one item per session**, carrying that session's `bestMatch` excerpt, truncated
  to 240 code points by `truncateUnicodeCodePoints`;
- at most 20 items, with `hasMore` true when a session was left out;
- an aborted signal is `gateway/cancelled`.

The matcher itself lives in that provider: `compileSessionTextFilter` compiles a
"literal case-insensitive, whitespace-flexible semantic-text match" -- the phrase
is split on whitespace, its regex metacharacters are escaped, and the parts are
rejoined with `\s+` under the `iu` flags.

This host mounts no query provider, and it does not need one to answer the
question: it already reads and projects exactly these conversations for
`session/list`, `session/page` and `session/follow`. What it must not do is send
the sidebar a shape or a refusal the reference would not.

## Decision

- **Answer from the projected logs.** `sessionSearch` walks the conversations the
  list shows, reads each one's projected log -- the same `dshwire.SessionLog` the
  list row's title and the page's records come from -- and matches the phrase
  against the text blocks of its `user/message` and `assistant/message` records.
  A user message carries its content directly, an assistant message nests the wire
  message under `message`; nothing else is message content, which is the same
  restriction as the reference's event filter. Our projection only ever stamps
  `surfaceOp: "append"` (a conversation here is linear, ADR 0108), so the
  reference's `surface: current` filter is a no-op rather than something to
  reproduce.
- **The list and the search are one enumeration.** The grouping, blank filtering
  and newest-first ordering that `session/list` applies were extracted into
  `visibleSessions`, and both handlers read it: a session is searchable exactly
  when it is listed, and a conversation whose log cannot be read is skipped rather
  than answered with an invented hit or a failed search.
- **The contract is mirrored exactly.** One row per conversation; the twenty-item
  cap with `hasMore`; the 240-code-point excerpt, cut with the reference's own
  longest-prefix truncation; and the reference's own query refusals, word for word
  (`gateway/bad-request` for empty, over-length and NUL; `gateway/arguments-invalid`
  for a missing or non-string `query`, which the reference's strict schema rejects
  at the gateway before its handler runs). The matcher is the reference's
  compiled filter: the phrase is data, its metacharacters are literal, its words
  may be separated by any whitespace, and case is ignored.
- **Three deviations are recorded rather than hidden**, all of them consequences
  of not mounting an index:
  1. **No ranking.** The reference's `bestMatch` is the strongest hit by its
     index's rank. Here the row quotes the conversation's **newest** matching
     message, and the rows keep the session list's order (newest conversation
     first) instead of a global relevance order.
  2. **No index and no page cursor.** Every query reads the listed conversations'
     logs. There is no cursor in the response because the client never sends one;
     one extra row is collected to answer `hasMore` without a second pass.
  3. **No cancellation.** The signal the client passes is not carried by this
     host's unary transport, so `gateway/cancelled` has nothing to report. The work
     is bounded by the listed conversations plus one match, so the answer arrives
     in the time a rejected call would have taken. The excerpt is ours as well: a
     window starting up to 60 code points before the match, with a leading
     ellipsis when the text was cut, and no markup -- the reference's excerpt
     comes from its index's snippet function.

## Consequences

- Pinned by `TestSessionSearchFindsMessageText` (both conversations, newest
  first, the matching text quoted), `TestSessionSearchMatchesLiterally` (case,
  whitespace and literal punctuation), `TestSessionSearchIgnoresNonMessageRecords`
  (a tool result is not message content), `TestSessionSearchKeepsTheNewestMatchPerSession`
  (one row per conversation, the newest turn), `TestSessionSearchCapsResultsAndReportsMore`
  (twenty rows and `hasMore`), `TestSessionSearchNormalizesTheQuery` (the
  reference's refusals, including 500 UTF-16 units as 251 astral code points), and
  `TestSessionExcerptIsBounded` (the 240-code-point bound counted in code points,
  never a split one). `TestSessionSearchEnvelopeMatchesTheVendoredConsole` reads
  the generated remote map: one `request` object holding `query`, a result of
  `items` and `hasMore`, and an item of `sessionId` and `snippet`.
- `session/list` is unchanged in what it serves; sharing the enumeration changed
  no row it renders, which the existing list tests keep asserting.
- The ledger moves to **46 served, 4 streams, 16 refused, 43 unserved** of 109,
  and its next-up item 1 narrows to `session/fork`, `session/attachment` and
  `session/updateQueue`.
- Live on a scratch host (`--addr 127.0.0.1:8803`, throwaway `--checkpoint-dir`
  and `--settings-file`), a conversation prompted with "the quarterly report
  discusses the harbor crane budget" was found by `{"query":"harbor crane"}` and
  by the multi-space `{"query":"QUARTERLY   REPORT"}` alike:
  `{"hasMore":false,"items":[{"sessionId":"run_…297714000","snippet":"the
  quarterly report discusses the harbor crane budget"}]}`. After a second
  conversation was prompted with "the harbor crane quote arrived late", the same
  query answered two rows with the newer conversation first, in exactly the order
  `session/list` serves (`run_…321548000` then `run_…297714000`). A query matching
  nothing answered `{"items":[]}`; a blank query answered
  `"code":"gateway/bad-request","message":"session search query must not be
  empty"`; and an extra field answered `"code":"gateway/arguments-invalid",
  "message":"unexpected argument \"limit\""`.
- The page's search box now has a host answer: typing a phrase lists the
  conversations that contain it, newest first, and opening one lands on that
  conversation.
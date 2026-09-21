# ADR 0124: The Chunks and Errors the Console Reads Off a Stream

Status: accepted

## Context

Three facts the console renders were absent from the served stream:

- **The usage pill.** The client reads token accounting with
  `lastAssistantStreamChunk(stream, "usage")?.usage` -- from the **compacted stream**,
  not from the settlement record's own `usage` field -- and then maps it with
  `normalizeUsage`, which returns nothing at all unless `inputTokens` and
  `outputTokens` are both present. This host's settlement carried `usage`, so the
  counts were on the wire and the pill still never appeared.
- **The finish reason.** A provider ends a response with a `finish` chunk whose
  `reason.kind` is `stop` or `tool-calls`; the console distinguishes a finished answer
  from one that is waiting on tools. This host sent neither.
- **The tool call a cancelled run left open.** A run cancelled (or failed) while a tool
  was running never records that tool's result, so the console rendered the card as
  **still running, forever**. Upstream answers those calls itself -- `AbortError` /
  `ABORTED_BEFORE_DISPATCH`, and for the tool node `Interrupted` / `interrupted`, which
  the console's own classifier renders as `stopped`.

## Decision

- **The live attempt streams its usage and finish chunks.** `model.usage` becomes a
  `{type: "usage", usage: …}` chunk and `model.done` a
  `{type: "finish", reason: {kind: "stop" | "tool-calls"}}` chunk, with the reason taken
  from the durable `toolCallCount` -- upstream's own two reason kinds, nothing invented.
  The counts use the console's names (`inputTokens`, `outputTokens`, `totalTokens`),
  which is the mapping the settlement already made.
- **The compacted stream carries them too**, appended after the block timelines, as
  verbatim `{type: "chunk", time, chunk}` records. That is the shape the client's
  `expandAssistantStream` passes through untouched, so a console that *loaded* the
  session (rather than streaming it) reads the same pill and the same finish reason as
  one that watched it live.
- **A run that ends with calls outstanding answers them.** `run.cancelled` and
  `run.error` project one `tool/result` per unanswered call, immediately before the
  `turn/end`, carrying `isError: true` and
  `error: {name: "Interrupted", code: "interrupted"}`. This host cannot tell a call that
  never dispatched from one that was running, so every pending call is reported as
  interrupted -- the state the console renders as "stopped" -- rather than guessing
  which upstream code applies.

## Consequences

- Pinned by `TestAssistantTrackerStreamsUsageAndFinish` (the live chunks and both
  reasons), `TestProjectionKeepsTheDeltasInTheMessageStream` (the two trailing records
  in the compacted stream, with the console's token names) and
  `TestProjectionAnswersTheToolCallsAnInterruptedRunLeftOpen` (one result per pending
  call, before the close, for both a cancelled and a failed run).
- Live, with a real qwen-plus turn that ran a shell tool: the served
  `assistant/message.stream` expands -- under the console bundle's **own** extracted
  `expandAssistantStream` -- to `text-delta ×7, usage, finish`, `normalizeUsage` reports
  the pill renders, and a turn cancelled while `sleep 45` was running served
  `tool/result {"callId":…,"isError":true,"error":{"name":"Interrupted","code":"interrupted"}}`.
  The console's `validateSessionEventData` accepted all twelve records of that session.
- Still missing, and named as such: a failed tool (`tool.error`, or a non-zero exit)
  keeps only `isError: true`, because upstream has no execution-failure code to mirror
  and inventing one would be a label the console cannot translate. The tool-call
  *deltas* (`tool-call-delta`, and the `tool-call-chunks` compaction) are still not
  streamed: a tool call appears when `tool/call` is projected, so a live call's
  arguments do not type out the way a provider's would.
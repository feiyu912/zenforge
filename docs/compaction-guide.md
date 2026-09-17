# Compaction Guide

This guide covers ZenForge context management: how runs survive provider
outages with classified retry, and how they stay inside the model context
window with prune-and-summarize compaction. The design rationale is recorded
in [ADR 0023](adr/0023-context-management-retry-and-compaction.md); the
configuration keys live in [config-reference.md](config-reference.md).

## Why Compaction Exists

Long runs accumulate conversation history, tool declarations, and large tool
results. Two failure modes follow:

1. **Pressure** — the request approaches the model's context window and
   quality degrades or the provider rejects it.
2. **Overflow** — the provider rejects the request outright with a
   context-window error.

ZenForge handles both durably: compaction decisions are checkpointed before
their effects, so a resumed run replays the same compacted history instead of
re-deriving it. Resume semantics are covered in the
[Checkpoint/Resume Guide](checkpoint-resume-guide.md).

## The Two Triggers

| Trigger | When | Retain budget | Failure behavior |
| --- | --- | --- | --- |
| Pressure | Before each model call, when the estimated request reaches `ThresholdRatio` (default 0.8) of `Policy.ContextWindow` | `RetainRatio` (default 0.16) of the window, or absolute `RetainTokens` | Fail-soft: `compaction.error` event, the run continues |
| Overflow | After a model call fails with a canonical context-window error | 0 (maximal reduction: only the newest well-formed turn group is kept) | Forced: a failed recovery fails the run |

Pressure compaction requires a positive `ContextWindow`. With
`ContextWindow: 0` (the CLI default) pressure evaluation never fires, but
overflow recovery still works — the provider's own error is the trigger.

Pressure compaction runs at the start of each model call, before the first
attempt of that turn. One boundary allows `1 + CompactionRetries` passes
(default: 2), so a single summarization that lands above the threshold is
followed by one more attempt.

## Estimating Tokens

ZenForge has no provider tokenizer. The built-in `HeuristicEstimator` prices
text at `DefaultCharsPerToken` (4 Unicode code points per token) plus 4
tokens of framing overhead per message, and estimates three components of the
prospective request:

- the system-message prefix (host instructions, environment context, project
  instructions, skill catalog);
- the tool declarations (names, descriptions, JSON schemas);
- the conversation messages, including tool-call arguments.

Estimates drive pressure decisions only; provider-reported usage remains the
billing truth. Hosts can substitute any implementation of the `Estimator`
interface.

## What One Compaction Does

Compaction is prune-then-summarize:

1. **Prune** (opt-in, library level). `PruneToolResults` rewrites tool
   results larger than `Prune.ThresholdChars` (default 8192) into a head
   (default 4096) and tail (default 1024) around an explicit
   `... [N characters omitted by context compaction] ...` marker. Pruning is
   model-free, deterministic, and never touches the retained recent context.
   When pruning alone brings a pressure run back under the threshold, the
   compaction finishes with outcome `pruned` and no summary is produced.
2. **Choose the retain boundary.** `RetainBoundary` keeps the most recent
   messages that fit the retain budget, then backs off while the boundary
   lands on a tool-role message — a retained tool result is never separated
   from the assistant turn that called it, so the history stays well-formed
   for both provider protocols.
3. **Summarize the shadowed range.** The `Summarizer` receives the original
   task input plus a bounded transcript of the shadowed messages (defaults:
   80k characters total, 2000 per message, middle elided first). The built-in
   `ModelSummarizer` makes one no-tools `Generate` call with the
   codex-derived handoff prompt (`SummarizePrompt`) and rejects empty
   summaries.
4. **Replace and verify.** The shadowed range becomes one user message:
   `SummaryPrefix` framing followed by the summary, with `compaction.id` and
   `compaction.reason` in message meta. If the estimated token count did not
   actually drop, the whole transaction fails with `ErrNoReduction` rather
   than committing a pointless shadow; `ErrNothingToCompact` reports that no
   range can be shadowed at all.

!!! note "Pruning is durable"
    Pruned message content is checkpointed in its pruned form: the omission
    marker states this explicitly. Only summarized compactions append a
    `CompactionRecord`; a prune-only pass leaves its trace in the messages
    and events instead.

## Overflow Recovery

When a model call fails and `compaction.IsOverflowError` recognizes a
canonical context-window rejection — a typed `model.HTTPStatusError` with
status 400 or 413 carrying markers such as `context_length_exceeded` or
"prompt is too long", or an untyped error matching the same conservative
marker set — the agent:

1. supersedes the failed attempt (durable `superseded` status, linked to its
   replacement via `replacesId`/`replacementId`);
2. runs forced compaction with reason `overflow` and retain budget 0;
3. emits `model.retry` with reason `context_overflow` (no backoff delay —
   the retry is immediate);
4. retries the model call.

Recovery attempts per model call are bounded by `MaxOverflowRetries`
(default 1; 0 disables recovery). If forced compaction errors (for example
the summarizer model is down), the run fails with
`context overflow recovery failed: ... (provider error: ...)`. If compaction
cannot reduce the context (`ErrNoReduction`/`ErrNothingToCompact`), the
original provider error surfaces instead.

Overflow is owned by compaction end to end: `modelretry` classifies
context-window failures as `context_window_exceeded` but never retries them,
so an over-limit request is never blindly re-sent.

## Durable Records

Each summarized compaction appends a `harness.CompactionRecord` to
`RunState.Compactions`:

| Field | Meaning |
| --- | --- |
| `id` | Compaction identity (`cmpct_` + random hex), also in the replacement message meta |
| `step` | Run step at which compaction happened |
| `reason` | `pressure`, `overflow`, or `manual` |
| `tokensBefore` / `tokensAfter` | Conversation-only heuristic estimates around the rewrite |
| `shadowedMessages` | Number of messages replaced by the summary |
| `prunedResults` / `charsRemoved` | Pruning effect inside the shadowed range |
| `summarizerModel` / `summaryUsage` | Which model summarized and what it cost |
| `createdAt` | UTC timestamp |

Run-state validation bounds the history at `CompactionHistoryLimit` (64)
records and requires unique ids and in-range steps, alongside the equally
bounded model attempt history (see
[ADR 0007](adr/0007-run-state-schema.md)).

## Events

Compaction and retry are fully observable on the public event stream
([ADR 0002](adr/0002-public-event-contract.md)):

| Event | Payload highlights |
| --- | --- |
| `compaction.started` | `compactionId`, `reason`, `step`, `estimatedTokens`, `windowTokens`, `ratio`, `threshold` |
| `compaction.pruned` | `prunedResults`, `charsRemoved` |
| `compaction.summary` | `summary` (excerpted to 2000 runes), `summaryChars`, `summarizerModel`, `shadowedMessages`, `summaryUsage` |
| `compaction.done` | `outcome` (`pruned` or `summarized`), post-compaction `estimatedTokens`; `tokensBefore`/`tokensAfter` when summarized |
| `compaction.error` | `error` — fail-soft pressure failures land here |
| `model.retry` | `reason` (a `modelretry` code, or `context_overflow`), `retry`, `step`, `delayMs` (absent for overflow retries), `error` when a call failed |
| `model.superseded` | The attempt that retry or overflow recovery replaced |

The full summary text is durable in the checkpointed replacement message;
events only carry the excerpt.

## Interaction With Model Retry

Retry and compaction are separate opt-in subsystems that meet at the model
call:

- `Config.Retry` (`*modelretry.Config`) classifies failures into a closed
  taxonomy (`rate_limit`, `server_error`, `timeout`, `transport`,
  `empty_response`, `context_window_exceeded`, `auth`, `quota`,
  `request_invalid`, `canceled`, `unknown`). Unknown fails closed — it is
  never retryable.
- Retryable failures supersede the attempt, emit `model.retry` **before**
  the backoff wait (so a crash during the wait still explains the pause),
  then wait `InitialDelay * 2^n` capped at `MaxDelay` with ±`Jitter`.
  Provider `Retry-After` raises the delay as a floor capped at
  `MaxRetryAfterDelay`.
- `Config.StreamIdleTimeout` (positive) fails silent streams with a typed
  `model.StreamIdleError`, classified as `timeout`. Zero disables the
  watchdog.
- `context_window_exceeded` is never retried by `modelretry`; the agent's
  overflow path (above) owns it.

## Configuration

Library — both subsystems are off unless configured, and configuration
errors surface from `Stream` (nil stream, `configure compaction: ...` /
`configure model retry: ...`):

```go
agent := zenforge.New(zenforge.Config{
    Model: modelAdapter,
    Tools: tools,

    // Context window management. Summarizer is required; ModelSummarizer
    // reuses any model.Model. ContextWindow 0 keeps overflow recovery
    // without proactive pressure compaction.
    Compaction: &compaction.Config{
        Policy: compaction.Policy{
            ContextWindow: 128_000,
            // ThresholdRatio 0.8, RetainRatio 0.16, MaxOverflowRetries 1
            // come from WithDefaults when zero.
            Prune: compaction.PrunePolicy{Enabled: true},
        },
        Summarizer: compaction.ModelSummarizer{Model: modelAdapter, Name: "gpt-4.1"},
    },

    // Retry with defaults (5 retries, 500ms doubling to 10s, ±10% jitter,
    // Retry-After floor capped at 120s). Zero fields take defaults.
    Retry:             &modelretry.Config{},
    StreamIdleTimeout: 5 * time.Minute,
})
```

CLI — retry and the idle watchdog are on by default; compaction is wired
with the configured provider as summarizer, and pressure compaction switches
on as soon as a context window is known:

```json
{
  "model": {
    "contextWindow": 128000,
    "retry": {
      "enabled": true,
      "maxRetries": 5,
      "initialDelay": "500ms",
      "maxDelay": "10s",
      "jitter": 0.1,
      "streamIdleTimeout": "5m0s"
    }
  }
}
```

The same window can be set per invocation with `--context-window 128000`.
`maxRetries: 0` or `enabled: false` disables retry;
`streamIdleTimeout: "0s"` disables the watchdog. The prune policy is a
library-level knob and is not exposed in CLI configuration.

## Standalone Use

The `compaction.Compactor` is usable without the agent facade — hosts can
drive evaluation and compaction over their own `harness.MessageState`
histories:

```go
compactor, err := compaction.New(compaction.Config{
    Policy:     compaction.Policy{ContextWindow: 128_000},
    Summarizer: compaction.ModelSummarizer{Model: modelAdapter, Name: "gpt-4.1"},
})
if err != nil {
    return err
}

pressure := compactor.Evaluate(systemTokens, toolSpecs, state.Messages)
if pressure.ShouldCompact {
    result, err := compactor.Compact(ctx, compaction.Request{
        RunID:    state.RunID,
        RunInput: state.Input,
        Step:     state.Step,
        Reason:   compaction.ReasonManual,
        Messages: state.Messages,
    })
    if err != nil {
        return err
    }
    state.Messages = result.Messages
    state.AppendCompaction(result.Record)
}
```

`ReasonManual` exists for exactly this host-driven path; the agent itself
only produces `pressure` and `overflow` records.

## Limits

- Heuristic estimates are not provider token counts; a misconfigured or
  unusual tokenizer can make the 0.8 threshold fire early or late. The
  overflow backstop still catches the late case.
- Compaction rewrites run-state history: the shadowed originals leave
  `RunState.Messages`. What remains is the summary, the durable record, and
  the event trail.
- Summarization spends a model call per summarized compaction; its usage is
  recorded on the `CompactionRecord`, not on the run's attempt history.
- Compaction is per-run. There is no cross-run memory or shared summary
  store; each run compacts its own history.
- Pruning marks pruned content as permanently pruned in the checkpoint —
  the omitted middle is not recoverable from run state.

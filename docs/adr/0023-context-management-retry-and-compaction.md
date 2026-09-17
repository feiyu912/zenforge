# ADR 0023: Context Management Uses Classified Retry And Durable Compaction

Status: accepted

## Context

Long-running agents hit two distinct context problems. The first is transient
provider failure: rate limits, 5xx responses, transport resets, hung streams,
and occasionally empty completions. The second is the hard context-window
limit: sooner or later the conversation no longer fits, and the provider
rejects the request.

Both reference harnesses treat these as first-class durable runtime concerns
rather than adapter details. Codex retries with jittered exponential backoff,
honors rate-limit headers, watches idle streams, and compacts with a handoff
summary when the window is exceeded. DSH classifies failures into a taxonomy,
emits a durable retry marker before the backoff wait, and compacts
prune-first at step boundaries.

ZenForge already has the invariants this must fit into: a durable model
attempt lifecycle (`started` → `streaming` → `committed`, `interrupted`, or
`superseded`, linked by `replacesId`/`replacementId`), checkpoint-before-
decision ordering, and a public event contract. Retry and compaction must be
crash-consistent, replayable on resume, and observable as events. ZenForge
also has no reliable tokenizer: token counts must be heuristic estimates, and
provider-reported usage remains the billing truth.

## Decision

Two subsystems, each opt-in at the library level (`Config.Retry` and
`Config.Compaction` nil means off) and enabled by default in the CLI.

### modelretry: classified retry with backoff

The `modelretry` package owns a closed failure taxonomy: `rate_limit`,
`server_error`, `timeout`, `transport`, `empty_response`,
`context_window_exceeded`, `auth`, `quota`, `request_invalid`, `canceled`,
and `unknown`. Classification fails closed: unrecognized error shapes map to
`unknown`, which is never retryable, so programming errors stay
single-attempt instead of looping. `context_window_exceeded` is classified
for observability but never retried by `modelretry` — overflow recovery is
owned by compaction.

The schedule is `InitialDelay * 2^retriesDone` capped at `MaxDelay`, jittered
by ±`Jitter` (defaults: 5 retries, 500ms initial, 10s max, 0.1 jitter).
Provider `Retry-After` advice — parsed from delta-seconds or HTTP-date by the
model adapters into `model.HTTPStatusError.RetryAfter` — raises the delay as
a floor, capped at `MaxRetryAfterDelay` (default 120s); it never lowers the
computed backoff.

Each retry supersedes the in-flight attempt durably (checkpointed
`superseded` status plus `model.superseded` event), emits a `model.retry`
event with `reason`, `retry`, `step`, and `delayMs` **before** the backoff
wait so a crash during the wait leaves a durable record of why the loop
paused, and then starts a new attempt linked via `replacesId`. Empty
completions (no content, no tool calls) retry through the same machinery as
`empty_response`.

The stream idle watchdog (`Config.StreamIdleTimeout` > 0; CLI default 5m)
fails a stream that produces no events within the timeout with a typed
`model.StreamIdleError`, which classifies as retryable `timeout`.

### compaction: prune-then-summarize with durable provenance

The `compaction` package evaluates pressure at each model-call boundary with
a heuristic estimator (4 characters per token plus per-message overhead,
covering the system prefix, tool declarations, and conversation). When the
estimate reaches `ThresholdRatio` (default 0.8) of `Policy.ContextWindow`,
pressure compaction runs. It is fail-soft: errors emit `compaction.error`
and the run continues, because the provider's canonical overflow signal
remains the hard backstop.

When a model call fails with a canonical context-window error
(`IsOverflowError`: typed 400/413 status errors carrying known markers, or
conservative text matching), the agent supersedes the attempt, runs forced
overflow compaction with the retain budget set to zero (maximal head
reduction), and retries the call, emitting `model.retry` with reason
`context_overflow`. Recovery attempts per model call are bounded by
`MaxOverflowRetries` (default 1). This path works even when no context
window is configured: `ContextWindow: 0` disables pressure compaction but
keeps overflow recovery.

Compaction itself prunes first — `PruneToolResults` deterministically shrinks
oversized tool results to head/tail budgets around an explicit omission
marker, and a pruning pass alone can bring a run back under the threshold
(outcome `pruned`, no summary). If summarization is still needed, the
shadowed range is replaced by one user message framed with `SummaryPrefix`
and produced through a host-supplied `Summarizer` (the built-in
`ModelSummarizer` uses the codex-derived `SummarizePrompt` handoff shape).
`RetainBoundary` backs off tool-role messages so the retained history never
orphans a tool result from its assistant tool-call turn, and
`ErrNoReduction`/`ErrNothingToCompact` fail the transaction instead of
committing a pointless shadow.

Every completed compaction appends a `harness.CompactionRecord` (id, step,
reason, token counts, shadowed/pruned counts, summarizer model and usage,
timestamp) to `RunState.Compactions`, bounded by `CompactionHistoryLimit`
(64). Lifecycle events are `compaction.started`, `compaction.pruned`,
`compaction.summary`, `compaction.done`, and `compaction.error`; the summary
text inside events is excerpted to 2000 runes while the checkpoint retains
the full replacement message.

## Consequences

Benefits:

- transient provider failures recover without host code, respecting provider
  backoff advice;
- every retry and compaction is checkpointed before its effect, so resume
  replays a deterministic attempt chain;
- unknown errors fail closed: retry never amplifies programming mistakes;
- overflow recovery works without a configured window, and pressure
  compaction avoids most overflows when one is configured;
- token estimation stays dependency-free and replaceable through the
  `Estimator` and `Summarizer` interfaces;
- durable records and events explain every shadowed range after the fact.

Costs:

- heuristic estimates diverge from provider tokenizers, so the threshold is
  approximate by design;
- summarization spends an extra model call whose usage is recorded on the
  compaction record, not the run's model attempts;
- compaction rewrites run-state history: the shadowed originals leave
  `RunState.Messages`, and only the summary, record, and events remain;
- backoff waits add latency, bounded by `MaxRetries` and `MaxDelay`;
- two new configuration surfaces must be validated at construction
  (`configure compaction` / `configure model retry` config errors surface
  from `Stream`).

## Alternatives Rejected

### Retry Inside The Model Adapters

Adapter-local retry hides the attempt lifecycle from checkpoints, cannot emit
the durable pre-wait marker, and duplicates backoff logic per provider. The
agent-level loop keeps one attempt chain visible to resume and replay, which
is also where codex places its retry logic.

### Classify Unknown Errors As Retryable

Retrying unrecognized errors turns configuration and programming mistakes
into quota burn and latency. Failing closed matches the repository's
deny-by-default posture (ADR 0010).

### Let modelretry Retry Context Overflow

A blind retry of an over-limit request fails identically; recovery requires
rewriting history, which is compaction's job. Splitting ownership keeps the
retry classifier transport-focused and the compactor window-focused.

### Token-Accurate Counting With Provider Tokenizers

Per-provider tokenizer dependencies are heavy, still approximate for
other endpoints, and couple the core to adapter internals. The reference
harnesses also combine heuristic pressure with the provider's overflow
signal as the authoritative backstop.

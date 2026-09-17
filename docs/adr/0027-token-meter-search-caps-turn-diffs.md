# ADR 0027: Token Meter, Search Discovery Caps, And Turn Diffs Reuse Existing Seams

Status: accepted

## Context

Three reference behaviors close most of the remaining observability and
discovery gap identified in the reference parity plan (items C1, C14,
C21, C22).

Codex tracks provider usage per turn, parses rate-limit headers into a
`RateLimitSnapshot`, and exposes a zero-argument `get_context_remaining`
tool so the model can check its own budget instead of guessing when to
wrap up. DSH's tool-fs-search suite bounds discovery results the same
way its spill store bounds tool output: `glob` keeps the first 100 paths
in modification-time order and saves the complete sorted list, `grep`
keeps the first 250 matches with 2000-byte line previews, VCS
directories are never descended into, and every over-cap result carries
a footer explaining exactly what was cut and where the rest lives. Codex
also renders a per-turn unified diff of workspace mutations (TurnDiff)
under a 100ms in-process budget, and re-injects environment context
mid-run only when it actually changed (WorldState).

ZenForge already owns the seams each behavior needs: `model.Usage`
flows from adapters through committed attempts into durable
`UsageState`; tool-call metadata is cloned per call and never feeds
approval fingerprints; the Spill middleware (ADR 0026) already writes
private on-disk files the read tool can reach; Write/Edit tools see
both sides of every mutation; and the environment context is frozen
into run-state metadata at run start (ADR 0024). The question was
whether each behavior needs a new subsystem or can ride these seams.

## Decision

Ride the existing seams; add two small packages and no new durable
stores.

### Token meter: rate limits on `model.Usage`, budget on call metadata

`model.RateLimit` is the single normalized snapshot shape. Adapters own
normalization: `model.ParseRateLimits` understands both the OpenAI
`x-ratelimit-*` headers (Go duration or plain-second resets) and the
Anthropic `anthropic-ratelimit-*` headers (RFC 3339 resets), skips
unparsable values, and returns nil when a provider reports nothing. The
snapshot rides `model.Usage.RateLimits` through the existing plumbing:
stream events, committed attempts, and `harness.ApplyUsage`, which
persists the latest snapshot as `UsageState.RateLimits`
(`RateLimitState`, resets in milliseconds) — replace-on-observe, never
accumulated, additive within run-state version v1. Each observed
snapshot also emits a `model.ratelimits` event for live surfaces.

`get_context_remaining` (package `tools/contextinfo`, matching the
codex tool name and `tokens_left` output shape) stays stateless: before
dispatching any tool call, the agent computes the remaining budget —
configured context window minus the larger of the heuristic estimate
and the provider-measured prompt tokens of committed attempts — and
injects it as tool-call metadata under
`contextinfo.TokensRemainingMetadataKey`. The tool echoes the value or
reports null when no window is configured. Metadata is cloned per call,
never feeds approval fingerprints or grant matching, so a value that
changes every call cannot invalidate approvals.

### Search discovery caps and `workspace_glob`

`tools/workspace` adopts the DSH defaults verbatim: glob keeps 100
paths (`DefaultGlobMaxResults`), grep keeps 250 matches
(`DefaultGrepMaxMatches`) with 2000-byte rune-safe line previews
(`DefaultGrepMaxLineBytes`). Grep asks the workspace for one extra
match to learn that the cap cut anything, and the capped result carries
`capped` plus a footer telling the model to narrow the pattern.

A new `workspace_glob` tool completes the discovery suite: `**` spans
directory boundaries, a pattern without a slash matches basenames at
any depth, results are files only (never directories), hidden files are
included, VCS directories (`.git`, `.svn`, `.hg`, `.bzr`, `.jj`, `.sl`)
are never descended into, results sort newest-first, path syntax
(`..`, absolute patterns, bad globs) is rejected before any filesystem
access, and a visit budget plus depth cap bound the walk. The walk is
built on recursive `Workspace.List` calls, so it inherits the adapter's
root confinement for free instead of adding a second traversal path.

The Spill middleware's store logic was extracted into a reusable
`tool.SpillStore` (same 0700/0600 permissions and name sanitization),
and the CLI wires one store shared by the middleware and the search
tools: an over-cap glob saves its complete sorted list there and the
footer names the exact path. A missing or failing store degrades to a
plain "N more omitted" footer, never to a failed discovery call. Grep
does not spill: the workspace walk stops at the cap, so no complete
list exists to save — the footer says so by asking for a narrower
query instead.

### Turn diffs: capture at mutation, render at the boundary

`tools/workspace.TurnDiffStore` receives both sides of every successful
Write/Edit mutation (the Edit tool already holds the original; Write
reads it best-effort first, so a new file records as "created"). Within
one drain window the first original recorded for a path wins, so a file
edited three times in a turn diffs once, against the turn's starting
content. Capture is bounded: 2 MiB per file side, 32 MiB per run
window, 256 files; over-limit entries degrade to path-only notes.

The agent drains the store at each turn boundary (end of
`runPendingTools`) under a 100ms budget mirroring codex, and emits one
`turn.diff` event with per-file unified diffs; over-budget files
degrade to notes, and files whose net content is unchanged are omitted.
The diffs render through the new `diff/` package: classic Myers
shortest-edit-script with a per-round windowed trace (edit distance
capped at 1500) and a coarse prefix/suffix fallback above 4000
combined lines, so diffing is bounded in both time and memory. Diffs
live in the durable event log; nothing new is added to run state.

### Environment updates: diff-only re-injection

`maybeInjectEnvironmentUpdate` runs at every model-call boundary when
environment context is enabled: it re-renders the live environment
facts and compares them against the last render recorded in run-state
metadata (initially the frozen ADR 0024 baseline). Only on change does
it append a compact `<environment_update>` system message, persist the
new baseline under `zenforge.environment_update`, and emit
`environment.updated`. The frozen `<environment_context>` is never
rewritten, so resume still replays the run's starting snapshot, and
today the only fact that can drift mid-run is the UTC date — the seam
exists for future live facts (git branch, sandbox mode) to join.

## Consequences

Benefits:

- the model can check its own remaining budget and hosts get
  provider-reported rate limits as durable state and events, with
  adapter-owned header normalization and no new plumbing;
- discovery results are bounded and self-describing: every capped
  result says what was cut, and complete glob lists stay one read-tool
  call away through the shared spill store;
- every turn leaves an auditable unified diff of what it changed, which
  also feeds the future review/guardian mode (roadmap C13);
- the environment stays honest across midnight boundaries without
  breaking the frozen-context resume guarantee;
- all four behaviors are library-optional: nil stores and unset windows
  disable them cleanly, and run-state version v1 is untouched
  (additive optional fields only).

Costs:

- the turn-diff store holds mutation content in memory between turn
  boundaries (bounded per run, drained every turn);
- Write performs an extra best-effort read when turn diffs are enabled;
- glob walks through `List` calls, which is O(entries) per directory —
  acceptable at workspace scale and bounded by the visit budget, but
  slower than a ripgrep binary would be;
- `get_context_remaining` reports the agent-computed budget, not a
  provider-confirmed one: it can lag a mid-turn context change by one
  model call.

## Alternatives Rejected

### A DSH-Style Replay Token Meter

DSH's token-meter is a session-replay service pricing every surface
node per request. ZenForge's committed attempts already carry
provider-measured usage, and the pressure estimator already prices the
prospective request; a replay pricer would duplicate both for no new
capability.

### Rate Limits As A Separate Event-Only Channel

Skipping durable state and emitting events only would leave resumed
runs and non-streaming hosts without the latest snapshot. Riding
`UsageState` keeps one authoritative copy with zero schema churn.

### A Tool With Direct Run-State Access For `get_context_remaining`

Giving the tool a state handle would require per-run tool construction
or a state service, breaking the static-registry model. Call metadata
is already cloned per call, observable by any tool, and provably
fingerprint-neutral.

### Ripgrep-Backed Search Tools

DSH shells out to a packaged ripgrep binary. ZenForge has no binary
dependency story, and the workspace adapters already confine
traversal; List-based walking keeps confinement, portability, and the
adapter seam at a speed adequate for agent workloads.

### Snapshot-Store-Based Turn Diffs

The snapshot store keeps file metadata (hash/size/mtime) for CAS
checks, not content. Extending it to hold content would blur its
single-purpose contract and grow unboundedly; capturing at the
mutation site is exact, bounded, and drain-per-turn.

### Rewriting The Frozen Environment Context On Change

Mutating the persisted baseline would break the ADR 0024 guarantee
that resume replays the run's starting context. Appending an
`<environment_update>` message preserves both the original snapshot and
the change history in the durable message log.

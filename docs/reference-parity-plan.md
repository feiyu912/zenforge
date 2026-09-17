# Reference Parity Plan: ZenForge vs. deepseek-harness and codex

This document is the gap analysis behind the current upgrade wave. It maps
every capability observed in the two reference harnesses —
[deepseek-harness (DSH)](https://github.com/deepseek-ai/deepseek-harness) and
[codex](https://github.com/openai/codex) — onto ZenForge: what ZenForge already
had, what this wave implemented, and what remains as roadmap with a concrete
adoption sketch. It is a living planning artifact, not an ADR; the design
decisions themselves are recorded in `docs/adr/`.

Method: both references were inventoried capability-by-capability from their
sources (DSH `lib/` TypeScript surface, codex `codex-rs/` Rust workspace), then
each item was classified:

- **Shipped** — implemented in this upgrade wave, with tests.
- **Already had** — ZenForge had an equivalent or stronger mechanism before
  this wave.
- **Roadmap** — deliberately deferred, with a sketch of how it would land in
  the ZenForge architecture and a size estimate (S/M/L).

## Cross-cutting principles

Both references converge on the same engineering principles. ZenForge adopts
all of them; the right column points at concrete evidence.

| Principle | Meaning in the references | ZenForge evidence |
| --- | --- | --- |
| Fail-closed enforcement | Policy violations error out; unknown cases deny | `policy.ReviewCommand` denies non-allowlisted commands; `modelretry.Classify` marks unknown failures non-retryable; event-log write failures stop the run |
| Policy, not prompting | Safety lives in code paths the model cannot talk around | Tool argument validation, workspace roots, snapshot CAS, approval gates — none are prompt-level |
| Capability-boundary authorization | Crossing a boundary needs a human decision, once per boundary | Approval broker + grants store; sandbox escalation always prompts (ADR 0025) |
| Provenance on every record | Events/records carry ids, fingerprints, timestamps | Event payloads carry run/step/tool-call ids; `CompactionRecord` carries summarizer model + usage; attempt chaining via `ReplacesID`/`ReplacementID` |
| Durable marker before side effect | The log records intent before the wait/side effect | `model.retry` is emitted and checkpointed **before** the backoff sleep; approval requests checkpoint before pausing |
| Bounded resources | Every store, buffer, and loop has a cap | Attempt history 64, compaction records 64, instructions budget 32 KiB, spill 50 KiB inline, shell output caps, max steps, tool `MaxCalls` |
| Single owning writer | One component owns each mutation path | Runner owns `RunState`; checkpoint store owns persistence; adapters own provider protocol (ADR 0012) |
| Adapter-owned normalization | Core sees one typed contract | `model.Event`/`model.Message`; `model.HTTPStatusError` with `RetryAfter`; sandbox `Session`/`ExecuteResult` |
| Typed contracts | Errors and payloads are structured, not stringly | `tool.ErrInvalidArguments`, `approval.ErrRequired`, `model.StreamIdleError`, `compaction.ErrNoReduction`, typed events |
| Observability as events | Everything noteworthy is an event | ~40 event types incl. `compaction.*`, `model.retry`, `model.superseded`, `instructions.loaded` |

## Part A — Shipped in this wave

| # | Capability | Reference source | ZenForge implementation |
| --- | --- | --- | --- |
| A1 | Context compaction: pressure trigger at step boundary, prune-then-summarize, retain-boundary at tool-role edges, overflow recovery on provider context-window errors | DSH compaction package; codex `SUMMARIZATION_PROMPT`/`SUMMARY_PREFIX` + `CompactedItem` | `compaction/` package; `maybeCompact`/`runCompaction` in `agent.go`; durable `harness.CompactionRecord` (cap 64); events `compaction.started/pruned/summary/done/error`; guide: `docs/compaction-guide.md`; ADR 0023 |
| A2 | Model-call retry: failure taxonomy, exponential backoff with jitter, Retry-After honored as capped floor, attempt supersede chaining | DSH llm-retry; codex retry (200ms·2ⁿ·jitter, stream/request split) | `modelretry/` package; retry loop in `callModelDurable`; durable `model.retry` event emitted before the wait; `ModelAttempt` supersede chain; ADR 0023 |
| A3 | Stream idle watchdog | codex 300s stream idle timeout | `Config.StreamIdleTimeout` (CLI default 5m); typed `model.StreamIdleError` classified as retryable timeout |
| A4 | Empty-response retry | DSH empty_response classification | `modelretry.EmptyResponseFailure()`; supersede + retry with reason `empty_response` |
| A5 | Retry-After parsing from provider headers | codex/DSH honor 429/503 Retry-After | `model.ParseRetryAfter` (delta-seconds + HTTP-date); OpenAI/Anthropic clients capture the header into `HTTPStatusError.RetryAfter` |
| A6 | Exact-match file edit tool with ambiguous/not-found recoverable errors | DSH edit (FS_AMBIGUOUS_EDIT); codex apply_patch intent | `workspace_edit` in `tools/workspace`: exact `oldString`/`newString`, `replaceAll`, snapshot CAS guard, routed through the file-policy approval path |
| A7 | Hierarchical project instructions (AGENTS.md-compatible): root markers, root→cwd chain, override files, merged budget dropping broader scopes first, compat filenames | codex `agents_md.rs` (32 KB budget, override files); DSH agent-instructions | `instructions/` package; CLI defaults `AGENTS.override.md`/`AGENTS.md`/`ZENFORGE.md`/`CLAUDE.md`, marker `.git`, 32 KiB budget, `~/.zenforge/AGENTS.md` global; `instructions.loaded` event; ADR 0024 |
| A8 | Frozen environment context (`<environment_context>`: cwd, platform, date, mode, tools) + resume replays persisted prompting instead of rediscovering | codex `environment_context` tag + base_instructions persisted in rollout | `applyRunContext` freezes env context + rendered instructions into `RunState.Meta` (`zenforge.environment_context`, `zenforge.project_instructions`); `systemPrefixMessages` replays on resume; ADR 0024 |
| A9 | Sandbox escalation ladder: model-visible `sandboxPermissions`+`justification`, one-shot, strictly-wider, approval before spawn, denial markers | DSH bash escalation (`sandbox_permissions`, `[sandbox: …]` markers); codex denial classifier → escalate-to-approval retry | `tools/shell`: escalation args present only when confined; pairing validation; escalation never bypasses the allowlist but always prompts; namespaced escalation fingerprint rides the existing approval/grant/resume channel; approved call runs locally; ADR 0025 |
| A10 | Sandbox-denial classifier (exit 128+SIGSYS; quick-reject {2,126,127}; keyword dialects) with advisory markers | codex `is_likely_sandbox_denied`; DSH denial marker grammar | `policy.IsLikelySandboxDenied`; markers appended to confined failures with the escalation hint |
| A11 | ask_user tool: stable ids echoed in answers, structured options, multi-select, durable pause/resume, root-agent-only | DSH ask_user_question (id-echoed answers, DELEGATED_CALLER guard, non-durable waterfall) | `tools/askuser` + `approval/cli` question rendering; durable through the approval channel (operation `user.question`); answers via `approval.Decision.Payload` → new `approval.MetadataDecisionPayload`; subagents get a recoverable error; ADR 0026 |
| A12 | Tool-result spill store: 50 KiB inline cap, head/tail preview, private 0700/0600 on-disk store, fail-soft | DSH spill (50 KB cap, head/tail preview, 0700 tmp store, spill footers) | `tool.Spill` middleware; idempotent filename per run+call+arguments; CLI spills under `<workspace>/.zenforge/spill` so the read tool can reach it; ADR 0026 |
| A13 | Repeat-tool reminder at thresholds [3,5,8], escalating text, never blocking | DSH repeat-tool-reminder (consecutive identical calls, injected as plugin user message, reset on human input) | `tool.RepeatGuard` middleware, per-run streaks; deviation: reminder rides the tool result (durable in history) and does not reset on steering; ADR 0026 |
| A14 | Time/date context in the prompt | DSH time-context | `<today>` (UTC) inside the frozen environment context (A8) |
| A15 | Panic recovery + output/call bounds as composable middleware | DSH bounded tool budgets | Pre-existing `tool.RecoverPanic/MaxCalls/MaxOutputBytes/Timeout`; CLI now composes `[RecoverPanic, RepeatGuard, Spill]` as `ToolRuntime` |
| A16 | Token meter: provider rate-limit snapshots end to end + `get_context_remaining` tool | codex TokenUsageRecord/RateLimitSnapshot/`get_context_remaining`; DSH token-meter | `model.RateLimit` + `model.ParseRateLimits` (x-ratelimit-* and anthropic-ratelimit-* headers) riding `model.Usage`; durable `harness.RateLimitState` in `UsageState`; `model.ratelimits` event; the agent injects remaining tokens into tool-call metadata and `tools/contextinfo` reports them (`tokens_left` null without a configured window); ADR 0027 |
| A17 | Search discovery caps: `workspace_glob` tool + grep match caps and line previews + over-cap footers | DSH tool-fs-search (glob 100 paths newest-first with the complete list saved, grep 250 matches, 2000-byte line previews, VCS excludes) | `tools/workspace/glob.go` (`**` matcher, basename-at-any-depth patterns, List-based walk with visit budget); shared `tool.SpillStore` (extracted from the Spill middleware) wired as `SearchSpill`; ADR 0027 |
| A18 | Turn-diff tracker: per-turn unified diffs of workspace mutations | codex TurnDiff (100ms budget, path-list fallback) | `diff/` (Myers unified diff with bounded coarse fallback); `tools/workspace.TurnDiffStore` captured by Write/Edit at mutation time; the agent drains at each turn boundary under a 100ms budget into `turn.diff` events; ADR 0027 |
| A19 | WorldState diff-only environment re-injection | codex WorldState environment updates | `maybeInjectEnvironmentUpdate` re-renders live environment facts at each model-call boundary and appends an `<environment_update>` system message plus `environment.updated` event only on change; the frozen baseline (A8) is never rewritten; ADR 0027 |
| A20 | `present` tool: declare existing files as final deliverables with durable delivery records | DSH tool-present (1–8 files per call, regular-file validation, `deliverables/presented` session event) | `tools/present` validates paths through the workspace (missing files and directories rejected with DSH-style recoverable errors); the agent emits `deliverables.presented` with the tool-call id and validated files only for successful calls; ADR 0028 |
| A21 | Session titles: explicit override plus deterministic first-prompt fallback, sanitized and log-only | DSH session-title (OSC/CSI/ESC + control + bidi stripping, whitespace collapse, word and byte caps, never in the model surface) | `sessiontitle/` package mirrors `cleanTitleText`/`normalizeSessionTitle`/`fallbackSessionTitle`; `applyRunContext` freezes the title into run-state meta and the `session.title` event is published right after `run.started`; CLI `--title` and `agent.sessionTitle`; ADR 0028 |
| A22 | Observation policy completed: present-version CAS for existing files and observed-absence for new files | DSH fs-observation-policy (unseen/absent/present-version CAS) | `SnapshotStore.RecordAbsentForRun`/`AbsentObservedForRun`; `workspace_read` records absence when a read reports not-found; `workspace_write` refuses blind creates (`ErrSnapshotRequired`) and `workspace.ErrPathNotFound` now wraps `fs.ErrNotExist`; ADR 0028 |

## Part B — Already had (parity confirmed by this review)

| Capability | Reference analogue | ZenForge mechanism |
| --- | --- | --- |
| Durable runs: checkpoint every boundary, crash-safe resume, terminal replay | codex rollout JSONL (ordinals, writer lock, fork/revert); DSH session format v3 | `checkpoint/` (JSONL + SQLite), `harness.RunState` version `zenforge.run_state.v1`, fail-closed saves, resume tests across model/tool/approval boundaries |
| Append-only event log, fail-closed writes, multi-sink | DSH session format (58 event types, projections, SQLite FTS) | `eventlog/` (memory/JSONL/SQLite) + `Bus`/`FanoutStore`; ~40 typed events; `run.error` never emitted for cancellation |
| Approval brokers, durable inboxes, cross-run grants | codex session approval cache + durable execpolicy amendments; DSH permission presets | `approval/` brokers (CLI/always/deny/store), `PendingBroker`, memory/SQLite inboxes + `GrantStore` with fingerprint/rule scopes |
| Model-attempt lifecycle with supersede chaining and history cap | codex StreamError events + retry accounting | `ModelAttempt` Started→Streaming→Committed/Interrupted/Superseded, `ReplacesID`/`ReplacementID`, cap 64 |
| Deny-by-default shell with AST-level review | codex execpolicy + ParsedCommand classification; DSH bash budgets | `policy.ReviewCommand` + `safety/bashast`/`safety/bashsec` (wrapper commands, redirections, embedded scripts); ADR 0010 |
| Contained shell execution | codex Seatbelt/bwrap; DSH fs sandbox modes | `sandbox/` adapters (docker/fake/containerhub), no silent fallback (ADR 0019/0020); session reuse via metadata; OS-level modes are roadmap (C5) |
| Workspace tools with observation policy | DSH fs tools + observation policy (unseen/absent/present-version CAS); codex apply_patch per-path checks | `tools/workspace` read/list/grep/write/edit; `RequireReadBeforeWrite` + `SnapshotStore` SHA256 CAS; read/write roots; byte caps; symlink-escape rejection; `workspace.changed` events |
| Subagent orchestration with depth + fan-out limits | DSH subagents (depth 1, 1–16 children); codex collaboration modes | `subagent` runtime + `task`/`agent_invoke` tools; child checkpoints; cancelled children propagate as failures; ADR 0005/0017/0018 |
| Planner/todo + plan-execute preset | codex update_plan + plan mode; DSH todo | `planner` + `PlanningMode` presets; durable plan/execute stages; ADR 0013/0014 |
| Skills with progressive disclosure | DSH skills (layered providers, digest-gated catalog); codex skills ($mentions, implicit detection) | `skill` bundles + catalog prompt; ADR 0022 |
| MCP client integration | DSH MCP client (startup timeout, namespacing, elicitation); codex MCP (namespaces, read-only auto-approve) | `mcp/` adapter mapping MCP tools into the tool registry |
| HTTP/SSE server + run manager | DSH api/SDK/headless (thread.* JSONL); codex `exec --json` | `server/` + RunManager; access-control hook |
| Provider streaming adapters owning protocol details | codex model metadata + transport fallback; DSH adapter-owned normalization | `model/openai`, `model/anthropic` streaming; typed `HTTPStatusError` |
| Tracing/OTel as best-effort observability | DSH observability-as-events | `trace/` + otel exporter that cannot change run results |
| Layered CLI configuration with generated reference | codex layered config (user > project) | `zenforge init` defaults; `docs/config-reference.md` verified against generated defaults; profiles/admin layer are roadmap (C15) |

## Part C — Roadmap (deferred, with adoption sketches)

Deferred items are real gaps, sequenced by value-per-effort against the
ZenForge architecture. None blocks the shipped surface above.

| # | Capability | Reference source | Adoption sketch | Size |
| --- | --- | --- | --- | --- |
| C2 | Persistent PTY shell sessions (`unified_exec`: session ids, stdin writes, yield windows, head+tail buffers, proc caps, credential-scrubbed snapshots) + background jobs registry | codex unified_exec; DSH jobs/bash run_in_background | New `tools/exec` with a session registry keyed by run; reuse sandbox sessions; job ids + `job_output`/`job_kill` tools; scrub env in snapshots | L |
| C3 | Hooks protocol/engine (lifecycle events, matchers, command/MCP handlers, exit-2 block, deny>ask>allow, managed-only lockdown) | DSH hooks (claude-code/codex dialects); codex hooks engine (12 events) | `hooks/` package with a typed event enum, matcher config, and a broker that can veto tool calls before the invoker; compose as outermost middleware | L |
| C4 | Headless exec protocol (`--json` ThreadEvent stream, `--output-schema` structured final output) | codex exec --json/--output-schema; DSH headless api | CLI flag emitting the existing event stream as JSONL; final-answer schema validation via jsonschema before `run.done` | M |
| C5 | OS-level sandboxes (Seatbelt SBPL with read-only `.git`/config subpaths, bwrap+seccomp, Landlock) | codex sandboxes + writable-root hardening | New `sandbox/seatbelt` + `sandbox/landlock` backends implementing the existing `sandbox.Sandbox` interface; the escalation ladder (ADR 0025) already provides the model-visible half | L |
| C6 | apply_patch envelope tool with per-path writable auto-approval | codex apply_patch (Lark grammar, envelope) | `workspace_patch` tool reusing edit's CAS + policy path; parse the *** Begin Patch envelope; multi-file atomicity via staged writes | M |
| C7 | Web search/fetch tools with SSRF defense and untrusted-content framing | DSH web_search/web_fetch (SSRF defense, untrusted-content notice) | `tools/web` behind explicit config; deny private IP ranges after DNS resolution; wrap content with the untrusted-data notice | M |
| C8 | Goals (persisted same-session objective, continuation rounds, blocked-reason gating) and Ralph fresh-agent loops | DSH goals + ralph; codex memories/goals | `goals/` store keyed by session; a driver that re-invokes `Agent.Resume` per round with a bounded report; Ralph = fresh-run loop over a shared workspace | M |
| C9 | Workflow engine (JS orchestration script, agent()/pipeline()/parallel() hooks, schema-validated results) | DSH workflow tool | `workflow/` with an embedded JS runtime (goja), hooks mapping to subagent tasks, JSON-schema-validated returns | L |
| C10 | Session format v3 hardening: rollout ordinals, fork/revert, writer locks, migrations, projections | codex rollout (ordinals, fork/revert, writer lock); DSH session v3 (migrations, projections, FTS) | Add monotonic ordinals + a writer lock file to the JSONL event store; checkpoint fork = new run id seeded from a chosen seq | M |
| C11 | System-prompt registry with ordered sections, runtime contexts, `{{vars}}` fail-loud substitution | DSH system-prompt registry | Replace ad-hoc `systemPrefixMessages` with a section registry; each section typed + ordered; missing variables fail the run | S |
| C12 | Plan mode as a first-class collaboration mode (read-only tools until plan approval, exit_plan_mode) | codex plan mode/collaboration modes; DSH plan mode | `PlanningModePlanOnly` preset: file-policy denies writes; a `present_plan` tool routes through approval; on approval, switch mode durably | M |
| C13 | Review/guardian modes (second-model adversarial review of diffs/decisions) | codex review/guardian; DSH adversarial verification | Post-run middleware spawning a review subagent over `workspace.changed` paths + the turn-diff stream (A18) | M |
| C15 | Layered config: admin requirements layer, profiles, secret redaction, server-driven model metadata | codex requirements.toml > user > project, profiles, RedactedString | `configfile` precedence chain + `requirements` overrides that can only tighten; `RedactedString` type for keys in dumps | M |
| C16 | Image input + view_image tool; reasoning effort/summary with encrypted replay | codex view_image + reasoning support | `model.Message` parts for images; adapter passthrough; reasoning items stored encrypted in run state and replayed verbatim | M |
| C17 | Tool search / deferred tool loading for large registries | codex tool_search/defer_loading | Registry-level `Definitions(filter)`; a `tool_search` tool that activates deferred definitions per run | S |
| C19 | Webhook/schedule triggers and slash commands | DSH webhook/schedule, commands | Server endpoints creating runs from signed webhooks; cron scheduler reusing RunManager; commands as canned tasks | M |
| C20 | MCP server mode (expose ZenForge runs as an MCP server), elicitation, resources; MCP tool namespacing + read-only auto-approve | DSH MCP server; codex MCP namespaces/auto-approve | `mcp/server.go` exposing run tools; prefix `mcp__server__tool` on client-side names; auto-approve read-only annotations through the grants store | L |
| C23 | Memories (cross-run distilled learnings) | codex memories; DSH cross-session | `adapters/memory` extension: run-end distillation into scoped entries injected as instructions (A7 channel) | M |

## Verification

Everything marked **Shipped** is covered by Go tests runnable with:

```sh
env GOTOOLCHAIN=local go test ./...
```

Key suites: `compaction/`, `modelretry/`, `instructions/`, `diff/`,
`sessiontitle/`, `tools/askuser/`, `tools/contextinfo/` and
`tools/present/` (via root agent tests), `tools/shell/`,
`tools/workspace/` (glob, grep caps, turn-diff store, observation
policy), `tool/` (spill store + repeat guard), `approval/cli/`, and the
root-package agent tests (`compaction_agent_test.go`,
`retry_agent_test.go`, `context_agent_test.go`,
`context_meter_test.go`, `turn_diff_test.go`,
`environment_update_test.go`, `session_metadata_test.go`).
Docs claims are policed by `docs/links_test.go`,
`docs/schema_versions_test.go`, and `docs/mvp_validation_test.go`.

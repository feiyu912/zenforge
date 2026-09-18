# Changelog

## Unreleased

### Added

- Safe-boundary run steering through `Agent.Steer` and detached HTTP
  `ServeDetachedSteer`. Accepted messages become durable user turns after
  pending tools and before the next model call, emit `request.steer`, and map
  through the ZenMind projector. The built-in controller is owner-local;
  multi-worker routing remains application-owned.
- `examples/http-harness-agent`, a loopback-only production-shaped HTTP
  assembly with an environment-selected provider, Agent Skills, typed tool,
  Docker shell, durable SQLite stores, HITL, detached runs, and graceful
  shutdown. Authentication and tenancy remain application-owned.
- Optional `RunRegistryDeleter` terminal-record cleanup, implemented by the
  memory and SQLite registries and invoked by explicit `RunManager.Forget`
  without deleting durable events or checkpoints.
- Explicit `RunManager.RecoverStale` scans with batch limits, expired-lease and
  terminal filtering, normal claim fencing, and per-run recovery outcomes for
  host-owned recovery loops.
- Optional cross-manager cancellation requests through
  `RunCancellationRegistry`, implemented by the memory and SQLite registries
  with lease-fenced owner polling, pre-agent recovery checks, inherited pending
  cancellation, and legacy SQLite schema migration.
- Multi-replica deployment guidance covering supported storage topologies,
  per-operation routing, distributed cancellation, explicit crash recovery,
  side-effect idempotency, graceful rollout, and external acceptance gates.
- Bounded Agent Skill auxiliary resources with immutable bundle snapshots,
  digest/provenance metadata, symlink and path-escape rejection, and on-demand
  progressive disclosure through the existing `load_skill` tool.
- Optional detached run registry for `server/harnesshttp.RunManager`, with
  shared run claims, lease refresh, durable status/list lookup, cross-manager
  durable attach evidence, in-memory and SQLite registry implementations, and
  `NewRuntime` validation. The default remains process-local unless an
  application supplies a registry.
- Canonical `server/harnesshttp.NewRuntime` assembly and a single-process
  detached HTTP lifecycle with start, resume, status, list, replay-to-live attach,
  explicit cancel, `Last-Event-ID` reconnects, disconnect-independent
  execution, max-active admission, run timeout, terminal retention, and shared
  approval/FanoutStore wiring. Applications still own provider/auth, routes,
  durable storage, shutdown, and side-effect idempotency.
- Durable approval inbox interfaces, memory and SQLite pending stores, and a
  polling `approval.StoreBroker`. Agents register approval requests after the
  waiting checkpoint and before `approval.requested`; HTTP approval submission
  now targets `approval.Inbox`, commits before returning success, treats
  identical decision retries as idempotent, and reports conflicting decisions.
- ZenMind `BuildRun` host resolvers for catalog skills, tool overrides, and
  workspace/host-access policy, with fail-closed policy declarations and
  complete executable `zenforge.Config` propagation.
- `ApprovalEventBridge` correlation from real approval lifecycle events to
  awaiting wire values, including snapshot recovery, resumed replay, timeout
  answers, and no-answer reused resolutions.
- Run-bound `ProjectStrict` validation and projector state v2, retaining read
  compatibility for unbound v1 snapshots. These additions do not provide
  complete Chat Storage or platform transport/pending-awaiting wiring.
- Environment-based application model construction through `model/provider`,
  limited to OpenAI and Anthropic protocols with compatible custom base URLs.
- A built-in Docker sandbox with bounded execution, secure defaults, mounted
  workspace path mapping, and checkpoint-safe session restoration.
- A complete `examples/harness-agent` app and an independent consumer module
  covering Agent Skill progressive disclosure, typed tools, HITL approval, and
  Docker-backed shell execution. The example accepts `-skill-root` or
  `ZENFORGE_SKILL_ROOT` and ships a real `SKILL.md`.
- Validated filesystem Agent Skill catalogs and immutable bundles that expose
  descriptors first, then return instructions and individually requested
  auxiliary resources with digest and safe provenance via `load_skill`.
  Marketplace installation, entitlement, and lifecycle remain
  application/platform responsibilities.
- CI gates for race detection, vet, the independent consumer module, and a
  real Docker integration test.

### Integration Status

- `agent-platform` branch `codex/zenforge-engine-bridge` at `82ca4d3` now
  provides the downstream ZenForge engine bridge, feature-flag selector, and
  HTTP sync/async, SSE, WebSocket, approval, attach, and legacy fallback
  integration tests.
- `agent-platform` `main@f6d89da` restores the ZenForge bridge, selector,
  routing, initialization, and rollout documentation. Platform Go 1.26 tests,
  race tests, and the HTTP stream integration test pass. Deployed UI
  verification remains external acceptance. The opt-in
  `TestAdapterRunsAgainstRealContainerHub` covers a disposable live Hub session.
- ZenForge and the bridge require Go 1.26.x; older Go toolchains are unsupported.

### Extended history since v0.1.0

The full main-branch capability and hardening list, moved here verbatim from
the README's former Project Status section when the README became a landing
page. Curated summaries of the same ground live in the guides under
[`docs/`](docs/) and on the [documentation site](https://feiyu912.github.io/zenforge/).

- `server/harnesshttp` access control hook for auth and tenancy injection.
- `eventlog.Bus` and `eventlog.FanoutStore` for live multi-subscriber event fanout.
- `approval.PendingBroker` for simple process-local pending approvals, or
  `approval.StoreBroker` with `approval/memory` or `approval/sqlite.OpenInbox`
  for shared approval listing/submission across processes.
- Optional cross-run rule authorization through memory or SQLite
  `approval.GrantStore` implementations; no store preserves checkpoint-only
  behavior, while configured store errors fail closed.
- `adapters/zenmind`: run configuration mapping, chat JSONL projection, and a
  fail-closed routing helper for a host-owned feature flag.
- Platform sessions can provide a fully resolved prompt and strict conversation
  history, including tool-call turns, without duplicating history on resume.
- `adapters/memory`: scoped memory augmentation.
- Sub-agent resume reuses terminal children and continues existing child checkpoints.
- Child checkpoint backend failures stop before model execution, while missing checkpoints alone start fresh child runs.
- Cancelled child runs propagate as failed subtask results instead of false completion.
- Pure sub-agent agents advertise `task` and `agent_invoke` without requiring planning, and validate host limits before child state is checkpointed.
- Host-bounded nested delegation inherits child orchestration only below the configured maximum depth.
- Sub-agent context inheritance is explicit: task metadata, trusted parent context, host spec metadata, and runtime-owned fields have deterministic precedence.
- Child task file scopes are copied into `subagent.files`, and the configured workspace is retained through nested child configs.
- Active tool resume is covered through durable JSONL checkpoints.
- CLI run/resume are covered against local OpenAI-compatible streaming and durable JSONL checkpoints.
- CLI argument error output is covered for common command mistakes.
- `zenforge code <repo> <task>` binds workspace and shell execution to the resolved positional repository and rejects missing, nonexistent, or non-directory targets.
- Config reference is checked against the generated `zenforge init` defaults.
- Release notes version coverage is checked against `VERSION`.
- Durable schema version docs and flattened event contract docs are checked.
- CLI todo rendering is covered for typed plan/execute payloads.
- The code review example wires workspace snapshots and CLI approval for risky shell commands.
- The code review example README documents its approval prompt and effective read-only workspace posture.
- The code review example safety wiring is checked in the examples test suite.
- MVP validation evidence is checked against existing test and benchmark names.
- The docs test suite rejects platform brand coupling outside
  `adapters/zenmind` and rejects platform-module or `internal` imports inside
  that adapter.
- The SDK embedded example is run in tests without an API key.
- MVP scope now reflects the current CLI, adapter, resume, and example surface.
- Product roadmap MVP scope now reflects the current MCP, memory, sub-agent, and CLI inspection surface.
- Product roadmap resume scope now matches supported checkpoint-boundary resume.
- Max-step finalization drains the last pending tool calls before the final no-tool answer turn.
- MVP validation maps max-step final no-tool behavior to a concrete end-to-end test.
- Cancellation before model or tool execution persists a cancelled terminal checkpoint and event.
- Failure-mode docs and MVP validation describe durable cancellation semantics.
- Final no-tool turns fail clearly if a provider still returns tool calls.
- Failure-mode, resume, and MVP docs cover final-turn provider contract errors.
- Plan/execute checkpoints continue sequence numbers across stages and persist the terminal summary.
- Resume and MVP docs map durable plan/execute summaries to a SQLite end-to-end test.
- Served MCP runs are gated by `--allow-run`, never prompt on the protocol
  streams, and report refused tool calls with the run's outcome; `--run-timeout`
  bounds one run and a timed-out run still carries its id.
- An interrupted checkpoint save no longer masks its own failure: the JSONL
  store completes a durably-pending save under a context that cannot be
  cancelled, and the loop re-derives its checkpoint counter after a failed save.
- A background job is announced as terminal only after its output has been
  drained, so a foreground `Run` returns the output a command produced instead
  of racing the pipe copies (ADR 0058).
- `exec_command` can run a command on a pseudo-terminal (`pty: true`, with
  `rows`/`cols`): interactive programs, prompts, and full-screen tools behave
  as they would for a human, the merged stream arrives as the job's stdout,
  `write_stdin` drives it, and killing it signals the session's whole process
  group so a `SIGHUP`-ignoring child cannot keep the terminal open (ADR 0060).
  A bounded output buffer keeps both ends of a stream, so a flooded job still
  shows its first lines and any read that crossed the discarded middle reports
  how many bytes it skipped.
- `workflow/` runs the reference's orchestration scripts in-process (ADR
  0057): a JavaScript body with top-level `await`, `agent()`/`parallel()`/
  `pipeline()`/`phase()`/`log()` hooks, an `args` input, a bounded
  concurrency/total-agent/item cap, cooperative cancellation with a grace
  timer, and an enforced JSON-Schema subset for structured child results.
- The `workflow` tool wires those scripts to real sub-agent runs (ADR 0059):
  it is advertised wherever sub-agents are configured, each `agent()` call is
  a one-task sub-agent run through the existing orchestrator (same identity,
  nesting depth, options merge, and live `subtask.*` events), `phase()`/`log()`
  progress rides the subtask event carrier, `Config.WorkflowAgent` picks the
  worker and `Config.WorkflowLimits` bounds the run, a completed run returns
  the reference's text shape plus structured
  `{workflow, agentsStarted, stopReason, value}`, and a `provider`/`model`
  override is refused with a fatal `AGENT_START` rather than silently ignored.
- Plan/execute internal stages no longer leak terminal run lifecycle events or continue after stage failure.
- Planner spec, guide, and MVP validation document the single top-level run lifecycle.
- Plan/execute orchestration failures persist terminal checkpoints and resume without retrying completed work.
- Planner and failure-mode docs map durable orchestration failures to concrete resume tests.
- Plan/execute failure and cancellation paths fail closed when their terminal checkpoint cannot be saved.
- Workspace tools enforce file read/write roots before adapter access, return approval requests for policy exceptions, and reuse approved fingerprint/rule metadata.
- Workspace read-before-write snapshots are scoped by run and compare SHA256 in addition to size, mtime, and file type.
- Successful `workspace_write` calls emit `workspace.changed` and persist dirty paths in run state.
- Local workspace writes reject final symlink escapes and non-regular targets before writing.
- Complete platform-derived `safety/bashast` and `safety/bashsec` packages provide fail-closed AST, legacy-validator, wrapper-command, redirection, and embedded-script review; unsupported syntax requires approval or is denied.
- Failed plan/execute saves cannot mutate the last durable checkpoint through shared state metadata.
- Planner update failures are surfaced and checkpointed instead of emitting a false todo/task transition.
- Core checkpoint writes fail closed before model/tool progress or successful terminal events.
- Resume, failure-mode, and MVP docs map checkpoint fail-closed behavior to concrete tests.
- Checkpoint loads and resume fail closed on unknown run-state version, phase,
  or mode while retaining legacy empty version/mode compatibility.
- Event-log sequence and append failures stop execution and surface a live `run.error` instead of publishing unrecorded progress.
- Trace exporters remain best-effort platform observability and cannot change the harness result.
- Architecture package layout is aligned with the current repository.
- Historical API sketch is labeled and current guides are prioritized.
- README Quick Look and architecture snippets use current store/interface names.
- User-facing guides no longer present themselves as drafts and use current tool, shell, and sandbox APIs.
- Approval guide examples use neutral core decisions, with platform payload mapping kept at adapter edges.
- CLI workspace writes require a fresh read snapshot by default.
- Quickstart and config reference document the CLI workspace write snapshot default and configurable file roots.
- CLI workspace read/write byte limits from config are applied at runtime.
- MVP validation maps CLI workspace byte-limit enforcement to a concrete test.
- CLI workspace read/write roots from config are applied to runtime file policy.
- Code-review and repo-refactor examples now wire explicit workspace file roots and read-before-write snapshots.
- CLI config rejects invalid shell timeout durations instead of silently falling back.
- Config reference and MVP validation document invalid shell timeout handling.
- CLI config rejects invalid agent planning modes instead of disabling planning silently.
- Config reference and MVP validation document invalid planning mode handling.
- CLI config rejects invalid approval modes before building the runtime.
- Config reference and MVP validation document invalid approval mode handling.
- CLI config rejects invalid model providers and checkpoint store types before runtime setup.
- Config reference and MVP validation document invalid provider/checkpoint handling.
- CLI config rejects negative agent, workspace, and shell limit values.
- Config reference and MVP validation document negative CLI limit handling.
- HTTP approval submit bad JSON and invalid decisions are covered.
- MVP validation maps HTTP approval bad request handling to a concrete test.
- HTTP event replay rejects invalid `afterSeq` and `limit` query values.
- MVP validation maps HTTP event replay query validation to a concrete test.
- HTTP live event streaming rejects invalid negative buffer configuration.
- MVP validation maps HTTP live buffer validation to a concrete test.
- HTTP handler method guards are covered across run, resume, event, live event, and approval endpoints.
- MVP validation maps HTTP handler method guards to a concrete test.
- HTTP resume distinguishes invalid POST JSON from a missing run id.
- Approval without a broker closes the current stream at a resumable waiting checkpoint; `Run` returns `approval.ErrRequired`.
- Approval abort decisions persist a cancelled terminal checkpoint instead of a generic failed run.
- `Agent.Run` returns cancellation and deadline terminal events as matching Go errors.
- Approval run/rule grants survive checkpoints and resume, while mismatched scope keys require a new decision.
- Harness-owned approval run/tool identity overrides tool-provided values, and mismatched broker decision IDs fail closed.
- Generic approval middleware binds decisions to the exact request and scope key before retrying a tool; aborts expose both `approval.ErrAborted` and `context.Canceled`.
- MVP validation maps HTTP resume invalid JSON handling to a concrete test.
- Sandbox checkpoint state binds sessions to the exact run/subtask scope.
- Sandbox close is best-effort and cannot replace a successful command result.
- Container Hub transport deadlines map to stable `sandbox_timeout` errors.
- JSONL event/checkpoint stores use cross-process file locks, reject unsafe
  run IDs, and recover interrupted checkpoint saves from a pending journal.
- Shell output capture and Container Hub response reads are bounded in memory.
- Tool retries require `tool.MarkRetryable`; permanent and policy errors run once.
- `ToolArgumentRedaction` removes configured nested keys from durable `tool.call` events without changing tool input.
- Tool call budgets are isolated by run, and output truncation preserves valid UTF-8.
- Trace metadata enrichment.
- A hardening test suite and a failure-mode guide.
- The root Agent loop is now an adapter around `harness.Runner`; runner-level tests cover text completion, tool continuation, and oneshot finalization directly.
- Production Agent checkpoint creation and `checkpoint.created` payloads are
  shared across normal, planner, terminal, and cancellation paths; `recorder`
  remains a low-level ordered-write helper rather than the Agent lifecycle.
- ZenMind adapter wire goldens are pinned to `agent-platform@1893edb5`, while
  downstream engine/feature-flag/HTTP/SSE/WS/approval/attach integration is
  tested on `agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3`.
  Platform `main@f6d89da` restores the ZenForge bridge, selector, routing,
  initialization, and rollout documentation. The existing `agent-webclient`
  focused protocol tests and production build pass; production deployment
  acceptance remains external.
- ZenMind run assembly rejects missing or typed-nil models and explicitly
  declared unavailable tools, while preserving undeclared, explicitly empty,
  and legacy tool-list semantics.
- ZenMind host resolvers assemble skills, tool overrides, and workspace access;
  approval events correlate to awaiting wire with snapshot recovery; and
  `ProjectStrict` enforces one run with v2/v1 state compatibility. This remains
  adapter behavior, not complete Chat Storage or platform wiring.
- `compaction/`: context management modeled on DSH and codex — pressure
  compaction at step boundaries (summarize shadowed history with a retain
  budget, with optional library-level tool-result pruning via `Policy.Prune`),
  forced overflow recovery on provider context-window errors, durable
  `CompactionRecord` history, and `compaction.*` events. Opt-in via
  `Config.Compaction`; the CLI always wires it, so overflow recovery works
  without a configured window and pressure compaction activates once
  `--context-window` / `model.contextWindow` is set.
- `modelretry/`: fail-closed model-failure taxonomy (rate limit, server,
  timeout, transport, empty response, context window, auth, quota), exponential
  backoff with jitter, capped Retry-After support parsed from OpenAI/Anthropic
  response headers, superseded model-attempt chaining, durable `model.retry`
  events emitted before the wait, and a stream idle watchdog
  (`Config.StreamIdleTimeout`, typed `model.StreamIdleError`).
- `workspace_edit`: exact-match file editing with `replaceAll`, recoverable
  ambiguous/not-found errors, and the same read-before-write snapshot CAS as
  `workspace_write`, routed through the file-policy approval path.
- `instructions/`: hierarchical AGENTS.md-compatible project instruction
  discovery (root markers, root-to-cwd chain, override files, compat
  filenames, user-global scope, 32 KiB merged budget dropping broader scopes
  first) plus a frozen `<environment_context>` block; both persist into
  `RunState.Meta` at run start so resume replays the exact prompting, with an
  `instructions.loaded` event.
- Shell sandbox escalation ladder: model-visible `sandboxPermissions` +
  `justification` arguments exist only while the shell is confined, never
  bypass the command allowlist, always require a fresh approval under a
  namespaced fingerprint, and run the approved call locally once; a
  conservative denial classifier (`policy.IsLikelySandboxDenied`) appends
  DSH-grammar sandbox-denial markers and the escalation hint to confined
  failures.
- `tools/askuser`: durable ask-user tool with stable per-question ids echoed
  in answers, structured options, multi-select, and a root-agent-only rule;
  answers ride `approval.Decision.Payload` through the new
  `approval.MetadataDecisionPayload` key, and the interactive CLI broker
  renders questions and collects answers.
- `tool.Spill` middleware: oversized tool output moves to a private 0700/0600
  on-disk store with a UTF-8-safe head/tail preview and pointer inline
  (50 KiB default cap), fail-soft to bounded truncation when the store is
  unavailable; the CLI spills under `<workspace>/.zenforge/spill` so the read
  tool can reach full outputs.
- `tool.RepeatGuard` middleware: consecutive identical tool calls per run are
  counted at thresholds 3/5/8 and answered with escalating in-result
  reminders; the real result is never blocked or replaced.
- CLI composes `RecoverPanic → RepeatGuard → Spill` as its tool runtime and
  gained `--context-window`, `model.retry.*`, `agent.environmentContext`, and
  `agent.projectInstructions` configuration, all covered by the generated
  config reference.
- `docs/reference-parity-plan.md` maps every observed DSH/codex capability to
  its ZenForge status (shipped, already had, roadmap) with adoption sketches,
  backed by ADRs 0023–0027 and the Compaction Guide.
- Token meter (ADR 0027): adapters normalize provider rate-limit headers
  (`x-ratelimit-*`, `anthropic-ratelimit-*`) into `model.Usage.RateLimits`,
  persisted as `UsageState.RateLimits` and emitted as `model.ratelimits`
  events; the `get_context_remaining` tool reports the live remaining budget
  the agent injects into tool-call metadata, or null without a configured
  context window.
- `workspace_glob` discovery tool: DSH-style `**` matcher, basename-at-any-
  depth patterns, VCS excludes, newest-first ordering, visit budget; plus the
  DSH search caps — glob keeps 100 paths inline with the complete sorted list
  saved to a shared `tool.SpillStore`, grep caps at 250 matches with
  2000-byte rune-safe line previews, and every over-cap result carries an
  explicit footer.
- Turn-diff tracker (codex TurnDiff): Write/Edit mutations are captured in
  `tools/workspace.TurnDiffStore` and rendered at each turn boundary under a
  100ms budget as unified diffs (new `diff/` package, bounded Myers with
  coarse fallback) in `turn.diff` events; oversized or over-budget files
  degrade to path-only notes and unchanged files are omitted.
- Diff-only environment re-injection (codex WorldState): live environment
  facts are re-rendered at each model-call boundary, and only a change
  appends an `<environment_update>` system message plus an
  `environment.updated` event; the frozen `<environment_context>` baseline
  is never rewritten, so resume semantics are unchanged.
- `present` tool (DSH tool-present): the model declares existing workspace
  files as final deliverables; a successful call emits a
  `deliverables.presented` event carrying the tool-call id and the validated
  workspace-relative paths, while missing files and directories are rejected
  with recoverable errors before anything is announced.
- Session titles (DSH session-title): OSC/CSI/ESC sequences, control
  characters, and bidirectional marks are stripped and titles are capped on
  rune boundaries; an explicit `--title`/`agent.sessionTitle` wins, otherwise
  the first eight words of the task input become the deterministic fallback.
  Titles live in run-state meta plus the `session.title` event and never
  enter the model prompt.
- Completed write observation policy (DSH fs-observation-policy): with
  read-before-write enabled, existing files need a fresh same-run snapshot and
  new files need a same-run observed absence (a read that reported
  not-found), so a blind create is refused; `workspace.ErrPathNotFound` now
  also satisfies `errors.Is(err, fs.ErrNotExist)`.
- MCP server mode (DSH MCP server): `adapters/mcp/server.go` exposes
  ZenForge as an MCP server over any reader/writer pair — `initialize` with
  version negotiation, `tools/list` with read-only annotations, and
  `tools/call` with the protocol's own error taxonomy (a tool failure is
  `isError`, a malformed request is a JSON-RPC error, a notification is
  never answered). The client's stdio framing was corrected to the spec's
  newline-delimited JSON while still reading `Content-Length` headers, and
  remote tools are namespaced `mcp__<server>__<tool>` with their read-only
  hints read into the definition so an approval decision can use them.
  `zenforge mcp-server` serves ZenForge itself the same way, exposing
  `zenforge_runs`, `zenforge_run_status`, and `zenforge_version` as read-only
  tools, `zenforge://runs` and `zenforge://runs/{runId}` as read-only
  resources, and the workspace's commands as prompts (`prompts/get` renders one
  as inert text, with inline shell disabled), none of which needs an operator
  grant. A client that supplies an MCP progress token gets
  `notifications/progress` while `zenforge_run` works through a run; without
  one it receives exactly the frames it did before
  tools (status answers for one run: live state for a run this server started,
  the durable summary for a run this install recorded, and `unknown`
  otherwise). `--allow-run` adds `zenforge_run`, which starts a run in the
  configured workspace and returns its answer, id, and status; with
  `detach: true` it instead returns the id as soon as the run has started, so
  a task longer than one call can hold keeps running on the server (bounded by
  `--run-timeout`), is polled through `zenforge_run_status`, and can be stopped
  early with `zenforge_run_cancel`. The tool is
  advertised without a read-only hint so the calling client asks its own
  operator, it does not exist without the grant from this host's operator, and
  inside the served run approval-gated tools are allowed when the calling
  client advertises MCP elicitation (the gate asks it for a boolean decision and
  grants that one call), while the default `prompt` falls back to a refusal —
  reported with the run's outcome — for a client that cannot answer, and
  `--approve always` skips the question entirely.
- Configured MCP servers (DSH/codex MCP client): a `mcpServers` config section
  starts stdio servers and exposes their tools as `mcp__<server>__<tool>`,
  with `deferred: true` opting a server into `tool_search` activation. The
  section is validated before anything is spawned and a server that cannot
  start fails the command, because the file is the operator's own. A call the
  server did not declare read-only goes through the approval broker with the
  reference's rule (destructive asks, read-only runs, absent hints ask), and
  the request carries a per-tool rule key plus an argument fingerprint so a
  broad grant cannot be replayed for a different payload. Both time bounds are
  per server (`startupTimeout`, covering `initialize` and `tools/list`, and
  `toolCallTimeout` per call), so a cold container or a long-running remote
  tool says so in its own entry instead of moving the default for every
  server. Server processes get
  the ambient environment minus credential-shaped names, are owned by the
  command that started them, and are drained on every exit path — including a
  repeated schedule, which drains per firing. The stdio client now dispatches
  responses by id, so a declared per-call timeout really fires and the
  connection survives it, and `Close` waits for both the process and the
  reader goroutine.
- Images and reasoning (codex `view_image`, reasoning replay): the
  `view_image` tool shows the model an image from the workspace — confined
  like any other file read, format verified from magic bytes, and carried on
  the tool result so it is replayed on later turns and survives a resume.
  Provider reasoning is captured with its signature and replayed verbatim
  where the provider requires it (Anthropic thinking blocks), streaming on
  its own `model.reasoning` event so it is never mistaken for answer text.
- Canned commands and schedules (DSH commands/schedule): `--commands
  <dir>` loads `*.md` prompt templates invoked as `/name args`
  (subdirectories namespace them as `/git:commit`), with `$ARGUMENTS`,
  `$1..$9`, workspace-confined `@file` includes, and `!`cmd`` inline shell
  that requires an explicit `run-bash: true` and still goes through the
  shell policy. `--schedule 'every 1h'` (or a cron subset) repeats a task
  in-process, surviving a failed firing and stopping cleanly on
  cancellation.
- A guardian review (`--review report|enforce`): one adversarial model call
  over the finished run — task, answer, changed files, real unified diffs
  read back from the run's turn-diff events, commands, failures. Report mode
  records findings and finishes; enforce mode sends a `request_changes`
  verdict back as the agent's next instruction, bounded by the same
  three-refusal limit as Stop hooks. A malformed verdict is an error, never
  an approval, and a broken reviewer can neither block nor approve.
- Durable cross-run memories (codex memories): `--memory <dir>` keeps
  learnings in two readable markdown files — an append-only raw file and a
  consolidated summary that is injected as instructions (frozen into run
  state, so a resume replays it). Identity is the content, so the same
  learning found twice is one memory; `--memory-scope` decides visibility.
  `--memory-distill` adds one model call per finished run to extract new
  learnings; a distillation failure is reported and never costs the user
  their answer.
- Lifecycle hooks in the agent loop: `SessionStart` and `UserPromptSubmit`
  run once at run start, their context is frozen into durable run state and
  rendered as a system section, and a block fails the run before any model
  call; a `Stop` hook can refuse to let the agent finish, sending it back to
  work with the hook's reason as its next instruction, bounded at three
  refusals per terminal path so a hook that never relents cannot hold the
  agent hostage.
- Lifecycle hooks (codex `hooks` crate): `--hooks <file>` runs user
  commands at `PreToolUse`/`PostToolUse` (wired as tool middleware, so a
  blocking hook prevents the call and `updatedInput` rewrites it) and the
  engine also implements `SessionStart`/`UserPromptSubmit`/`Stop`. A JSON
  payload goes to stdin, exit 0 parses a JSON decision, exit 2 blocks with
  stderr as the reason, and a malformed decision is a failure rather than a
  silent allow; failures fail open unless the hook sets `failClosed`.
- Model-facing job tools: `--jobs` (or `agent.jobs`) registers
  `exec_command` (foreground or `background=true`), `write_stdin`,
  `job_output` (offset reads, `waitMs` long-polling, dropped-output flags,
  and a hint telling the model how to wait when a job is still running),
  `job_list`, and `job_kill`. `job_list` is read-only, so plan mode allows
  it while the state-changing tools stay refused.
- Background jobs (codex `exec_command`/`write_stdin`): `jobs` starts a
  long-running command detached from the tool call, reads each stream from
  an absolute offset so polling never re-reads or silently skips bytes
  (dropped bytes are reported, not hidden), feeds interactive programs
  through stdin, and stops jobs by kill or timeout. The job limit fails
  loudly instead of queueing behind a full slot.
- Landlock + seccomp sandbox backend: `sandbox/linuxsandbox` binds the two
  in-process layers into a `sandbox.Sandbox` by re-invoking this binary as
  `zenforge linux-sandbox --policy <json> -- <command>`; the helper decodes
  the policy (rejecting unknown fields), probes the Landlock ABI, plans
  both layers before applying either, then applies and execs. The policy
  JSON and its SHA-256 are recorded on the session, and a policy Landlock
  cannot express is refused when the backend is built. Select it with
  `--sandbox landlock`; the helper is hidden from the usage text but is a
  real subcommand.
- Seccomp network filter (codex `linux-sandbox/src/landlock.rs`, seccomp
  section): `sandbox/seccomp` plans the classic BPF program as data —
  architecture guard (a foreign ABI is killed, otherwise the wrong syscall
  table fails open), unconditional denies for the network syscalls,
  `ptrace`, and `io_uring` (which can create sockets without `socket(2)`),
  `AF_UNIX`-only `socket`/`socketpair`, `recvfrom` deliberately allowed for
  subprocess tooling, and `EPERM` for everything matched. A test-local BPF
  interpreter verifies the program's semantics rather than only its shape,
  and `Available` probes `/proc/sys/kernel/seccomp/actions_avail` because a
  filter cannot be uninstalled.
- Landlock planner (codex `linux-sandbox/src/landlock.rs`):
  `sandbox/landlock` derives the access mask from the probed ABI (`REFER`
  from 2, `TRUNCATE` from 3, `IOCTL_DEV` from 5), grants whole-filesystem
  read plus read-write on the declared roots (or only the declared read
  roots when `FullDiskRead` is false), and handles every right the ABI
  supports so an unhandled right can never be mistaken for a denied one. A
  read-only carve-out inside a writable root is rejected with
  `ErrUnsupportedCarveOut`, because Landlock unions matching rules and has
  no deny rule — silently ignoring it would grant more than the policy
  promised. The planner is tested on every platform; the applier
  (`create_ruleset`/`add_rule`/`restrict_self` + `Exec`) is build-tagged
  and cross-compiled for Linux.
- Sandbox wiring: `--sandbox <none|seatbelt|bwrap|docker>` (or
  `shell.sandbox.backend`) confines the shell tool in the chosen backend and
  keeps the session open so later calls reuse the layout, with
  `--sandbox-root`, `--sandbox-allow-network`, `--sandbox-restricted`,
  `--sandbox-image`, `--sandbox-protected`, and `--sandbox-timeout` for the
  details. An unavailable backend fails with `sandbox_unavailable` instead
  of running unsandboxed. `--plan` and `--goals` are also real flags now,
  matching the documented configuration keys.
- Linux bubblewrap sandbox (codex `linux-sandbox/src/bwrap.rs`):
  `sandbox/bwrap` expresses the sandbox as a bubblewrap argument list — a
  read-only root (or a tmpfs root plus the approved read roots), the
  declared writable roots bound shallowest-first, `.git`/`.zenforge` bound
  read-only over themselves, user/pid/ipc namespaces, all capabilities
  dropped, no network unless requested, and a fresh `/proc`. Because the
  policy is data, it is auditable and testable off Linux; missing writable
  roots are dropped and missing protected paths are skipped, since
  bubblewrap cannot bind a target that does not exist. Seccomp filtering
  (a separate helper process in the reference) is not applied yet.
- macOS Seatbelt sandbox (codex `sandboxing/seatbelt.rs`): `sandbox/seatbelt`
  generates an SBPL profile per session — closed by default, the reference's
  base/platform/network policies ported verbatim, write access only to the
  declared roots, `.git` and `.zenforge` pinned read-only inside every
  writable root (as an exclusion on the allow rule, since a deny there would
  match the complement), and paths passed as `-D` parameters so a hostile
  path cannot rewrite the policy. Paths are canonicalized because the kernel
  matches resolved vnodes, and each writable root's ancestors stay readable
  so tools can resolve their working directory. Network is denied unless
  requested, and the adapter refuses to run at all off darwin.
- Goals and Ralph (DSH goal domain + `dsh-tool-ralph`): `--goals`
  registers `create_goal`/`get_goal`/`update_goal`, whose lifecycle
  (revision-checked transitions, round budget, and the rule that a goal
  may only be reported blocked after the same condition survives three
  consecutive rounds) lives in `goals/`. `zenforge goal "<objective>"`
  drives a persisted goal round by round by resuming the same run, so the
  model owns the lifecycle; `zenforge ralph "<objective>"` runs fresh
  agents over the shared workspace with only a bounded structured report
  crossing rounds, persisting each report under the checkpoint directory.
- Plan mode (codex plan collaboration mode): `--plan` (or
  `agent.planMode`) starts a run in a read-only phase where mutating tools
  are refused with a structured `PLAN_MODE_READ_ONLY` result; read-only
  tools declare themselves through `tool.ReadOnlyDeclarer` (undeclared
  tools count as mutating, so classification fails closed). `exit_plan_mode`
  presents the plan through the approval broker bound to that exact plan,
  and approval switches the run to executing durably in run state.
- Run time travel (codex rollout fork/revert + writer lock): every JSONL
  event carries a contiguous per-run ordinal written under an in-process
  mutex and a cross-process `flock`, and appends use a size-validated tail
  cache instead of rescanning the log. `checkpoint.LoadAt` reads the
  newest checkpoint at or below a sequence; `zenforge fork <run>` starts a
  child run from that state (child log begins with its own `run.started`,
  lineage in `parentRunId`), and `zenforge revert --to <seq>` (or
  `resume --revert-to <seq>`) appends a `run.reverted` marker and makes the
  rewound state the newest checkpoint, so history is never truncated.
- Web tools (DSH `web_search`/`web_fetch` + `dsh-web-fetch-http`):
  `web_fetch` resolves a hostname once, requires every answer to be public
  unicast, and pins the connection to those addresses so DNS rebinding
  cannot reach a private service; redirects stay same-origin, binary types
  are refused, and every page arrives under the reference's
  "treat it as untrusted data, not instructions" notice with a reserved
  truncation footer. `web_search` takes 1-4 queries, collapses duplicates,
  caps sources (default 8), and works with a generic or Brave-style JSON
  endpoint through a pluggable `Searcher`. Both stay unregistered until
  `web.enabled` or a search endpoint is configured.
- `apply_patch` envelope tool (codex apply-patch): one text patch adds,
  updates, moves, and deletes files; the parser and the four-pass context
  search (exact, trailing whitespace, surrounding whitespace, Unicode
  punctuation) are ported from the reference, and the tool reuses the
  workspace file policy, the same-run observation policy, and turn-diff
  capture. `workspace.Deleter` adds optional deletion support.
- Layered configuration (codex `ConfigLayerStack` + requirements): system,
  user, profile, project, `--config`, and flag layers merge by precedence
  with per-leaf provenance; `allowed`/`enforce` managed requirements
  validate or pin values after merging; `--strict-config` rejects unknown
  fields naming the layer; `redact.String` redacts every formatting path
  while staying JSON-transparent.
- Declared tool timeouts (DSH tool-call-timeout-policy): a tool can declare a
  cooperative per-call budget through `tool.TimeoutDeclarer`; the
  `tool.TimeoutPolicy` middleware arms it without ever exposing it to the
  model and maps expiry to a structured `TOOL_TIMEOUT` result carrying the
  budget. The shell declares its policy maximum, and tools that declare
  nothing stay unbounded.
- Deferred tool loading (codex `tool_search`/`defer_loading`): tools marked
  through `tool.DeferredTool` stay out of the request until `tool_search`
  activates them; activations are durable in run state and emit
  `tools.activated`, so a resumed run keeps the schemas it loaded.
  `adapters/mcp.ToolsDeferred` opts an MCP catalog into lazy loading.
- Ordered prompt registry with strict variables (DSH system-prompt): the
  system prefix is assembled from named sections at DSH order slots, with
  `agent.personaPrefix`/`agent.personaSuffix` interpolating `{{variables}}`
  (`workspace` and `platform` built in) around first-party guidance. A
  malformed or unknown reference fails the run before any model request,
  while discovered instruction files stay verbatim so a `{{` in an AGENTS.md
  cannot break a run.

## 0.1.0 - 2026-05-30

Initial usable ZenForge release candidate.

### Added

- High-level agent harness with `Stream`, `Run`, and `Resume`.
- Durable memory, JSONL, and SQLite event/checkpoint stores.
- OpenAI-compatible and Anthropic model adapters.
- Workspace, shell, todo/planner, MCP, memory, and sub-agent tooling.
- HITL approval brokers and CLI approval modes.
- HTTP/SSE server helpers and event replay.
- JSON/stdout/memory trace sinks plus OpenTelemetry span export.
- Fake sandbox test helpers and the optional Container Hub sandbox beta;
  local shell execution remains a direct workspace execution path, not a
  `sandbox.Sandbox` backend.
- ZenMind compatibility event and approval adapter.
- Initial conversation messages with checkpoint-safe first-run, resume, and
  plan/execute semantics; caller-owned tool arguments are copied into run state.
- ZenMind resolved-prompt precedence and strict platform history conversion,
  including OpenAI tool calls and snake/camel tool-call identity fields.
- SDK, provider, adapter, safety, resume, and release documentation.

### Known Limitations

- Resume restarts from checkpointed boundaries, not mid-provider stream tokens.
- MCP support covers tools; resources, prompts, sampling, discovery, and OAuth
  remain host/platform responsibilities.
- OpenTelemetry exporter setup is owned by host services.
- CLI config is JSON only.
- Container Hub remains optional/beta.

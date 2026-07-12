# Changelog

## Unreleased

### Added

- Optional cross-manager cancellation requests through
  `RunCancellationRegistry`, implemented by the memory and SQLite registries
  with lease-fenced owner polling and legacy SQLite schema migration.
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
- The bridge remains on its integration branch and is not claimed as merged to
  `agent-platform` `main`. A smoke test against a real Container Hub service
  also remains external acceptance.
- ZenForge and the bridge require Go 1.26.x; older Go toolchains are unsupported.

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

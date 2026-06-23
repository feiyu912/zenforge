# Changelog

## Unreleased

### Integration Status

- `agent-platform` branch `codex/zenforge-engine-bridge` at `d9ebc9e` now
  provides the downstream ZenForge engine bridge, feature-flag selector, and
  HTTP sync/async, SSE, WebSocket, approval, and attach integration tests.
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

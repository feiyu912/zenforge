# V0.1 Release Notes

V0.1.0 is the first usable ZenForge release candidate: a Go-native harness for
long-running, tool-using, observable, and recoverable agents.

## Highlights

- High-level `zenforge.Agent` with `Stream`, `Run`, and `Resume`.
- Platform-compatible React, Oneshot, and Plan/Execute execution presets with
  checkpointed mode identity.
- Durable event log and checkpoint stores: memory, JSONL, and SQLite.
- OpenAI-compatible and Anthropic model adapters.
- Local workspace, shell, todo/planner, MCP, memory, and sub-agent tooling.
- Human-in-the-loop approval broker and CLI approval modes.
- Server helpers for HTTP runs, resume, event replay, and SSE streaming.
- Trace sinks for JSONL/stdout/memory and OpenTelemetry spans.
- Direct local shell execution, fake sandbox test helpers, and the optional
  Container Hub sandbox adapter beta. Local execution is not a
  `sandbox.Sandbox` backend.
- ZenMind adapter with platform catalog/session DTOs and model resolution,
  fail-closed rollout routing, stateful flat-wire projection, approval protocol
  translation, and event-line JSONL output. Wire goldens are pinned to
  `agent-platform@1893edb5`.

## Safety Defaults

- Shell commands are deny-by-default and can require approval.
- Workspace access is jailed to configured roots.
- Sandbox-required execution fails closed instead of falling back to host shell.
- Trace redaction helpers cover common secret-bearing keys.
- Memory, MCP, catalog loading, tenancy, auth, and retention stay at adapter or
  host-platform boundaries.

## Known Limitations

See [limitations.md](./limitations.md). Important V0.1 limitations:

- Resume does not continue partially streamed provider responses.
- Shell command side effects should be designed for retry safety.
- MCP support starts with tools; resources, prompts, sampling, discovery, and
  OAuth flows remain platform responsibilities.
- OpenTelemetry exporter setup is owned by host services.
- CLI config is JSON, not YAML.
- Nested sub-agents are blocked by default.
- Container Hub remains optional/beta.
- External `agent-platform` engine/feature-flag/SSE/WS/fallback integration,
  complete Chat Storage V3.1, and real Container Hub acceptance are not covered
  by repository-local adapter goldens.

## Verification

Run before tagging:

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./docs/... ./cli ./adapters/zenmind
env GOTOOLCHAIN=local go test ./examples/...
rg -n '"[^"[:space:]]*agent-platform[^"[:space:]]*"' --glob "*.go" .
```

Expected:

- all packages pass, including docs link checks;
- examples compile;
- platform-boundary search returns no platform module import strings.

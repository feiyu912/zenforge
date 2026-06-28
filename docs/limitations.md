# Limitations

ZenForge is an MVP harness. It is intentionally explicit about what is durable,
what is experimental, and what remains adapter territory.

## Runtime

- Resume replaces an interrupted model attempt from the committed prompt
  boundary; it does not continue through a provider-native mid-token cursor.
- Tool argument event redaction does not remove original arguments from durable
  checkpoints because resume needs them.
- Resume does not assume an OS command completed if the process crashed while
  the command was running.
- Resume is strongest at checkpoint boundaries: before model calls, after model
  calls, before tools, after tools, and around approval waits.
- Long-running command cancellation depends on the configured shell or sandbox
  backend.

## Tools And Safety

- Shell is deny-by-default.
- Cross-run approval reuse is opt-in and limited to approved `ScopeRule`
  decisions. It requires a configured grant store, trusted tenant/subject
  namespace, and exact `ruleKey` plus operation `fingerprint`; once/run scopes
  remain checkpoint-only.
- Workspace tools enforce local root boundaries, but they are not a replacement
  for OS sandboxing when running untrusted workloads.
- Sandbox support is adapter-based. Core works without Container Hub.
- Legacy sandbox checkpoint state without run/subtask ownership is reopened
  instead of reused.

## Planning And Sub-Agents

- Plan/execute is a preset, not a general project-management system.
- Sub-agents resume from explicit parent and child checkpoint boundaries; they
  do not resume an in-flight provider stream inside a child run.
- Nested sub-agents are blocked by default and remain outside the MVP surface.

## Deferred Systems

- Full platform memory extraction is not included. Retrieved memory can be
  adapted into normalized tasks through `adapters/memory`.
- MCP tools can be adapted through `adapters/mcp`, but resources, prompts,
  sampling, discovery, and OAuth flows remain host/platform responsibilities.
- OpenTelemetry exporter setup is not included; host services provide tracer
  providers/exporters and can use `trace/otel` as the sink adapter.
- YAML config is not included; the current CLI config format is JSON.
- Container Hub is optional and lives behind `sandbox/containerhub`.

## Platform Boundary

ZenForge should not import `agent-platform` or ZenMind server/chat packages.
Those systems can adapt to ZenForge through public model, tool, workspace,
approval, sandbox, event log, checkpoint, and trace interfaces.

`adapters/zenmind` has repository-local golden coverage for the
`agent-platform@1893edb5` catalog/session DTO subset, stream wire envelopes,
content/tool projection, approval roundtrip, and event-only chat JSONL lines.
Projector cursor state is serializable for attach/resume, but the host still
owns where that state is stored alongside its transport cursor.
This repository does not implement complete Chat Storage V3.1 or own platform
server wiring. That downstream wiring is implemented and tested on
`agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3`, including the
engine selector, HTTP/SSE/WS, approval, attach, and legacy fallback. It has not
been merged to platform `main`. A real Container Hub deployment also remains an
environment acceptance item.

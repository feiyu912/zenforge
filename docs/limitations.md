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

## HTTP Lifecycle

- Detached `RunManager` ownership, status retention, duplicate exclusion,
  active-run accounting, the event bus, and `PendingBroker` are in-memory and
  single-process. The durable event check is not a distributed atomic claim.
- Multi-replica deployments need an external run lease/claim, shared approval
  routing, durable status coordination, and deliberate reconnect routing.
- Attachment disconnect stops only replay/follow delivery. It does not cancel
  detached execution; callers must use explicit cancel, a run timeout, or
  runtime shutdown.
- Terminal retention removes only manager status records. Durable events remain
  caller-owned and may still be replayed after status expires.
- The application owns OpenAI/Anthropic protocol and compatible base URL
  selection, credentials, auth/tenancy, route paths, durable stores and their
  closure, HTTP server shutdown, and `Runtime.Close`.

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

## Agent Skills

- Version 1 catalogs load one bounded `SKILL.md` body. Referenced auxiliary
  files, dependency resolution, package installation, updates, signatures, and
  remote fetching are not implemented by `load_skill`.
- `skill/fs` accepts an explicit trusted root and rejects unsafe provenance and
  symlink traversal. The application still owns source trust and allowlists.
- Bundles are immutable startup snapshots. They do not provide live catalog
  refresh or marketplace lifecycle management.
- Skills do not grant tools, approval, workspace access, sandbox access, or
  sub-agent authority. Those controls remain in their existing runtimes.
- ZenMind marketplace entitlement, materialization, UI, and APIs are not
  implemented in core; a platform adapter may supply trusted catalog inputs.

## Platform Boundary

ZenForge should not import `agent-platform` or ZenMind server/chat packages.
Those systems can adapt to ZenForge through public model, tool, workspace,
approval, sandbox, event log, checkpoint, and trace interfaces.

`adapters/zenmind` has repository-local golden coverage for the
`agent-platform@1893edb5` catalog/session DTO subset, stream wire envelopes,
content/tool projection, approval roundtrip, and event-only chat JSONL lines.
`BuildRun` resolves host-owned skills, tools/overrides, and workspace/host
access and propagates executable runtime config, but does not load platform
catalogs or construct host services. Declared `HostAccess` and `ToolOverrides`
fail closed when their resolvers are absent.

Projector state is serializable for attach/resume. Strict projection requires a
v2 run binding; readable v1 snapshots remain unbound and cannot use
`ProjectStrict`. `ApprovalEventBridge` snapshots pending/completed correlations
and reconstructs awaiting wire values from real events, but the host owns
snapshot persistence, awaiting ID allocation, submit routing, and delivery.
Reused grant resolutions emit no answer because no awaiting request was opened.

This repository does not implement complete Chat Storage V3.1 or own platform
server wiring. That downstream wiring is implemented and tested on
`agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3`, including the
engine selector, HTTP/SSE/WS, approval, attach, and legacy fallback. It has not
been merged to platform `main`. A real Container Hub deployment also remains an
environment acceptance item.

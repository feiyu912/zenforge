# Limitations

ZenForge is an MVP harness. It is intentionally explicit about what is durable,
what is experimental, and what remains adapter territory.

## Runtime

- Resume does not continue a partially streamed model response.
- Resume does not assume an OS command completed if the process crashed while
  the command was running.
- Resume is strongest at checkpoint boundaries: before model calls, after model
  calls, before tools, after tools, and around approval waits.
- Long-running command cancellation depends on the configured shell or sandbox
  backend.

## Tools And Safety

- Shell is deny-by-default.
- Shell command approval is scoped to the current request/run behavior; no
  cross-run persistent approvals are included in MVP.
- Workspace tools enforce local root boundaries, but they are not a replacement
  for OS sandboxing when running untrusted workloads.
- Sandbox support is adapter-based. Core works without Container Hub.

## Planning And Sub-Agents

- Plan/execute is a preset, not a general project-management system.
- Sub-agents are available as a runtime tool, but advanced resume of partially
  completed child runs is still limited.
- Nested sub-agents are blocked by default.

## Deferred Systems

- Full memory extraction is not included.
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

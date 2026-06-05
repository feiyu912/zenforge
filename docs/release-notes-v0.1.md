# V0.1 Release Notes

V0.1.0 is the first usable ZenForge release candidate: a Go-native harness for
long-running, tool-using, observable, and recoverable agents.

## Highlights

- High-level `zenforge.Agent` with `Stream`, `Run`, and `Resume`.
- Durable event log and checkpoint stores: memory, JSONL, and SQLite.
- OpenAI-compatible and Anthropic model adapters.
- Local workspace, shell, todo/planner, MCP, memory, and sub-agent tooling.
- Human-in-the-loop approval broker and CLI approval modes.
- Server helpers for HTTP runs, resume, event replay, and SSE streaming.
- Trace sinks for JSONL/stdout/memory and OpenTelemetry spans.
- Container Hub sandbox adapter beta plus local/fake sandbox backends.
- ZenMind compatibility adapter for event mapping and approval submit payloads.

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

## Verification

Run before tagging:

```bash
env GOCACHE=/private/tmp/agent-platform-go-build-cache go test ./...
go test ./examples/...
rg -n "agent-platform|ZenMind" --glob "*.go" .
```

Expected:

- all packages pass, including docs link checks;
- examples compile;
- platform-boundary search returns no Go-source matches.

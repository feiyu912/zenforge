# ZenForge

> Production-first Go agent runtime for long-running, tool-using, observable, and recoverable agents.

ZenForge is a batteries-included agent harness for Go services. A single `zenforge.Agent` runs real multi-step work, with replaceable adapters for every concern — model, tools, workspace, planner, checkpoint store, event log, trace sink, approval broker, sandbox, and HTTP/SSE edge. Resume is first-class, not bolted on.

It is **not** a Go clone of LangChain. The goal is a small, opinionated runtime that you can embed in a backend, a CLI, a desktop app, or a gateway, instead of pulling in a Python agent framework.

Current release: `v0.1.0`. The `main` branch carries additional v0.1.x capabilities on top of that tag — see [Project Status](#project-status).

## Why

Most agent frameworks target notebooks. ZenForge targets services:

- **Durable runs** — checkpoints at every boundary, resume after crashes.
- **Observable execution** — typed event stream + JSONL/SQLite/OTel sinks.
- **Replaceable parts** — swap models, stores, transports, even the planner, without rewriting the loop.
- **Small public surface** — five functions, one `Config` struct, one `Task` type. Everything else is an interface.

## Quick Look

```go
import (
    "context"
    "os"

    "github.com/feiyu912/zenforge"
    checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
    eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
    "github.com/feiyu912/zenforge/model/openai"
    "github.com/feiyu912/zenforge/tools"
    "github.com/feiyu912/zenforge/trace"
)

lookup := tools.Must("lookup", "Look up internal facts.",
    func(ctx context.Context, in struct {
        Query string `json:"query" jsonschema:"required"`
    }) (string, error) {
        return "result for " + in.Query, nil
    })

ctx := context.Background()
events, err := eventlogsqlite.Open(ctx, ".zenforge/runs.db")
if err != nil {
    return err
}
defer events.Close()
checkpoints, err := checkpointsqlite.Open(ctx, ".zenforge/runs.db")
if err != nil {
    return err
}
defer checkpoints.Close()

agent := zenforge.New(zenforge.Config{
    Model:        openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY"), Model: "gpt-4.1"}),
    Instructions: "Use tools when useful and answer briefly.",
    Tools:        []zenforge.Tool{lookup},
    Events:       events,
    Checkpoints:  checkpoints,
    Trace:        trace.Redact(trace.Stdout()),
    MaxSteps:     8,
})

result, err := agent.Run(ctx, zenforge.Task{Input: "Review this package and summarize the risk."})
// for ev := range agent.Stream(ctx, task) { ... }
// agent.Resume(ctx, "run_123")
```

## Install

```bash
go get github.com/feiyu912/zenforge@v0.1.0
```

Go 1.22+. The core is dependency-light: OpenTelemetry SDK and pure-Go SQLite via `modernc.org/sqlite` (no cgo).

## CLI

```bash
go run ./cmd/zenforge init
export OPENAI_API_KEY=...
go run ./cmd/zenforge run --config zenforge.json "Analyze this repo"
go run ./cmd/zenforge run --checkpoint-type sqlite --checkpoint-dir .zenforge/runs.db "..."
go run ./cmd/zenforge resume run_123
go run ./cmd/zenforge runs
```

Config is JSON. See [`docs/config-reference.md`](docs/config-reference.md) and [`docs/cli-design.md`](docs/cli-design.md).

## Highlights

**Runtime**
- Single `zenforge.Agent` with `Stream`, `Run`, `Resume`.
- Plan/execute preset with built-in todo manager.
- Run-scoped pending approval broker (`approval.PendingBroker`).
- Durable event log and checkpoint stores: memory, JSONL, SQLite.
- Sub-agent runtime tool with checkpoint-aware child resume; nested sub-agents blocked by default.

**Models**
- OpenAI-compatible and Anthropic adapters.
- Streaming text and tool calls.

**Tools**
- Typed tool helper that infers JSON schema from Go structs.
- Workspace, shell (deny-by-default), todo, MCP bridge, sub-agent task tool.
- Memory augmenter that hydrates normalized tasks from a store.

**HTTP / SSE edge** — `server/harnesshttp`
- `POST /run`, `POST /resume`, `GET /events` (replay with `afterSeq`), `GET /live` (live fanout).
- `GET /approvals`, `POST /approval` for run-scoped pending approval flows.
- Access control hook to enforce auth and inject trusted metadata; ZenForge does not own auth, tenancy, or catalog loading.

**Live events**
- `eventlog.Bus` and `eventlog.FanoutStore` for multiple live subscribers.
- Replay (`/events`) and live (`/live`) are deliberately separate: replay is the read model, live is fanout.

**Observability**
- Trace sinks: memory, stdout, JSONL, OpenTelemetry spans.
- Trace metadata enrichment.
- Redaction helpers for common secret-bearing keys.

**Platform adapters**
- `adapters/zenmind` — run config mapping, chat JSONL projection, feature flag router.
- `adapters/mcp` — MCP tool bridge (resources/prompts/sampling/OAuth stay with the host).
- `adapters/memory` — scoped memory augmentation into normalized tasks.

**Sandbox**
- Local, fake, and Container Hub (beta) backends.
- `sandbox.State` helpers for cross-run session continuity.

## Examples

Each example is a runnable Go program under [`examples/`](examples/). The SDK
embedded example runs locally without an API key; provider-backed examples need
`OPENAI_API_KEY` or an OpenAI-compatible endpoint.

| Example | What it shows |
| --- | --- |
| [`sdk-embedded-agent`](examples/sdk-embedded-agent) | Embed ZenForge in a Go service; runs without an API key. |
| [`simple-tool-agent`](examples/simple-tool-agent) | Minimal model + tool loop. |
| [`code-review-agent`](examples/code-review-agent) | Workspace + shell with approval. |
| [`repo-refactor-agent`](examples/repo-refactor-agent) | Long task with checkpoints and resume. |

## Documentation

Start here:
- [Quickstart](docs/quickstart.md)
- [SDK Guide](docs/sdk-guide.md)
- [Provider Guide](docs/provider-guide.md)
- [Tool Authoring](docs/tool-authoring-guide.md)

Server and edge:
- [Server HTTP Guide](docs/server-http-guide.md) · [Server SSE Guide](docs/server-sse-guide.md)
- [Approval Guide](docs/approval-guide.md) · [Checkpoint & Resume](docs/checkpoint-resume-guide.md)

Adapters and integrations:
- [ZenMind Adapter](docs/zenmind-adapter-guide.md) · [MCP Adapter](docs/mcp-adapter-guide.md) · [Memory Adapter](docs/memory-adapter-guide.md)
- [Sandbox Guide](docs/sandbox-guide.md) · [Sub-Agent Guide](docs/subagent-guide.md) · [Planner Guide](docs/planner-guide.md) · [Trace Guide](docs/trace-guide.md)

Design and operation:
- [Architecture](docs/architecture.md) · [Harness State Machine](docs/harness-state-machine.md) · [Failure Modes](docs/failure-modes.md) · [Security Guide](docs/security-guide.md) · [Limitations](docs/limitations.md)
- [MVP Validation](docs/mvp-validation.md) · [v0.1 Release Notes](docs/release-notes-v0.1.md) · [Release Checklist](docs/release-checklist.md)
- [Vision](docs/vision.md) · [Product Roadmap](docs/product-roadmap.md)

Architecture decision records live in [`docs/adr/`](docs/adr/).

## Project Status

`v0.1.0` is the first usable release candidate. The current `main` branch adds the following on top of the v0.1.0 tag without changing the public surface:

- `server/harnesshttp` access control hook for auth and tenancy injection.
- `eventlog.Bus` and `eventlog.FanoutStore` for live multi-subscriber event fanout.
- `approval.PendingBroker` for run-scoped pending approvals, exposed via `GET /approvals` and `POST /approval`.
- `adapters/zenmind`: run configuration mapping, chat JSONL projection, feature flag router.
- `adapters/memory`: scoped memory augmentation.
- Sub-agent resume reuses terminal children and continues existing child checkpoints.
- Active tool resume is covered through durable JSONL checkpoints.
- CLI run/resume are covered against local OpenAI-compatible streaming and durable JSONL checkpoints.
- CLI argument error output is covered for common command mistakes.
- Config reference is checked against the generated `zenforge init` defaults.
- Release notes version coverage is checked against `VERSION`.
- Durable schema version docs and flattened event contract docs are checked.
- MVP validation evidence is checked against existing test and benchmark names.
- Go source platform-boundary terms are checked in the docs test suite.
- The SDK embedded example is run in tests without an API key.
- MVP scope now reflects the current CLI, adapter, resume, and example surface.
- Architecture package layout is aligned with the current repository.
- Historical API sketch is labeled and current guides are prioritized.
- README Quick Look and architecture snippets use current store/interface names.
- User-facing guides no longer present themselves as drafts and use current tool, shell, and sandbox APIs.
- `sandbox.State` for cross-run session continuity.
- Trace metadata enrichment.
- A hardening test suite and a failure-mode guide.

Verification before each release:

```bash
go test ./...
go test ./examples/...
grep -R -n -E 'agent-platform|ZenMind' --include='*.go' .   # must return nothing
```

**Not in MVP** — see [`docs/limitations.md`](docs/limitations.md) for the full list:

- Resume does not continue a partially streamed provider response.
- Cross-run persistent approvals are not included.
- MCP covers tools only; resources, prompts, sampling, discovery, and OAuth stay with the host platform.
- OpenTelemetry exporter setup stays in host services.
- CLI config is JSON only.
- Nested sub-agents are blocked by default.
- Container Hub sandbox is optional and beta.

## Repository Layout

```text
zenforge/
  agent.go              # zenforge.Agent + Config + Task + Result
  task.go               # normalized task model
  events.go             # public event contract
  config.go             # high-level Config
  approval/             # broker + run-scoped pending broker
  checkpoint/           # memory, jsonl, sqlite stores
  eventlog/             # bus + fanout + memory, jsonl, sqlite stores
  model/                # openai, anthropic adapters
  tools/                # workspace, shell, todo, task
  subagent/             # sub-agent runtime
  planner/              # todo manager + plan/execute preset
  sandbox/              # local, fake, containerhub + State helpers
  workspace/            # workspace interface + local impl
  policy/               # shell/workspace policy types
  trace/                # sinks: memory, stdout, jsonl, otel
  recorder/             # event recorder helpers
  eventlog/             # fanout + durable stores
  server/               # harnesshttp + sse helpers
  adapters/             # mcp, memory, zenmind
  harness/              # loop, state machine, resume
  examples/             # runnable examples
  cmd/zenforge/         # CLI
  docs/                 # design, guides, ADRs
```

## Contributing

Issues and pull requests are welcome. The CI workflow runs `go test ./...`, builds the examples, and fails the build if any Go file references `agent-platform` or ZenMind platform packages — that boundary is the point of the project.

Before opening a PR, run:

```bash
go test ./...
go test ./examples/...
```

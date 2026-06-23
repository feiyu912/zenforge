# ZenForge

> Production-first Go agent runtime for long-running, tool-using, observable, and recoverable agents.

ZenForge is a batteries-included agent harness for Go services. A single `zenforge.Agent` runs real multi-step work, with replaceable adapters for every concern — model, tools, workspace, planner, checkpoint store, event log, trace sink, approval broker, sandbox, and HTTP/SSE edge. Resume is first-class, not bolted on.

It is **not** a Go clone of LangChain. The goal is a small, opinionated runtime that you can embed in a backend, a CLI, a desktop app, or a gateway, instead of pulling in a Python agent framework.

Current release: `v0.1.0`. The `main` branch carries additional v0.1.x capabilities on top of that tag — see [Project Status](#project-status).

## Why

Most agent frameworks target notebooks. ZenForge targets services:

- **Durable runs** — checkpoints at every boundary, resume after crashes.
- **Observable execution** — typed event stream + JSONL/SQLite/OTel sinks, with fail-closed event-log writes.
- **Replaceable parts** — swap models, stores, transports, even the planner, without rewriting the loop.
- **Small public surface** — a focused root package for agent construction,
  normalized tasks, run results, and events, with replaceable interfaces in
  focused subpackages.

## Quick Look

```go
import (
    "context"
    "os"

    "github.com/feiyu912/zenforge"
    checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
    eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
    "github.com/feiyu912/zenforge/model"
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

result, err := agent.Run(ctx, zenforge.Task{
    Input: "Review this package and summarize the risk.",
    InitialMessages: []model.Message{
        {Role: "user", Content: "We are reviewing the storage package."},
        {Role: "assistant", Content: "I will preserve that context."},
    },
})
// for ev := range agent.Stream(ctx, task) { ... }
// agent.Resume(ctx, "run_123")
```

`Task.InitialMessages` supplies prior conversation in model order. ZenForge
checkpoints that history before appending the current `Input`; checkpoint
resume reuses it without duplication. In `plan_execute`, only the planning
stage receives the conversation history.

## Install

```bash
go get github.com/feiyu912/zenforge@main
go install github.com/feiyu912/zenforge/cmd/zenforge@main
```

Go 1.26 only. Both local development and CI require a Go 1.26.x toolchain;
CI sets `GOTOOLCHAIN=local` and rejects other versions. The core uses the
OpenTelemetry SDK, pure-Go SQLite via `modernc.org/sqlite` (no cgo), and
`mvdan.cc/sh/v3` for structural shell safety analysis.

## CLI

```bash
go run ./cmd/zenforge init
export OPENAI_API_KEY=...
go run ./cmd/zenforge run --config zenforge.json "Analyze this repo"
go run ./cmd/zenforge code --config zenforge.json ./repo "Review and improve this codebase"
go run ./cmd/zenforge run --checkpoint-type sqlite --checkpoint-dir .zenforge/runs.db "..."
go run ./cmd/zenforge resume --config zenforge.json run_123
go run ./cmd/zenforge events --config zenforge.json run_123
go run ./cmd/zenforge runs --config zenforge.json
```

`run` executes a task in the configured workspace. `code <repo> <task>` binds
workspace and shell execution to the resolved repository. `resume <runID>`
continues a supported durable checkpoint, `events <runID>` prints its event
history (`--json` for JSON), and `runs` lists durable run summaries (`--json`
for JSON). Config is JSON and `init` creates `zenforge.json`; flags override
the file. See [`docs/config-reference.md`](docs/config-reference.md) and
[`docs/cli-design.md`](docs/cli-design.md).

CLI failures use stable exit codes: `1` runtime error, `2` invalid config or
usage, `3` cancellation, `4` approval rejection, and `5` unsupported resume
state (`0` is success).

## Highlights

**Runtime**
- Single `zenforge.Agent` with `Stream`, `Run`, `Resume`.
- Root Agent assembly delegates the durable model/tool state machine to the independently testable `harness.Runner`.
- Platform-compatible `react`, `oneshot`, and `plan_execute` execution modes, persisted across resume.
- Plan/execute preset with built-in todo manager.
- Run-scoped pending approval broker (`approval.PendingBroker`).
- Broker-free approval requests pause durably instead of allowing the model to continue past a risky tool.
- Run and rule approval scopes are durable grants matched by exact fingerprint or rule key.
- Durable event log and checkpoint stores: memory, JSONL, SQLite.
- JSONL stores reject path-like run IDs and serialize writers across processes
  with advisory `flock`; checkpoint saves recover through a pending journal.
- Sub-agent runtime tool with checkpoint-aware child resume; nested sub-agents blocked by default.
- S7 child progress is forwarded into the parent stream as real-time
  `subtask.event` records, while terminal child results retain stable task order.
- Host-owned sub-agent limits drive the advertised task schema and cannot be widened by model requests.
- Sub-agent tools are available independently of planner/todo configuration.
- Nested delegation remains off by default and requires explicit host opt-in plus a finite maximum depth.
- Child metadata is isolated by default; `InheritContext` explicitly carries trusted parent run metadata into children.
- Task file scopes are exposed to children as `subagent.files`, and child configs retain the host workspace boundary.

**Models**
- OpenAI-compatible and Anthropic adapters.
- Streaming text and tool calls.

**Tools**
- Typed tool helper that infers JSON schema from Go structs.
- Typed tool handlers can opt into the runtime `tool.Context` for run-scoped decisions.
- Workspace, shell (deny-by-default), todo, MCP bridge, sub-agent task tool.
- Workspace file policy supports read/write roots, approval requests, run-scoped read snapshots, and SHA256 stale-write detection.
- Workspace reads and grep reuse the platform binary-extension/device denylist in addition to content-based NUL detection.
- Shell policy uses the complete platform Bash AST and security classifiers. It hard-blocks dangerous or ambiguous forms, routes output redirections and complex structures to approval, and requires every parsed command in a chain or substitution to satisfy the allowlist.
- Memory augmenter that hydrates normalized tasks from a store.
- Explicit transient-error retry, per-run call budgets, UTF-8-safe output caps, and recursive audit argument redaction.
- Shell capture is bounded while commands run, and Container Hub response
  bodies are rejected when they exceed the adapter limit.

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
- `adapters/zenmind` — platform catalog/session DTOs with `ModelResolver`,
  strict history conversion, resolved-prompt precedence, fail-closed
  AgentKey/ChatID/RunID routing, stateful content/tool projection, approval wire
  translation, and platform event-line JSONL output.
- `adapters/mcp` — MCP tool bridge (resources/prompts/sampling/OAuth stay with the host).
- `adapters/memory` — scoped memory augmentation into normalized tasks.

The ZenMind wire contract is checked against fixtures captured from
`agent-platform@1893edb5` under
[`adapters/zenmind/testdata/platform`](adapters/zenmind/testdata/platform).
These goldens cover catalog/session input, flat stream envelopes, content/tool
lifecycles, approval ask/submit/answer, and chat event lines. Downstream
integration is implemented and tested on `agent-platform` branch
`codex/zenforge-engine-bridge` at `d9ebc9e`: it includes the engine bridge,
feature-flag selector, HTTP sync/async, SSE, WebSocket, approval, attach, and
legacy-fallback paths. That branch has not been merged to `agent-platform`
`main`, and these repository goldens alone remain narrower evidence.

`BuildRun` maps `Session.HistoryMessages` into `Task.InitialMessages`, including
OpenAI `tool_calls` and snake/camel tool-call IDs, and rejects malformed history
with its message index. `Session.ResolvedPrompt` takes precedence over the
legacy catalog instruction field. Raw tool arguments are copied into run-owned
state so later caller mutation cannot alter model requests or checkpoints.

For the platform event-line read model, project events first, then append each
`StreamEvent` with an explicit chat ID:

```go
projector := zenmind.NewProjectorWithIdentity(zenmind.ProjectorIdentity{
    ChatID: chatID, AgentKey: agentKey,
})
writer := zenmind.NewChatJSONLWriter(root)
for _, projected := range projector.Project(event) {
    if err := writer.Append(ctx, chatID, projected); err != nil {
        return err
    }
}
lines, err := zenmind.ReadEventLines(ctx, root, chatID)
```

`ChatJSONLWriter` writes `root/chatId.jsonl` platform `EventLine` records with
top-level `chatId`, `runId`, `updatedAt`, `liveSeq`, `event`, and `_type`. The deprecated
`LegacyChatJSONLWriter` type, constructed with
`NewLegacyChatJSONLWriter(root, mapper)`, and `ReadChatRecords` retain the old
`root/runId/chat.jsonl` `zenmind.chat_trace.v1` format only for existing callers.
Neither writer implements complete Chat Storage V3.1.

**Sandbox**
- Local shell tools execute directly in the configured workspace; they are not
  a `sandbox.Sandbox` backend.
- `sandbox/fake` provides a test backend, and `sandbox/containerhub` provides an
  optional beta Container Hub backend.
- Scoped `sandbox.State` helpers for same-run/subtask session continuity.
- Closed or cross-scope sessions are never written back as reusable checkpoint state.

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

`v0.1.0` is the first usable release candidate. The current `main` branch adds the following on top of the v0.1.0 tag without intentional breaking changes:

- `server/harnesshttp` access control hook for auth and tenancy injection.
- `eventlog.Bus` and `eventlog.FanoutStore` for live multi-subscriber event fanout.
- `approval.PendingBroker` for run-scoped pending approvals, exposed via `GET /approvals` and `POST /approval`.
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
  tested on `agent-platform` branch `codex/zenforge-engine-bridge@d9ebc9e`.
  The branch is not yet merged to platform `main`.

Verification before each release:

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./examples/...
env GOTOOLCHAIN=local go test ./docs/...
rg -n 'zenforge\.ya?ml|```ya?ml' README.md docs             # must return nothing
git diff --check
```

**Not in MVP** — see [`docs/limitations.md`](docs/limitations.md) for the full list:

- Resume does not continue a partially streamed provider response.
- Cross-run persistent approvals are not included.
- MCP covers tools only; resources, prompts, sampling, discovery, and OAuth stay with the host platform.
- OpenTelemetry exporter setup stays in host services.
- CLI config is JSON only.
- Core event/checkpoint JSONL durability uses Unix advisory file locks and
  therefore requires a filesystem with working `flock` semantics for
  multi-process writers.
- Nested sub-agents are blocked by default.
- Container Hub sandbox is optional and beta.
- A real Container Hub deployment smoke test remains external acceptance.

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
  sandbox/              # interface, fake/containerhub backends + State helpers
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

Issues and pull requests are welcome. The CI workflow runs `go test ./...` and
builds the examples. Core and other non-adapter Go packages must not couple to
`agent-platform` or ZenMind branding. `adapters/zenmind` may document protocol
provenance, but its imports are AST-checked to reject the platform module and
all `internal` packages.

Before opening a PR, run:

```bash
go test ./...
go test ./examples/...
```

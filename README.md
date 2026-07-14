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

    "github.com/feiyu912/zenforge"
    checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
    eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
    "github.com/feiyu912/zenforge/model"
    "github.com/feiyu912/zenforge/model/provider"
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
modelClient, err := provider.FromEnv()
if err != nil {
    return err
}
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
    Model:        modelClient,
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

## Run The Complete Harness Example

The application owns the selected model, tools, approval broker, and sandbox.
ZenForge supplies public adapters so the assembly stays small:

```bash
export ZENFORGE_PROVIDER=anthropic
export ZENFORGE_MODEL=MiniMax-M3
export ZENFORGE_API_KEY=...
export ZENFORGE_BASE_URL=https://api.minimax.io/anthropic

go run ./examples/harness-agent -question \
  "Load the project skill, inspect this project, and prove the shell runs in Docker."
```

Choose `openai` or `anthropic` according to the endpoint protocol. MiniMax and
other compatible vendors are BaseURL configurations, not additional ZenForge
provider types. The key must belong to the selected endpoint. This example uses
an Agent Skill catalog, a separate typed `inspect_path` tool, numbered HITL
approval, and the built-in Docker sandbox with a read-only workspace mount.
Override the bundled skill directory with `-skill-root` or
`ZENFORGE_SKILL_ROOT`.

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

Provider selection is protocol-shaped, not vendor-shaped: use
`--provider openai` for OpenAI-compatible Chat Completions and
`--provider anthropic` for Anthropic-compatible Messages. Other vendors stay as
`--base-url` overrides instead of becoming new core provider names. For
DeepAgents-style MiniMax:

```bash
export ANTHROPIC_API_KEY=...
go run ./cmd/zenforge run \
  --provider anthropic \
  --model MiniMax-M3 \
  --api-key-env ANTHROPIC_API_KEY \
  --base-url https://api.minimax.io/anthropic \
  "Analyze this repo"
```

SDK users can pass any application-owned `model.Model` to `zenforge.New`, so
custom gateways and test models live outside the harness instead of becoming
new core provider names.

## Highlights

**Runtime**
- Single `zenforge.Agent` with `Stream`, `Run`, `Resume`.
- Root Agent assembly delegates the durable model/tool state machine to the independently testable `harness.Runner`.
- Model stream drafts are checkpointed before publication. A process crash
  supersedes the interrupted attempt and restarts the same logical step without
  committing partial text, tool calls, or usage twice; attempt history is
  bounded and validated on load.
- Platform-compatible `react`, `oneshot`, and `plan_execute` execution modes, persisted across resume.
- Plan/execute preset with built-in todo manager.
- Run-scoped pending approval broker (`approval.PendingBroker`) plus optional
  durable approval inboxes (`approval.Inbox`) backed by memory or SQLite.
- Broker-free approval requests pause durably instead of allowing the model to continue past a risky tool.
- When an approval inbox is configured, the waiting checkpoint is saved and the
  request is registered durably before `approval.requested` is emitted.
  HTTP approval submit commits the decision before returning success, and a
  resumed waiter can consume a decision submitted by another process.
- Run and rule approval scopes are checkpointed grants matched by exact
  fingerprint or rule key. Optional `approval.GrantStore` persistence reuses
  only `ScopeRule` across runs, isolated by tenant/subject and exact rule key
  plus fingerprint, with TTL and revocation support.
- Durable event log and checkpoint stores: memory, JSONL, SQLite.
- Canonical `server/harnesshttp.NewRuntime` assembly for detached HTTP
  start/resume/status/list/attach/cancel, durable SSE reconnect with
  `Last-Event-ID`, explicit cancellation, active-run limits, timeout, and
  terminal status retention.
- Optional detached run registry (`RunRegistry`) for shared start/resume
  claims, lease refresh, durable status lookup, and run listing across managers, with
  memory and SQLite implementations.
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
- Environment factory for `ZENFORGE_*`, `OPENAI_*`, or `ANTHROPIC_*`
  configuration.
- Streaming text and tool calls.

**Tools**
- Typed tool helper that infers JSON schema from Go structs.
- Typed tool handlers can opt into the runtime `tool.Context` for run-scoped decisions.
- Workspace, shell (deny-by-default), todo, MCP bridge, sub-agent task tool.
- Workspace file policy supports read/write roots, approval requests, run-scoped read snapshots, and SHA256 stale-write detection.
- Workspace reads and grep reuse the platform binary-extension/device denylist in addition to content-based NUL detection.
- Shell policy uses the complete platform Bash AST and security classifiers. It hard-blocks dangerous or ambiguous forms, routes output redirections and complex structures to approval, and requires every parsed command in a chain or substitution to satisfy the allowlist.

**Agent Skills**
- Filesystem `SKILL.md` catalogs with bounded metadata/content validation.
- Progressive disclosure through `Config.Skills`: the first model request sees
  descriptors only. `load_skill` returns instructions plus a bounded auxiliary
  resource index, and the same tool loads one indexed resource on demand. All
  returned content includes SHA-256 identity and safe relative provenance.
- Skills are instruction packages, not executable tools. Catalog ownership,
  installation, trust, allowlists, and marketplace integration stay with the
  embedding application or platform.

Agent Skills are optional. An application that wants them owns the directory
and passes an immutable bundle:

```go
catalog, err := skillfs.New("./skills", skillfs.Options{Source: "my-app"})
if err != nil {
    return err
}
skills, err := skill.NewBundle(ctx, catalog, nil)
if err != nil {
    return err
}
agent := zenforge.New(zenforge.Config{
    Model:  modelClient,
    Skills: skills,
})
```

Each immediate child of `./skills` is one package:

```text
skills/
  project-review/
    SKILL.md
    references/
      checklist.md
```

- Memory augmenter that hydrates normalized tasks from a store.
- Explicit transient-error retry, per-run call budgets, UTF-8-safe output caps, and recursive audit argument redaction.
- Shell capture is bounded while commands run, and Container Hub response
  bodies are rejected when they exceed the adapter limit.

**HTTP / SSE edge** — `server/harnesshttp`
- `POST /run`, `POST /resume`, `GET /events` (bounded replay), and `GET /live`
  (live fanout or durable replay-to-live with `replay=true`).
- `GET /approvals`, `POST /approval` for run-scoped pending approval flows.
- Canonical `NewRuntime` wiring for detached start, resume, status, list,
  attach, explicit cancel, and safe-boundary steer. It shares one fanout store, bus, and approval inbox;
  disconnecting an attachment does not cancel the run.
- Every `zenforge.Agent` creates an in-process run controller by default, so
  SDK callers can use `Agent.Steer` without extra wiring.
- The manager is single-process by default. Configure `RunManagerOptions.Registry`
  with `NewMemoryRunRegistry` or `OpenSQLiteRunRegistry` to add shared run
  claims, lease refresh, durable status/list lookup, and cross-manager durable
  replay attachment. Their optional `RunCancellationRegistry` extension also
  lets any replica persist cancellation for the active owner; a recovering
  owner consumes a pending request before opening the resumed agent stream.
  Their optional `RunRegistryDeleter` extension lets explicit terminal
  `RunManager.Forget` cleanup remove a registry status record while preserving
  durable events and checkpoints.
  Steer is owner-local: it becomes a user message after pending tools and
  before the next model request. Multi-worker hosts route it to `OwnerID` or
  use an application-owned durable control queue.
  Applications still own model provider/protocol and compatible base URL
  configuration, auth, route paths, durable store closure, lifecycle shutdown,
  and idempotent external side effects.
- Hosts can explicitly recover expired, nonterminal registry records with
  `RunManager.RecoverStale`; normal resume claims keep concurrent recovery
  workers fenced, and per-run outcomes remain visible to the caller.
- The [deployment guide](docs/deployment-guide.md) defines supported
  single-process, shared-host, and multi-host topologies; per-operation routing;
  external side-effect idempotency; distributed cancellation; crash recovery;
  and graceful rollout acceptance.
- `examples/http-harness-agent` is the runnable loopback-only service assembly:
  environment-selected OpenAI/Anthropic protocol, SQLite events/checkpoints/
  approvals/runs, Agent Skills, typed tool, Docker shell sandbox, HITL, and
  detached HTTP lifecycle endpoints.

**Live events**
- `eventlog.Bus` and `eventlog.FanoutStore` for multiple live subscribers.
- `GET /live?replay=true&afterSeq=N` subscribes before replay, catches up from
  the durable store, de-duplicates by sequence, and accepts `Last-Event-ID` on
  reconnect. Plain `/live` remains ephemeral fanout.

**Observability**
- Trace sinks: memory, stdout, JSONL, OpenTelemetry spans.
- Trace metadata enrichment.
- Redaction helpers for common secret-bearing keys.

**Platform adapters**
- `adapters/zenmind` — platform catalog/session DTOs with host-owned
  model/skill/tool/workspace resolution, strict history conversion,
  fail-closed AgentKey/ChatID/RunID routing, run-scoped strict projection,
  event-driven approval correlation, and platform event-line JSONL output.
- `adapters/mcp` — MCP tool bridge (resources/prompts/sampling/OAuth stay with the host).
- `adapters/memory` — scoped memory augmentation into normalized tasks.

The ZenMind wire contract is checked against fixtures captured from
`agent-platform@1893edb5` under
[`adapters/zenmind/testdata/platform`](adapters/zenmind/testdata/platform).
These goldens cover catalog/session input, flat stream envelopes, content/tool
lifecycles, approval ask/submit/answer, and chat event lines. A checked
`manifest.json` pins every fixture's source files and SHA-256. Downstream
integration is implemented and tested on `agent-platform` branch
`codex/zenforge-engine-bridge` at `82ca4d3`: it includes the engine bridge,
feature-flag selector, HTTP sync/async, SSE, WebSocket, approval, attach, and
legacy-fallback paths. `agent-platform` `main@f6d89da` restores the bridge,
selector, routing, initialization, and rollout documentation; platform Go
1.26 tests, race tests, and HTTP stream integration pass. The existing
`agent-webclient` protocol consumer also passes 90 focused query/attach/submit,
event-processing, and HITL tests and its production build. This remains
narrower than deployed UI evidence. The opt-in Container Hub adapter test covers a
disposable live Hub session, not a production deployment.

`BuildRun` maps `Session.HistoryMessages` into `Task.InitialMessages`, including
OpenAI `tool_calls` and snake/camel tool-call IDs, and rejects malformed history
with its message index. `Session.ResolvedPrompt` takes precedence over the
legacy catalog instruction field. Raw tool arguments are copied into run-owned
state so later caller mutation cannot alter model requests or checkpoints.
Catalog skills, tools/overrides, and workspace root/host access are resolved by
host callbacks. Declared `HostAccess` or `ToolOverrides` without the matching
resolver fail closed. The complete runtime-owned tool, approval, planner,
sub-agent, persistence, trace, mode, planning, and step configuration is
propagated into `zenforge.Config`.

For the platform event-line read model, project events first, then append each
`StreamEvent` with an explicit chat ID:

```go
projector := zenmind.NewProjectorWithIdentity(zenmind.ProjectorIdentity{
    RunID: runID, ChatID: chatID, AgentKey: agentKey,
})
writer := zenmind.NewChatJSONLWriter(root)
projectedEvents, err := projector.ProjectStrict(event)
if err != nil {
    return err
}
for _, projected := range projectedEvents {
    if err := writer.Append(ctx, chatID, projected); err != nil {
        return err
    }
}
lines, err := zenmind.ReadEventLines(ctx, root, chatID)
```

Persist `projector.Snapshot()` beside the host's attach cursor and restore it
with `zenmind.NewProjectorFromState`; open content/tool blocks and platform
sequence numbers then continue without reused IDs.
Version 2 snapshots preserve the run binding. Version 1 snapshots remain
readable as unbound compatibility state, but fail closed under `ProjectStrict`.

`ChatJSONLWriter` writes `root/chatId.jsonl` platform `EventLine` records with
top-level `chatId`, `runId`, `updatedAt`, `liveSeq`, `event`, and `_type`, and
rejects duplicate or decreasing cursors for the same run. The deprecated
`LegacyChatJSONLWriter` type, constructed with
`NewLegacyChatJSONLWriter(root, mapper)`, and `ReadChatRecords` retain the old
`root/runId/chat.jsonl` `zenmind.chat_trace.v1` format only for existing callers.
Neither writer implements complete Chat Storage V3.1.

**Sandbox**
- Local shell tools execute directly in the configured workspace; they are not
  a `sandbox.Sandbox` backend.
- `sandbox/docker` provides a local Docker backend with a read-only root
  filesystem, no network by default, bounded output, and no host fallback.
- `sandbox/fake` provides a test backend, and `sandbox/containerhub` provides
  an optional beta Container Hub backend.
- Scoped `sandbox.State` helpers for same-run/subtask session continuity.
- Closed or cross-scope sessions are never written back as reusable checkpoint state.

## Examples

Each example is a runnable Go program under [`examples/`](examples/). The SDK
embedded example runs locally without an API key; provider-backed examples need
`OPENAI_API_KEY` or an OpenAI-compatible endpoint.

| Example | What it shows |
| --- | --- |
| [`sdk-embedded-agent`](examples/sdk-embedded-agent) | Embed ZenForge in a Go service; runs without an API key. |
| [`harness-agent`](examples/harness-agent) | Env provider + Agent Skill catalog + typed tool + HITL + Docker sandbox. |
| [`simple-tool-agent`](examples/simple-tool-agent) | Minimal model + tool loop. |
| [`code-review-agent`](examples/code-review-agent) | Workspace + shell with approval. |
| [`repo-refactor-agent`](examples/repo-refactor-agent) | Long task with checkpoints and resume. |

## Documentation

Start here:
- [Quickstart](docs/quickstart.md)
- [SDK Guide](docs/sdk-guide.md)
- [Provider Guide](docs/provider-guide.md)
- [Tool Authoring](docs/tool-authoring-guide.md)
- [Agent Skills Spec](docs/agent-skills-spec.md)
- [Deployment Guide](docs/deployment-guide.md)

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

Verification before each release:

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./examples/...
env GOTOOLCHAIN=local go test ./docs/...
rg -n 'zenforge\.ya?ml|```ya?ml' README.md docs             # must return nothing
git diff --check
```

**Not in MVP** — see [`docs/limitations.md`](docs/limitations.md) for the full list:

- Resume replaces an interrupted model attempt from its committed prompt
  boundary; it does not use a provider-native mid-token cursor.
- MCP covers tools only; resources, prompts, sampling, discovery, and OAuth stay with the host platform.
- OpenTelemetry exporter setup stays in host services.
- CLI config is JSON only.
- Core event/checkpoint JSONL durability uses Unix advisory file locks and
  therefore requires a filesystem with working `flock` semantics for
  multi-process writers.
- Nested sub-agents are blocked by default.
- Container Hub sandbox is optional and beta.
- A production Container Hub deployment smoke test remains external acceptance;
  the opt-in adapter integration test covers a disposable live Hub session.

## Repository Layout

```text
zenforge/
  agent.go              # zenforge.Agent + Config + Task + Result
  task.go               # normalized task model
  events.go             # public event contract
  config.go             # high-level Config
  approval/             # brokers, durable inbox, grants
  checkpoint/           # memory, jsonl, sqlite stores
  eventlog/             # bus + fanout + memory, jsonl, sqlite stores
  cli/                  # command helpers and approval UX
  tool/                 # core tool interfaces, middleware, budgets, redaction
  model/                # openai, anthropic adapters
  tools/                # workspace, shell, todo, task
  subagent/             # sub-agent runtime
  planner/              # todo manager + plan/execute preset
  sandbox/              # interface, fake/containerhub backends + State helpers
  workspace/            # workspace interface + local impl
  policy/               # shell/workspace policy types
  trace/                # sinks: memory, stdout, jsonl, otel
  recorder/             # event recorder helpers
  server/               # harnesshttp + sse helpers
  adapters/             # mcp, memory, zenmind
  harness/              # loop, state machine, resume
  examples/             # runnable examples
  cmd/zenforge/         # CLI
  docs/                 # design, guides, ADRs
```

## Contributing

Issues and pull requests are welcome. The CI workflow runs
`env GOTOOLCHAIN=local go test ./...` and builds the examples. Core and other
non-adapter Go packages must not couple to `agent-platform` or ZenMind
branding. `adapters/zenmind` may document protocol provenance, but its imports
are AST-checked to reject the platform module and all `internal` packages.

Before opening a PR, run:

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./examples/...
```

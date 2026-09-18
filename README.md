<div align="center">

# ZenForge

**Production-first Go agent runtime for long-running, tool-using, observable,
and recoverable agents.**

<p>
  <a href="https://feiyu912.github.io/zenforge/"><img src="https://img.shields.io/badge/docs-GitHub%20Pages-4285F4?style=for-the-badge&logo=readthedocs&logoColor=white" alt="Documentation"></a>
  <a href="https://github.com/feiyu912/zenforge/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/feiyu912/zenforge/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI status"></a>
  <a href="https://pkg.go.dev/github.com/feiyu912/zenforge"><img src="https://pkg.go.dev/badge/github.com/feiyu912/zenforge.svg" alt="Go Reference"></a>
  <a href="https://github.com/feiyu912/zenforge/releases/tag/v0.1.0"><img src="https://img.shields.io/github/v/tag/feiyu912/zenforge?style=for-the-badge&label=release&color=0a9396" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue?style=for-the-badge" alt="Apache-2.0 license"></a>
</p>

<a href="https://feiyu912.github.io/zenforge/tutorial/">Tutorial</a> ·
<a href="https://feiyu912.github.io/zenforge/quickstart/">Quickstart</a> ·
<a href="https://feiyu912.github.io/zenforge/concepts/">Concepts</a> ·
<a href="https://feiyu912.github.io/zenforge/sdk-guide/">SDK guide</a> ·
<a href="https://pkg.go.dev/github.com/feiyu912/zenforge">API</a> ·
<a href="CHANGELOG.md">Changelog</a>

</div>

ZenForge is a batteries-included agent harness for Go services. A single
`zenforge.Agent` runs real multi-step work, with a replaceable adapter for
every concern — model, tools, workspace, planner, checkpoint store, event log,
trace sink, approval broker, sandbox, and HTTP/SSE edge. **Resume is
first-class, not bolted on**: the state machine checkpoints every boundary, so
a crashed service picks runs back up exactly where they stopped.

It is **not** a Go clone of LangChain. The goal is a small, opinionated runtime
you can embed in a backend, a CLI, a desktop app, or a gateway — instead of
pulling in a Python agent framework and a sidecar process to babysit it.

The framework owns the loop; **your application owns the choices**:

| Framework owns | Application owns |
| --- | --- |
| Agent loop, streaming, tool dispatch, approval lifecycle, checkpointing, resume, event log, trace redaction | Model, tools, approval broker, sandbox backend, workspace, event/checkpoint stores, trace sink, sub-agent roster, auth and tenancy |

The boundary is one struct: `zenforge.Config`. Every adapter point is a Go
interface you can satisfy, wrap, mock, or leave `nil`.

## Why

Most agent frameworks target notebooks. ZenForge targets services:

- **Durable runs** — checkpoints at every boundary, resume after crashes, fork
  and time-travel through run history.
- **Observable execution** — typed event stream with JSONL/SQLite/OTel sinks,
  and fail-closed event-log writes: unrecorded progress is never published.
- **Replaceable parts** — swap models, stores, transports, even the planner,
  without rewriting the loop.
- **Security by construction** — deny-by-default shell policy built on a
  complete Bash AST, four OS-level sandbox backends, SSRF-safe web tools, and
  fail-closed approval and file-policy paths.
- **Small public surface** — a focused root package for construction, tasks,
  results, and events; replaceable interfaces live in focused subpackages.

## At a glance

| | |
| --- | --- |
| **~114k** lines of Go across **~100** packages | **1,500+** test functions, race + vet gated in CI |
| **81** Architecture Decision Records | **100+** pages of [documentation](https://feiyu912.github.io/zenforge/) |
| Go **1.26** only, zero cgo (pure-Go SQLite) | Apache-2.0, third-party attributions tracked in-repo |

```text
 ┌────────────────────────────── your application ──────────────────────────────┐
 │  model client · tools · approval policy · sandbox · workspace · store paths  │
 └──────────────────────────────────┬───────────────────────────────────────────┘
                                    │ zenforge.Config (interfaces at every seam)
 ┌──────────────────────────────────▼───────────────────────────────────────────┐
 │                              zenforge.Agent                                  │
 │                       Run · Stream · Resume · Steer                          │
 ├──────────────────────────────────────────────────────────────────────────────┤
 │                             harness.Runner core                              │
 │            state machine · step boundaries · durable checkpoints             │
 ├───────────────┬───────────────┬────────────────┬─────────────────────────────┤
 │   eventlog    │   checkpoint  │    approval    │  trace (memory·stdout·      │
 │  memory·jsonl │  memory·jsonl │  broker·inbox  │   jsonl·OpenTelemetry)      │
 │   ·sqlite     │   ·sqlite     │  ·grants       │                             │
 └───────────────┴───────────────┴────────────────┴─────────────────────────────┘
   server/harnesshttp (detached runs · SSE · web console)     cmd/zenforge (CLI)
   subagents · workflow engine · Agent Skills · MCP · jobs · goals · time travel
```

## Quick look

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
resume reuses it without duplication. In `plan_execute` mode, only the
planning stage receives the conversation history.

## Install

```bash
go get github.com/feiyu912/zenforge@main
go install github.com/feiyu912/zenforge/cmd/zenforge@main
```

Go 1.26 only: both local development and CI require a Go 1.26.x toolchain
(CI sets `GOTOOLCHAIN=local` and rejects other versions). The core uses the
OpenTelemetry SDK, pure-Go SQLite via `modernc.org/sqlite` (no cgo), and
`mvdan.cc/sh/v3` for structural shell safety analysis.

## Command line

The same runtime ships as a durable CLI. Every capability is observable:
events, checkpoints, approvals, and traces land under the workspace.

```bash
zenforge init                                   # writes zenforge.json (JSON config only)
zenforge run --config zenforge.json "Analyze this repo"
zenforge code --config zenforge.json ./repo "Review and improve this codebase"
zenforge exec --json "Summarize the open changes"   # headless one-shot, JSONL events
zenforge resume run_123                         # continue a durable checkpoint
zenforge events run_123                         # replay the event history
zenforge fork run_123 --at 42                   # branch a child run from any boundary
zenforge runs                                   # list durable run summaries
zenforge grants list                            # inspect standing approval rules
zenforge schedule add --spec "every 1h" --task "Review the open changes"
zenforge mcp-server                             # serve ZenForge itself over MCP
zenforge goal "Objective"                       # persisted goal, driven round by round
zenforge ralph "Objective"                      # fresh agents over a shared workspace
zenforge serve                                  # HTTP runtime + local web console
```

Provider selection is protocol-shaped, not vendor-shaped: `--provider openai`
for OpenAI-compatible Chat Completions, `--provider anthropic` for
Anthropic-compatible Messages. Other vendors stay as `--base-url` overrides
instead of becoming new core provider names, and SDK users can pass any
application-owned `model.Model`, so custom gateways and test models live
outside the harness. `--plan` starts a read-only plan mode; `--sandbox
none|seatbelt|bwrap|landlock|docker` confines shell execution; `--jobs`,
`--goals`, `--memory`, `--hooks`, and `--review` opt into the runtime tool
families. CLI failures use stable exit codes: `0` success, `1` runtime error,
`2` invalid config or usage, `3` cancellation, `4` approval rejection, `5`
unsupported resume state.

See [Quickstart](https://feiyu912.github.io/zenforge/quickstart/) and the
[configuration reference](https://feiyu912.github.io/zenforge/config-reference/).

## Capabilities

### Durable runtime

- Single `zenforge.Agent` with `Stream`, `Run`, `Resume`, and in-process
  `Steer`; the root loop delegates to the independently testable
  `harness.Runner` state machine.
- Model stream drafts are checkpointed before publication. A crash supersedes
  the interrupted attempt and restarts the same logical step without
  committing partial text, tool calls, or usage twice.
- `react`, `oneshot`, and `plan_execute` modes (with a built-in todo
  manager), persisted across resume.
- Durable event log and checkpoint stores: memory, JSONL, SQLite. JSONL stores
  reject path-like run IDs, serialize writers across processes with advisory
  `flock`, and recover interrupted saves through a pending journal.
- **Time travel**: `fork <run>` starts a child run from any checkpoint
  boundary; `revert --to <seq>` rewinds without ever truncating history.

### Approval & human-in-the-loop

- Run-scoped pending broker plus optional durable approval inboxes (memory or
  SQLite): the waiting checkpoint and the registered request are saved before
  `approval.requested` is emitted, HTTP submit commits the decision before
  returning, and a resumed waiter can consume a decision submitted by another
  process.
- Approval requests pause durably — the model can never continue past a risky
  tool without a decision.
- Grants by exact fingerprint or rule key, isolated by tenant/subject, with
  TTL and revocation: a standing grant is the rule, not a replayable token.
- A durable `ask_user` tool delivers structured questions with options and
  multi-select through the same approval channel.
- Lifecycle hooks (`PreToolUse`/`PostToolUse`/`SessionStart`/`Stop`) run user
  commands around the loop; a malformed hook decision fails closed.

### Sub-agents & workflows

- Checkpoint-aware `task` tool: children resume independently, terminal
  children are reused, cancelled children propagate honestly.
- Child progress streams into the parent as real-time `subtask.event` records;
  host-owned limits drive the advertised schema and cannot be widened by the
  model. Nested delegation requires explicit opt-in plus a finite depth.
- `workflow/` executes JavaScript orchestration scripts in-process —
  `agent()` / `parallel()` / `pipeline()` / `phase()` with concurrency caps,
  cooperative cancellation, and JSON-Schema-checked structured results.

### Sandboxing & security

- Shell policy runs the complete platform Bash AST with security classifiers:
  dangerous or ambiguous forms are hard-blocked, redirections and complex
  structures route to approval, and every command in a chain must satisfy the
  allowlist — deny by default.
- Four confinement backends: macOS **Seatbelt**, Linux **bubblewrap**, Linux
  **Landlock + seccomp** (in-process, re-exec helper), and **Docker** (no
  network by default). An unavailable backend fails with `sandbox_unavailable`
  instead of running unsandboxed.
- A model-visible escalation ladder: permission requests never bypass the
  allowlist, always require a fresh approval under a namespaced fingerprint,
  and run the approved call locally once.
- Workspace file policy: read/write roots, run-scoped read-before-write
  snapshots with SHA-256 stale-write detection, symlink-escape rejection.
- `web_fetch` pins DNS-resolved addresses to public unicast answers, so DNS
  rebinding cannot reach private services; every fetched page is framed as
  untrusted data, not instructions.

### Context & memory

- `compaction/`: pressure compaction at step boundaries and forced overflow
  recovery on provider context-window errors, with durable history and
  `compaction.*` events.
- `instructions/`: hierarchical AGENTS.md-style instruction discovery with a
  frozen environment-context block; both persist into run state, so resume
  replays the exact prompting.
- Provider rate-limit headers normalize into a token meter;
  `get_context_remaining` reports the live budget to the agent.
- Turn-diff tracking renders unified diffs at every turn boundary; oversized
  tool output spills to a private on-disk store with a UTF-8-safe preview;
  repeat-guard middleware counts loops and answers with escalating reminders.
- Durable cross-run memories: `--memory` keeps learnings in readable markdown
  and injects them into later runs; `--memory-distill` extracts them with one
  model call per finished run.

### Models, tools & extensibility

- OpenAI-compatible and Anthropic adapters with streaming text and tool calls;
  reasoning replay where the provider requires it, on its own
  `model.reasoning` event.
- `modelretry/`: a fail-closed failure taxonomy with jittered backoff,
  Retry-After support, superseded-attempt chaining, and a stream idle watchdog.
- Typed tools infer JSON Schema from Go structs; middleware composes panic
  recovery, repeat guards, spilling, budgets, timeouts, and redaction.
- **Agent Skills**: filesystem `SKILL.md` catalogs with progressive
  disclosure — descriptors first, instructions and indexed resources on
  demand, every byte carrying SHA-256 identity and provenance. Skills are
  instruction packages, not executable tools.
- **MCP both directions**: configure stdio servers whose tools arrive as
  `mcp__<server>__<tool>` behind per-tool approval rules, or serve ZenForge
  itself as an MCP server (tools, resources, prompts, progress, elicitation,
  detachable runs).
- `apply_patch` with the four-pass context search, plus a complete workspace
  tool family: read/list/glob/grep/write/edit, `view_image`, `present`
  deliverables, deferred loading through `tool_search`.

### Serving & operations

- `server/harnesshttp`: canonical runtime assembly for detached
  start/resume/status/list/attach/cancel — durable SSE reconnect with
  `Last-Event-ID`, replay-to-live attach, explicit cross-replica
  cancellation, active-run admission, and stale-run recovery.
- Optional run registries (memory/SQLite) share claims, leases, and durable
  status across managers; applications still own auth, routes, and lifecycle.
- HMAC-signed webhooks can start runs without holding a credential; a local
  web console rides in `zenforge serve`.
- Background jobs (`exec_command`/`write_stdin`/`job_output`) run detached,
  including pseudo-terminals for interactive programs, with bounded head+tail
  buffers that report — never hide — dropped output.
- Trace sinks: memory, stdout, JSONL, OpenTelemetry spans; redaction helpers
  keep secrets out of durable events.

### Platform adapters

- `adapters/zenmind` — platform catalog/session DTOs with host-owned
  resolution, strict history conversion, fail-closed routing, run-scoped
  projection, and event-driven approval correlation. Wire goldens are pinned
  against captured platform fixtures. See the
  [ZenMind adapter guide](https://feiyu912.github.io/zenforge/zenmind-adapter-guide/).
- `adapters/mcp` — bidirectional MCP: a stdio client that bridges remote tools
  behind approval rules, and a server mode that serves runs, resources, and
  prompts. OAuth stays with the host. See the
  [MCP guide](https://feiyu912.github.io/zenforge/mcp-adapter-guide/).
- `adapters/memory` — scoped memory augmentation into normalized tasks.

## Examples

Each example is a runnable Go program under [`examples/`](examples/). The SDK
embedded example runs locally without an API key; provider-backed examples need
`OPENAI_API_KEY` or an OpenAI-compatible endpoint.

| Example | What it shows |
| --- | --- |
| [`sdk-embedded-agent`](examples/sdk-embedded-agent) | Embed ZenForge in a Go service; runs without an API key. |
| [`harness-agent`](examples/harness-agent) | Env provider + Agent Skills + typed tool + HITL + Docker sandbox. |
| [`http-harness-agent`](examples/http-harness-agent) | Loopback-only HTTP service: detached runs, SSE, durable SQLite stores, webhooks. |
| [`simple-tool-agent`](examples/simple-tool-agent) | Minimal model + tool loop. |
| [`code-review-agent`](examples/code-review-agent) | Workspace + shell with approval. |
| [`repo-refactor-agent`](examples/repo-refactor-agent) | Long task with checkpoints and resume. |

The flagship assembly end to end:

```bash
export ZENFORGE_PROVIDER=anthropic
export ZENFORGE_MODEL=MiniMax-M3
export ZENFORGE_API_KEY=...
export ZENFORGE_BASE_URL=https://api.minimax.io/anthropic

go run ./examples/harness-agent -question \
  "Load the project skill, inspect this project, and prove the shell runs in Docker."
```

## Documentation

The full site is built from [`docs/`](docs/) with MkDocs Material and hosted
on GitHub Pages at **[feiyu912.github.io/zenforge](https://feiyu912.github.io/zenforge/)**.

| Start here | Deep dives |
| --- | --- |
| [Tutorial](https://feiyu912.github.io/zenforge/tutorial/) — a working agent in 15 minutes | [Architecture](https://feiyu912.github.io/zenforge/architecture/) |
| [Quickstart](https://feiyu912.github.io/zenforge/quickstart/) | [Harness state machine](https://feiyu912.github.io/zenforge/harness-state-machine/) |
| [Concepts](https://feiyu912.github.io/zenforge/concepts/) | [Approvals](https://feiyu912.github.io/zenforge/approval-guide/) · [Checkpoint & resume](https://feiyu912.github.io/zenforge/checkpoint-resume-guide/) |
| [SDK guide](https://feiyu912.github.io/zenforge/sdk-guide/) | [Compaction](https://feiyu912.github.io/zenforge/compaction-guide/) · [Sub-agents](https://feiyu912.github.io/zenforge/subagent-guide/) · [Planner](https://feiyu912.github.io/zenforge/planner-guide/) |
| [Tool authoring](https://feiyu912.github.io/zenforge/tool-authoring-guide/) | [Sandboxing](https://feiyu912.github.io/zenforge/sandbox-guide/) · [Security](https://feiyu912.github.io/zenforge/security-guide/) |
| [Providers](https://feiyu912.github.io/zenforge/provider-guide/) | [HTTP server](https://feiyu912.github.io/zenforge/server-http-guide/) · [SSE](https://feiyu912.github.io/zenforge/server-sse-guide/) · [Deployment](https://feiyu912.github.io/zenforge/deployment-guide/) |
| [Agent Skills](https://feiyu912.github.io/zenforge/agent-skills-spec/) | [MCP](https://feiyu912.github.io/zenforge/mcp-adapter-guide/) · [Memory](https://feiyu912.github.io/zenforge/memory-adapter-guide/) · [ZenMind](https://feiyu912.github.io/zenforge/zenmind-adapter-guide/) |
| [Configuration reference](https://feiyu912.github.io/zenforge/config-reference/) | [Failure modes](https://feiyu912.github.io/zenforge/failure-modes/) · [Limitations](https://feiyu912.github.io/zenforge/limitations/) · [Tracing](https://feiyu912.github.io/zenforge/trace-guide/) |

Eighty-one architecture decision records live in
[`docs/adr/`](docs/adr/) and on the
[site](https://feiyu912.github.io/zenforge/adr/).

## Project status

`v0.1.0` (2026-05-30) is the first usable release candidate; the `main` branch
carries additional v0.1.x capabilities on top of the tag without intentional
breaking changes. The highlights of `main`:

- Context management: compaction with overflow recovery, model-retry
  taxonomy, token meter, hierarchical AGENTS.md instructions, turn diffs.
- Orchestration: in-process JavaScript workflow engine, checkpoint-aware
  sub-agents, plan mode, goals, Ralph, durable background jobs and PTYs.
- Safety: Seatbelt, bubblewrap, Landlock+seccomp, and Docker sandbox
  backends; Bash-AST shell policy; SSRF-safe web tools; durable approvals
  with grants and `ask_user`.
- Protocol surface: MCP client and server modes, signed webhooks, detached
  HTTP lifecycle with cross-replica registries, a local web console.
- Time travel: fork from any checkpoint boundary; revert with full history.

The complete [CHANGELOG](CHANGELOG.md) tracks every change since v0.1.0, and
the [release notes](https://feiyu912.github.io/zenforge/release-notes-v0.1/)
cover the 0.1 feature set.

**Not in MVP** — see [limitations](docs/limitations.md) for the full list:

- Resume replaces an interrupted model attempt from its committed prompt
  boundary; it does not use a provider-native mid-token cursor.
- MCP serves runs, resources, prompts, elicitation, and sampling as a server;
  as a client it bridges remote tools, while remote resources/prompts and
  OAuth stay with the host platform.
- OpenTelemetry exporter setup stays in host services; CLI config is JSON only.
- JSONL durability uses Unix advisory file locks (`flock`).
- Nested sub-agents are blocked by default; the Container Hub sandbox is
  optional and beta.

## Repository layout

```text
zenforge/
  agent.go              # zenforge.Agent + Config + Task + Result
  task.go               # normalized task model
  events.go             # public event contract
  config.go             # high-level Config
  harness/              # loop, state machine, resume
  approval/             # brokers, durable inbox, grants
  checkpoint/           # memory, jsonl, sqlite stores
  eventlog/             # bus + fanout + memory, jsonl, sqlite stores
  model/                # openai, anthropic adapters + provider factory
  modelretry/           # failure taxonomy, backoff, Retry-After
  compaction/           # context pressure + overflow compaction
  instructions/         # hierarchical AGENTS.md-style discovery
  tool/                 # interfaces, middleware, budgets, redaction, spill, repeat guard
  tools/                # workspace, shell, patch, todo, task, askuser, present, web, jobs-facing
  skill/                # Agent Skill bundles + skillfs catalogs
  planner/              # todo manager + plan/execute preset
  subagent/             # sub-agent runtime
  workflow/             # in-process JavaScript workflow engine
  goals/                # persisted goal lifecycle
  jobs/                 # background jobs + PTY sessions
  schedule/             # recurring tasks
  hooks/                # lifecycle hook engine
  review/               # guardian review
  memory/               # durable cross-run memories
  commands/             # canned /commands templates
  sandbox/              # docker, fake, containerhub backends + State helpers
  sandbox/seatbelt      # macOS SBPL profile
  sandbox/bwrap         # Linux bubblewrap argument planning
  sandbox/landlock      # Landlock planner
  sandbox/linuxsandbox  # Landlock+seccomp re-exec helper
  sandbox/seccomp       # classic BPF network filter
  safety/               # bashast + bashsec: complete Bash AST classifiers
  policy/               # shell/workspace policy types, sandbox-denial classifier
  workspace/            # workspace interface + local impl
  applypatch/           # apply-patch parser + applier
  diff/                 # bounded Myers unified diff
  web/                  # SSRF-safe fetch transport (address pinning)
  configlayer/          # layered config stack, profiles, managed requirements
  redact/               # secret string that redacts every formatting path
  prompt/               # ordered system-prompt sections + variables
  sessiontitle/         # terminal-safe session-title derivation
  cli/ cmd/zenforge/    # command helpers, approval UX, binary
  server/               # harnesshttp + SSE helpers
  adapters/             # mcp (client+server), memory, zenmind
  trace/ recorder/      # sinks: memory, stdout, jsonl, otel; ordered writes
  examples/             # runnable examples (one dir per program)
  docs/                 # guides, ADRs, and the docs site source
```

## Contributing

Issues and pull requests are welcome. The CI workflow runs
`env GOTOOLCHAIN=local go test ./...`, race tests, `go vet`, and builds the
examples. Core and other non-adapter Go packages must not couple to
`agent-platform` or ZenMind branding; `adapters/zenmind` may document protocol
provenance, but its imports are AST-checked to reject the platform module and
all `internal` packages.

Before opening a PR, run:

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./examples/...
env GOTOOLCHAIN=local go test ./docs/...
rg -n 'zenforge\.ya?ml|```ya?ml' README.md docs   # must return nothing
git diff --check
```

## License

ZenForge is licensed under the [Apache License 2.0](LICENSE). Third-party
material that ZenForge serves or derives from — and the attributions their
licenses require — is recorded in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

ZenForge's design vocabulary owes a debt to prior art: the DeepSeek Harness
(MIT) and OpenAI Codex CLI (Apache-2.0), whose observable behaviors were
studied and independently reimplemented in idiomatic Go; the
[reference parity plan](https://feiyu912.github.io/zenforge/reference-parity-plan/)
maps every borrowed concept to its ZenForge status.

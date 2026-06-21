# ZenForge Product Roadmap

This is the end-to-end plan from project start to MVP to the first usable
product version.

## Product Definition

ZenForge is a Go-native agent harness for production systems.

It should provide:

- a default long-running agent;
- model streaming;
- tool calling;
- todo/planning;
- workspace context;
- safe shell/file tools;
- approval/HITL hooks;
- sub-agent delegation;
- event streaming;
- durable checkpoints;
- resume;
- CLI and SDK entrypoints.

It should not become a Go clone of LangChain. ZenForge should feel like a
runtime: fewer abstractions, stronger execution guarantees, better deployment
fit for Go backends.

## Target Users

Primary users:

- Go backend engineers building internal agent services;
- teams that want agents inside existing Go systems;
- private deployment users who care about observability, safety, and resume;
- ZenMind platform itself as the first serious downstream consumer.

Secondary users:

- CLI users who want a local code/research agent;
- teams that need sandboxed tool execution;
- developers who want a small alternative to Python-heavy agent runtimes.

## Product Stages

```text
S0  Project foundation
S1  Durable event/checkpoint core
S2  Tool runtime
S3  Safety and workspace
S4  Minimal agent harness
S5  Planner/todo runtime
S6  Approval/HITL
S7  Sub-agent runtime
S8  Sandbox adapter
MVP Public developer preview
V0.1 First usable product
V0.2 Production hardening
```

### Current Status

| Stage | Status and repository evidence |
| --- | --- |
| S0-S6 | Implemented and covered by the package tests mapped in `docs/mvp-validation.md`. |
| S7 | Implemented for runtime subtask streaming, bounded parallel execution, child checkpoint resume, and default-denied nesting; see the named sub-agent tests in `docs/mvp-validation.md`. |
| S8 | Local/fake backends and the Container Hub beta adapter are contract-tested with local HTTP servers. A real Container Hub service has not been exercised and remains external acceptance. |
| MVP | Repository-scoped acceptance is implemented and test-mapped. Provider-backed examples and deployment integration remain environment-dependent smoke tests. |
| V0.1 | `v0.1.0` was tagged. Repository wire goldens now cover the ZenMind DTO/projector/approval/event-line boundary at `agent-platform@1893edb5`; the external engine/feature-flag/SSE/WS/fallback spike is still open. |
| V0.2 | Partially implemented hardening, including SQLite soak coverage, Go 1.26-only CI, JSONL crash/concurrency safety, typed tools, and bounded shell/Hub responses. This roadmap stage is not declared complete. |

Completion in this table means repository code plus automated test evidence. It
does not claim production acceptance by external `agent-platform` or Container
Hub deployments.

## S0: Project Foundation

Goal:

Create a standalone repo with clear boundaries before porting implementation
logic.

Deliverables:

- repo skeleton;
- public package layout;
- module path;
- high-level API draft;
- extraction map from `agent-platform`;
- preparation plan;
- no dependency on `agent-platform/internal`.

Already started:

- `README.md`
- `docs/vision.md`
- `docs/current-project-mapping.md`
- `docs/architecture.md`
- `docs/mvp-scope.md`
- `docs/extraction-plan.md`
- `docs/preparation-plan.md`
- interface stubs for model/tool/workspace/checkpoint/trace.

Acceptance:

- `go test ./...` passes;
- repo can be pushed independently;
- team agrees not to copy large packages before S1/S2 boundaries are stable.

## S1: Durable Event And Checkpoint Core

Goal:

Make runtime state durable before extracting the agent loop.

Why first:

The current platform has trace/replay but not true resume. If ZenForge copies
the loop before extracting event/run-control shapes and adding a thin durable
checkpoint adapter, it will inherit this weakness.

Deliverables:

- `Event`;
- `EventType`;
- `RunEventLog`;
- `CheckpointStore`;
- `RunState`;
- `RunControlSnapshot`;
- `AssemblerSnapshot`;
- `ToolCallSnapshot`;
- `PlanSnapshot`;
- in-memory event bus as live fanout;
- JSONL store for local development;
- tests for append/read/replay/checkpoint load/save.

Acceptance:

- events can be appended and replayed by `runID` and sequence;
- checkpoint can be saved after each step;
- checkpoint schema can represent waiting approval, pending tool call, and plan
  state;
- event bus is not the source of truth.

## S2: Tool Runtime

Goal:

Build the public tool system and middleware layer.

Deliverables:

- `tool.Tool`;
- `tool.Registry`;
- typed tool helper;
- JSON schema handling;
- timeout middleware;
- retry middleware;
- budget middleware;
- audit/event emission;
- normalized `ToolResult`;
- tool definition loader or builder.

Source inspiration:

- `agent-platform/internal/tools/tool_router.go`;
- `agent-platform/internal/tools/tool_executor.go`;
- `agent-platform/internal/contracts/interfaces.go`.

Do not include yet:

- memory tools;
- session search;
- artifact gateway;
- desktop bridge;
- ZenMind frontend tools.

Acceptance:

- user can define a typed Go tool;
- tool can be called by name with JSON input;
- timeout and retry behavior is tested;
- tool calls emit stable events.

## S3: Safety And Workspace

Goal:

Make file and shell tools useful but safe.

Deliverables:

- `workspace.Workspace`;
- local workspace implementation;
- read/list/grep/write tools;
- read allowlist;
- write allowlist;
- read-before-write snapshot;
- shell tool;
- command allowlist;
- max output size;
- timeout;
- `bashsec` port or equivalent command review;
- file access approval plans.

Source inspiration:

- `agent-platform/internal/bashast`;
- `agent-platform/internal/bashsec`;
- `agent-platform/internal/filetools`;
- `agent-platform/internal/tools/tool_file.go`;
- `agent-platform/internal/tools/tool_grep.go`;
- `agent-platform/internal/tools/tool_bash.go`.

Acceptance:

- file tools cannot escape configured roots;
- shell tool can be locked down;
- risky operations can produce approval requests instead of running directly;
- local workspace tests cover symlink/path traversal cases.

## S4: Minimal Agent Harness

Goal:

Run a real model/tool loop in ZenForge.

Deliverables:

- `harness.Runner`;
- OpenAI-compatible model adapter;
- model streaming parser;
- tool call parsing;
- tool result message injection;
- max steps;
- final no-tool answer turn;
- cancellation;
- usage accounting;
- checkpoint after model turn/tool result;
- `Agent.Stream`;
- `Agent.Run`.

Source inspiration:

- `agent-platform/internal/llm/llm_engine.go`;
- `agent-platform/internal/llm/run_stream*.go`;
- `agent-platform/internal/llm/protocol*.go`;
- `agent-platform/internal/llm/mode.go`.

Important boundary:

Do not directly copy `LLMAgentEngine` as public API. It knows too much about
ZenMind config, registry, prompt, frontend tools, sandbox, and sessions.

Acceptance:

- agent can call a local tool and continue;
- stream emits `model.delta`, `tool.call`, `tool.result`, `run.done`;
- run can be interrupted;
- run creates checkpoints;
- public API does not import ZenMind platform packages.

## S5: Planner And Todo Runtime

Goal:

Make long tasks first-class.

Deliverables:

- todo state;
- `todo_write`;
- `todo_read`;
- `todo_update`;
- plan/execute/summary preset;
- task lifecycle events;
- per-task max work rounds;
- final summary turn.

Source inspiration:

- `agent-platform/internal/llm/plan_execute.go`;
- `agent-platform/internal/tools/tool_plan.go`.

Compatibility aliases:

- `plan_add_tasks` -> `todo_write`;
- `plan_get_tasks` -> `todo_read`;
- `plan_update_task` -> `todo_update`.

Acceptance:

- agent creates a todo list before executing a long task;
- each task reaches terminal status;
- failed task behavior is deterministic;
- todo updates are checkpointed and emitted as events.

## S6: Approval And HITL

Goal:

Decouple approval from ZenMind frontend/server protocol.

Deliverables:

- `approval.Request`;
- `approval.Decision`;
- `approval.Broker`;
- policy middleware;
- timeout behavior;
- approval event types;
- simple CLI approval broker.

Source inspiration:

- `agent-platform/internal/hitl`;
- `agent-platform/internal/llm/run_stream_security_approval.go`;
- `agent-platform/internal/llm/run_stream_hitl_submit.go`.

Adapter boundary:

ZenMind can map core approval events to:

- `awaiting.ask`;
- `request.submit`;
- `awaiting.answer`;
- `/api/submit`;
- WebSocket push notifications.

Acceptance:

- risky shell/file operation can pause;
- user decision resumes the same run;
- approval state is checkpointed;
- CLI can approve/reject.

## S7: Sub-Agent Runtime

Goal:

Support delegated child tasks without pulling in ZenMind server internals.

Deliverables:

- `SubAgentSpec`;
- `SubAgentOrchestrator`;
- `task` or `agent_invoke` tool;
- child event routing;
- child result aggregation;
- nested invocation guard;
- subtask checkpoint state.

Source inspiration:

- `agent-platform/internal/llm/run_stream_tools.go`;
- `agent-platform/internal/server/frame_orchestrator.go`;
- `agent-platform/internal/llm/orchestration.go`.

Do not copy directly:

- chat/resource ticket logic;
- proxy behavior;
- server route coupling;
- system-init persistence.

Acceptance:

- main agent can launch 1 to N child tasks;
- child results are injected back into parent;
- child events are visible in parent stream;
- failed child task returns structured failure;
- nested sub-agent invocation is blocked or explicitly configured.

## S8: Sandbox Adapter

Goal:

Support sandboxed execution without making Container Hub part of core.

Deliverables:

- `sandbox.Sandbox`;
- `sandbox.Session`;
- Container Hub adapter;
- environment prompt provider;
- sandbox shell tool backend.

Source inspiration:

- `agent-platform/internal/sandbox`;
- `agent-container-hub/internal/sandbox`;
- `agent-container-hub` HTTP API.

Acceptance:

- shell tool can run locally or in sandbox;
- sandbox session is scoped by run/subtask;
- environment prompt can be injected by adapter;
- core remains usable without Container Hub.

## MVP Developer Preview

MVP is reached when a Go developer can run:

```bash
zenforge run "Analyze this repository and propose a refactor plan"
```

MVP includes:

- OpenAI-compatible model adapter;
- local workspace;
- read/list/grep/write tools;
- safe shell tool;
- todo/planner;
- event stream;
- JSONL trace/checkpoint;
- resume from supported checkpoint boundaries;
- sub-agent task tool;
- MCP tool adapter;
- memory augmentation adapter;
- CLI approval for shell/file risk;
- CLI `events` and `runs` inspection commands;
- one code-review/refactor example.

MVP excludes:

- full ZenMind memory system;
- MCP resources, prompts, sampling, discovery, and OAuth;
- desktop bridge;
- mobile gateway;
- multi-tenant server APIs;
- schedule execution;
- skill marketplace.

MVP acceptance:

- `go test ./...` passes;
- `zenforge run` works locally;
- example repo analysis completes;
- tool calls and todo updates stream visibly;
- checkpoints are written;
- restart/resume works for supported states;
- docs explain limitations honestly.

## V0.1 First Usable Product

V0.1 should be the first version someone outside the project can reasonably try.

V0.1 product shape:

- Go SDK;
- CLI;
- OpenAI-compatible provider;
- local tools;
- planner/todo;
- approval broker;
- JSONL/SQLite checkpoint store;
- examples;
- adapter hooks for ZenMind.

V0.1 docs:

- quickstart;
- API guide;
- tool authoring guide;
- checkpoint/resume guide;
- approval guide;
- security guide;
- migration notes from `agent-platform`.

V0.1 acceptance:

- a clean clone can run examples;
- no dependency on private ZenMind internals;
- public APIs are documented;
- safety defaults are conservative;
- checkpoint format has versioning.

## V0.2 Production Hardening

Focus:

- stronger resume semantics;
- SQLite checkpoint store;
- OpenTelemetry trace sink;
- Container Hub adapter;
- MCP adapter;
- memory adapter;
- server/SSE helper package;
- benchmark and soak tests (`BenchmarkAgentRunStaticModel`,
  `TestSQLiteDurableRunSoak`);
- failure-mode documentation (`docs/failure-modes.md`).

## First ZenMind Integration

After ZenForge MVP, integrate back into `agent-platform` through adapters.

Recommended path:

1. Keep current `agent-platform` runtime working.
2. Add ZenForge behind a host feature flag using fail-closed
   AgentKey/ChatID/RunID routing (`adapters/zenmind.Router`).
3. Map ZenMind catalog/session into ZenForge `RunConfig`
   (`adapters/zenmind.BuildRun`).
4. Project ZenForge events into stateful content/tool platform lifecycles
   (`adapters/zenmind.Projector`).
5. Keep the event-only platform JSONL wire as a read model
   (`zenmind.NewChatJSONLWriter`), without claiming complete Chat Storage V3.1.
6. Gradually replace current internal loop for selected agents while
   `RouteLegacy` stays available.

Do not big-bang replace the platform runtime.

Repository fixtures under `adapters/zenmind/testdata/platform` provide golden
wire evidence from `agent-platform@1893edb5`. The engine bridge, actual rollout
flag, SSE/WS delivery, and fallback E2E still require changes and tests in the
external platform repository.

## Final Initial Product Vision

The first mature product should feel like this:

```text
ZenForge SDK
  Build Go-native agents with safe tools and durable runs.

ZenForge CLI
  Run code/research/refactor tasks locally.

ZenForge Adapters
  Plug into ZenMind, Container Hub, OpenTelemetry, and server transports.
  Keep auth and tenancy at the platform boundary.

ZenForge Runtime
  Long tasks, todos, sub-agents, approvals, checkpoints, traces.
```

The core promise stays simple:

```text
One agent.
Real tools.
Visible progress.
Safe execution.
Recoverable runs.
Go-native deployment.
```

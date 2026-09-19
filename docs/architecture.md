# Architecture

ZenForge should have three layers:

```text
deep API      easy default agent for most users
harness core  replaceable runtime pieces for advanced users
adapters      optional bridges a particular product or UI needs
```

The first two layers are the framework. The third is consumed *by* them and
never the other way round: an adapter may import the core, and the core must
not import an adapter or use one's vocabulary. The DSH console host is one such
adapter, and it lives entirely in `internal/dshapi`, `internal/dshboot`,
`internal/dshmount`, `internal/dshsession`, `internal/dshstream`, `cli/`, and
the vendored console artifacts under `webui/dsh/` — none of which a framework
user has to build, import, or know about. The rule is recorded in
[ADR 0099](adr/0099-the-framework-core-and-the-console-adapter-are-separate-layers.md)
and enforced by `console_boundary_test.go`; what that adapter answers today is
[the console coverage ledger](dsh-console-coverage.md).

## Current Package Layout

```text
zenforge/
  agent.go
  config.go
  task.go
  events.go

eventlog/
  interface.go
  bus.go
  memory/
  jsonl/
  sqlite/

harness/
  runner.go
  state.go

recorder/
  recorder.go

model/
  interface.go
  openai/
  anthropic/

tool/
  interface.go
  registry.go
  middleware/
  jsonschema/

tools/
  workspace/
  shell/
  todo/
  task/

workspace/
  interface.go
  local/

planner/
  todo.go

subagent/
  spec.go
  orchestrator.go

approval/
  broker.go
  request.go
  cli/

checkpoint/
  memory/
  jsonl/
  sqlite/

sandbox/
  containerhub/
  fake/

trace/
  interface.go
  otel/

server/
  harnesshttp/
  sse/

adapters/
  mcp/
  memory/
  sandbox/
  zenmind/

internal/            console adapter tier (optional, not framework API)
  dshapi/            the console's method surface, in envelopes
  dshboot/           boot payload and the console's runtime config
  dshmount/          HTTP and WebSocket mounting
  dshsession/        session wiring for the console
  dshstream/         $events, session/control, session/follow

cli/
  serve.go           `zenforge serve` = a DSH console host over the HTTP harness
webui/
  dsh/               the console's vendored browser artifacts
```

## Runtime Flow

`zenforge.Agent` owns dependency assembly, durable stores, public event
persistence, approvals, and sub-agent integration. It delegates the reusable
model/tool state machine to `harness.Runner` through explicit model, tool,
checkpoint, and event hooks. This keeps the harness independently testable
without introducing an import cycle back to the root package.

```text
Task input
  ↓
Load or create run state
  ↓
Build prompt context
  ↓
Model stream
  ↓
Tool call detection
  ↓
Validate args and policy
  ↓
Maybe request approval
  ↓
Execute tool
  ↓
Write event + checkpoint
  ↓
Continue, delegate, finish, or await
```

S1 keeps durable runtime state in two separate streams:

- `checkpoint.Store` saves `checkpoint.Checkpoint` records with schema version
  `zenforge.checkpoint.v1`; this is the source of truth for resume.
- `eventlog.Store` appends public `zenforge.Event` records using the flattened
  event JSON shape extracted from `agent-platform/internal/stream`; this is the
  observable history for users, CLI, trace adapters, and tests.

The production `zenforge.Agent` does not instantiate `recorder.Recorder`. Its
production checkpoint helpers build one canonical checkpoint shape and one
canonical `checkpoint.created` payload across normal, planner, terminal, and
cancellation paths. Writes fail closed: state is saved before the checkpoint
event, and terminal state is durable before terminal success is reported. The
`recorder` package is a separately tested low-level helper with the same
checkpoint-before-event ordering, terminal phase/event validation, and
cancelled-context persistence. It does not own Agent lifecycle, resume, live
streaming, or tracing.
When a server needs live observers, `eventlog.FanoutStore` can wrap any durable
`eventlog.Store` and publish appended events to `eventlog.Bus`; replay and
resume still come from the durable stores.

## Core Interfaces

### Model

```go
type Model interface {
    Generate(ctx context.Context, req model.Request) (*model.Response, error)
    Stream(ctx context.Context, req model.Request) (<-chan model.Event, error)
}
```

`model/openai` is the first concrete adapter. It targets OpenAI-compatible Chat
Completions, sends ZenForge tools as function tool definitions, streams SSE
chunks into normalized model events, and accumulates streaming `tool_calls`
before the harness invokes tools.

### Tool

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any
    Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error)
}
```

### Workspace

```go
type Workspace interface {
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte) error
    List(ctx context.Context, path string) ([]FileInfo, error)
    Grep(ctx context.Context, query GrepQuery) ([]Match, error)
    Stat(ctx context.Context, path string) (FileInfo, error)
}
```

### checkpoint.Store

```go
type Store interface {
    Save(ctx context.Context, checkpoint checkpoint.Checkpoint) error
    Load(ctx context.Context, runID string) (*checkpoint.Checkpoint, error)
    Delete(ctx context.Context, runID string) error
}
```

### trace.Sink

```go
type Sink interface {
    Emit(ctx context.Context, event trace.Event) error
}
```

## Public Event Types

Initial stable event set:

```text
run.started
run.resumed
run.done
run.error
run.cancelled
request.steer
step.started
step.done
model.started
model.delta
model.done
tool.call
tool.result
tool.error
todo.updated
workspace.changed
approval.requested
approval.resolved
approval.expired
subtask.started
subtask.event
subtask.done
subtask.error
task.started
task.done
task.error
task.cancelled
checkpoint.created
```

The adapter now has stateful content/tool lifecycle projection and flat-wire
goldens captured from `agent-platform@1893edb5`; compatibility evidence is no
longer limited to similar event names. Downstream connection is implemented and
tested on `agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3`: the
selector fixes one engine per query across HTTP sync/async, SSE, WebSocket,
approval, attach, and fallback behavior. This does not move platform ownership
into ZenForge core. Platform `main@f6d89da` restores the bridge, selector,
routing, initialization, and rollout documentation. Deployment remains a
platform responsibility.

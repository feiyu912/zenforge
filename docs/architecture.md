# Architecture

ZenForge should have two layers:

```text
deep API      easy default agent for most users
harness core  replaceable runtime pieces for advanced users
```

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
into ZenForge core, and the branch is not yet platform `main`.

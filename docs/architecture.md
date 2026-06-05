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

The `recorder` package coordinates the write order between those streams.
When a server needs live observers, `eventlog.FanoutStore` can wrap any durable
`eventlog.Store` and publish appended events to `eventlog.Bus`; replay and
resume still come from the durable stores.

## Core Interfaces

### Model

```go
type Model interface {
    Generate(ctx context.Context, req ModelRequest) (*ModelResponse, error)
    Stream(ctx context.Context, req ModelRequest) (<-chan ModelEvent, error)
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
    Schema() JSONSchema
    Call(ctx context.Context, input json.RawMessage, call ToolCallContext) (ToolResult, error)
}
```

### Workspace

```go
type Workspace interface {
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte) error
    List(ctx context.Context, path string) ([]FileInfo, error)
    Grep(ctx context.Context, query GrepQuery) ([]Match, error)
}
```

### Checkpoint Store

```go
type CheckpointStore interface {
    Save(ctx context.Context, checkpoint Checkpoint) error
    Load(ctx context.Context, runID string) (*Checkpoint, error)
    Delete(ctx context.Context, runID string) error
}
```

### Trace Sink

```go
type TraceSink interface {
    Emit(ctx context.Context, event Event) error
}
```

## Public Event Types

Initial stable event set:

```text
run.started
run.resumed
run.done
run.error
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
subtask.started
subtask.done
checkpoint.created
```

The existing `agent-platform/internal/stream` events can be mapped to these
public names by an adapter while the old UI keeps using its current payloads.

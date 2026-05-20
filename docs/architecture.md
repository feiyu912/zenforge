# Architecture

ZenForge should have two layers:

```text
deep API      easy default agent for most users
harness core  replaceable runtime pieces for advanced users
```

## Proposed Package Layout

```text
zenforge/
  agent.go
  config.go
  task.go
  events.go

harness/
  runner.go
  loop.go
  state.go
  checkpoint.go
  resume.go
  run_control.go

model/
  interface.go
  openai/
  anthropic/
  ollama/

tool/
  interface.go
  registry.go
  middleware.go
  policy.go
  result.go

tools/
  filesystem/
  shell/
  http/
  todo/
  task/

workspace/
  interface.go
  local/
  memory/
  sandbox/

planner/
  todo.go
  plan_execute.go

subagent/
  spec.go
  orchestrator.go

approval/
  policy.go
  request.go
  decision.go

checkpoint/
  memory/
  sqlite/
  jsonl/

trace/
  interface.go
  stdout/
  otel/
  jsonl/

adapters/
  zenmind/
    catalog/
    chatstore/
    containerhub/
    memory/
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

## Core Interfaces

### Model

```go
type Model interface {
    Generate(ctx context.Context, req ModelRequest) (*ModelResponse, error)
    Stream(ctx context.Context, req ModelRequest) (<-chan ModelEvent, error)
}
```

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


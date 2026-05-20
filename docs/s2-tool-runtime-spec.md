# S2 Tool Runtime Spec

S2 builds the tool execution layer that later model loops will call.

The goal is to support safe, typed, observable tool calls without depending on
ZenMind platform DTOs.

## S2 Outcome

After S2, ZenForge should have:

- public tool definitions;
- registry and lookup;
- typed tool helper;
- JSON argument validation;
- middleware chain;
- timeout/retry/budget policies;
- normalized results;
- event emission hooks;
- tests for common tool behavior.

No model loop is required yet.

## Design Principles

1. Tools are normal Go values.
2. Tool calls are explicit JSON calls.
3. Middleware owns policy.
4. Built-in tools are optional.
5. Tool runtime must not import ZenMind `api` or `contracts`.
6. Tool results must be safe to feed back into a model.

## Package Plan

```text
tool/
  interface.go
  definition.go
  registry.go
  invoker.go
  middleware.go
  errors.go

tool/jsonschema/
  infer.go

tools/
  typed.go
```

Later stages add:

```text
tools/todo/
tools/workspace/
tools/shell/
tools/http/
tools/task/
```

## Core Types

### Tool

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any
    Call(ctx context.Context, input json.RawMessage, call Context) (Result, error)
}
```

### Definition

```go
type Definition struct {
    Name        string         `json:"name"`
    Description string         `json:"description"`
    Schema      map[string]any `json:"schema"`
    Metadata    map[string]any `json:"metadata,omitempty"`
}
```

### Call

```go
type Call struct {
    ID        string          `json:"id"`
    RunID     string          `json:"runId"`
    Name      string          `json:"name"`
    Arguments json.RawMessage `json:"arguments"`
    Metadata  map[string]any  `json:"metadata,omitempty"`
}
```

### Context

```go
type Context struct {
    RunID      string
    Step       int
    ToolCallID string
    Deadline   time.Time
    Metadata   map[string]any
}
```

### Result

```go
type Result struct {
    Output     string         `json:"output,omitempty"`
    Structured map[string]any `json:"structured,omitempty"`
    Error      string         `json:"error,omitempty"`
    ExitCode   int            `json:"exitCode"`
    Metadata   map[string]any `json:"metadata,omitempty"`
}
```

Rules:

- `ExitCode == 0` means success unless `Error` is non-empty.
- `Output` is the model-facing string.
- `Structured` is for SDK/server consumers.
- Large outputs should be truncated by middleware, not by every tool.

## Registry

```go
type Registry interface {
    Register(tool Tool) error
    Lookup(name string) (Tool, bool)
    Definitions() []Definition
}
```

Default behavior:

- names are case-sensitive in display;
- lookup is case-insensitive by normalized key;
- duplicate normalized names are rejected;
- definitions are returned in stable sorted order.

## Invoker

```go
type Invoker interface {
    Invoke(ctx context.Context, call Call) (Result, error)
}

type Middleware func(Invoker) Invoker
```

Default invoker:

```text
lookup tool
validate args if validator configured
emit tool.call
call tool
normalize result
emit tool.result/tool.error
```

## Middleware

MVP middleware:

- timeout;
- retry;
- max calls;
- max output bytes;
- audit event;
- panic recovery;
- argument redaction.

Post-MVP middleware:

- approval;
- sandbox routing;
- OpenTelemetry span;
- per-tool rate limit.

## Typed Tool Helper

Target API:

```go
search := tools.New("search", "Search documents",
    func(ctx context.Context, in SearchInput) (SearchOutput, error) {
        ...
    },
)
```

Supported function signatures:

```go
func(context.Context, In) (Out, error)
func(context.Context, In) (string, error)
func(context.Context, In) error
```

Input/output must be JSON-serializable.

Initial schema inference can be simple:

- exported struct fields;
- `json` tags;
- `jsonschema` tags where present;
- primitive types;
- arrays/slices;
- maps as object.

## Validation

MVP validation can be pragmatic:

- JSON must decode into input type;
- required fields can be inferred from `jsonschema:"required"`;
- unknown fields are rejected by default for typed tools.

Full JSON Schema validation can be added later.

## Events

Tool runtime emits:

- `tool.call`;
- `tool.result`;
- `tool.error`;

Payload sketch:

```json
{
  "toolCallId": "tool_1",
  "toolName": "search",
  "arguments": {"query": "hello"},
  "durationMs": 123,
  "exitCode": 0
}
```

Argument persistence must respect redaction policy.

## Error Model

Use typed errors:

- `ErrToolNotFound`;
- `ErrDuplicateTool`;
- `ErrInvalidArguments`;
- `ErrTimeout`;
- `ErrBudgetExceeded`;
- `ErrOutputTooLarge`.

Errors should still produce a model-facing `Result` when invoked from a model
loop. Direct SDK callers can receive Go errors.

## Migration From agent-platform

Reuse concepts from:

- `contracts.ToolExecutor`;
- `ToolExecutionResult`;
- `tools.ToolRouter`;
- `ToolRouter.invokeWithPolicy`;
- `LoadEmbeddedToolDefinitions`;
- `MergeToolDefinitions`.

Do not leak:

- `api.ToolDetailResponse`;
- `contracts.ExecutionContext`;
- `contracts.QuerySession`;
- `chat.Store`;
- `memory.Store`;
- `skills.CandidateStore`.

## S2 Tests

Minimum tests:

- register and lookup tool;
- duplicate name rejected;
- definitions sorted;
- typed tool decodes input;
- typed tool rejects invalid JSON;
- timeout middleware cancels slow tool;
- retry middleware retries transient error;
- max output middleware truncates or errors as configured;
- invoker emits tool events through a fake sink;
- panic recovery returns tool error.

## S2 Exit Criteria

- tool package is usable without model runtime;
- typed helper works for common functions;
- middleware chain is tested;
- no ZenMind platform import;
- S3 safety/workspace tools can build on this layer.


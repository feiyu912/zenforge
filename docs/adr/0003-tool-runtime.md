# ADR 0003: Tool Runtime And Middleware

Status: accepted

Amendment: the tool runtime and middleware decision is implemented. The `http`
builtin listed below is deferred beyond the current MVP; it is not a core
builtin in the present release.

## Context

The existing platform has a useful tool architecture: backend tools, MCP,
frontend tools, action tools, runtime definitions, budget checks, timeout,
retry, observability, file tools, shell tools, and HITL gates.

ZenForge needs a smaller public tool runtime that preserves the strong parts
without inheriting ZenMind DTOs.

## Decision

ZenForge will model tools as typed Go interfaces plus optional middleware.

Tool execution will flow through a registry and middleware chain:

```text
model tool call
  ↓
lookup tool definition
  ↓
validate JSON args
  ↓
policy middleware
  ↓
approval middleware if needed
  ↓
timeout/retry/budget middleware
  ↓
tool call
  ↓
normalize result
  ↓
persist checkpoint + event
```

## Core Types

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any
    Call(ctx context.Context, input json.RawMessage, call Context) (Result, error)
}

type Registry interface {
    Register(tool Tool) error
    Lookup(name string) (Tool, bool)
    Definitions() []Definition
}

type Middleware func(Invoker) Invoker

type Invoker interface {
    Invoke(ctx context.Context, call Call) (Result, error)
}
```

## Built-In Tool Groups

MVP core builtins:

- `todo_read`
- `todo_write`
- `todo_update`
- `workspace_read`
- `workspace_write`
- `workspace_list`
- `workspace_grep`
- `shell`

Adapter or post-MVP:

- `http`;
- memory tools;
- session search;
- artifact publish;
- desktop bridge;
- MCP tools;
- frontend form tools;
- skill candidate tools.

## Policy Middleware

The following policies should be independent middleware:

- timeout;
- retry;
- max calls;
- max output bytes;
- argument redaction;
- audit logging;
- approval required;
- sandbox routing;
- command review;
- file access review.

## Typed Tool Helper

ZenForge should support:

```go
tools.New("search", "Search documents",
    func(ctx context.Context, in SearchInput) (SearchOutput, error) {
        ...
    },
)
```

Schema generation can be basic in MVP and improved later.

## Migration From agent-platform

Reuse concepts from:

- `contracts.ToolExecutor`;
- `tools.ToolRouter`;
- `tools.ToolExecutionResult`;
- `ToolRouter.invokeWithPolicy`;
- embedded YAML tool definitions.

Avoid leaking:

- `api.ToolDetailResponse`;
- `contracts.ExecutionContext`;
- ZenMind `QuerySession`;
- memory/chat/gateway dependencies.

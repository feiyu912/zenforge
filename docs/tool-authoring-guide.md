# Tool Authoring Guide

This guide covers the current ZenForge tool APIs for typed tools, manual
tools, built-in workspace tools, shell execution, and todo tools.

## Simple Typed Tool

```go
type SearchInput struct {
    Query string `json:"query" jsonschema:"required,description=Search query"`
    Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum result count"`
}

type SearchOutput struct {
    Results []string `json:"results"`
}

search, err := tools.New("search", "Search internal documents",
    func(ctx context.Context, in SearchInput) (SearchOutput, error) {
        if in.Limit <= 0 {
            in.Limit = 5
        }
        return SearchOutput{Results: []string{"example"}}, nil
    },
)
if err != nil {
    return err
}
```

Typed handlers may also accept the runtime tool context as a third argument
when they need run identity, tool-call identity, or trusted metadata:

```go
audit, err := tools.New("audit", "Record an audited action",
    func(ctx context.Context, in AuditInput, call tool.Context) (AuditOutput, error) {
        return AuditOutput{RunID: call.RunID}, nil
    },
)
```

## Registry And Middleware

```go
registry, err := tool.NewRegistry(search)
if err != nil {
    return err
}

invoker := tool.NewInvoker(registry,
    tool.RecoverPanic(),
    tool.Timeout(30*time.Second),
    tool.Retry(2),
    tool.MaxOutputBytes(64_000),
)

result, err := invoker.Invoke(ctx, tool.Call{
    ID:        "call_1",
    RunID:     "run_123",
    Name:      "search",
    Arguments: json.RawMessage(`{"query":"zenforge"}`),
})
```

## Manual Tool

Use the low-level interface when a tool needs custom schema or raw JSON input.

```go
type SearchTool struct{}

func (SearchTool) Name() string { return "search" }

func (SearchTool) Description() string {
    return "Search internal documents"
}

func (SearchTool) Schema() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "query": map[string]any{"type": "string"},
        },
        "required": []string{"query"},
    }
}

func (SearchTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
    var args struct {
        Query string `json:"query"`
    }
    if err := json.Unmarshal(input, &args); err != nil {
        return tool.Result{Error: "invalid_arguments", ExitCode: -1}, nil
    }
    return tool.Result{
        Output: "found 1 result",
        Structured: map[string]any{
            "results": []string{"example"},
        },
        ExitCode: 0,
    }, nil
}
```

## Result Guidance

Use:

- `Output` for concise model-facing text;
- `Structured` for application data;
- `Error` for machine-readable error codes;
- `ExitCode` for success/failure convention.

Avoid:

- returning secrets;
- dumping huge raw files into `Output`;
- hiding important failure information only in logs.

## Safety

Tools that touch files, shell commands, networks, or external systems should be
wrapped with middleware:

- timeout;
- max output size;
- approval;
- audit logging;
- redaction.

Retry is explicit:

```go
return tool.Result{}, tool.MarkRetryable(err)
```

Unmarked errors run once. Cancellation, deadline, timeout, validation, budget,
and output-limit errors are never retried even when wrapped.

`tool.RedactArguments("password", "token")` recursively redacts matching JSON
keys in tool runtime audit events while the tool still receives the original
arguments. For the top-level harness event log, configure:

```go
zenforge.Config{
    ToolArgumentRedaction: []string{"password", "token"},
}
```

This redacts event/audit projections, not checkpointed executable tool calls.
Resume requires the original arguments. Avoid putting long-lived secrets in
tool arguments and protect durable checkpoint storage with host-level access
control and encryption where needed.

The default built-in shell and workspace tools will use conservative safety
defaults.

## Workspace Tools

```go
ws, err := local.New(local.Config{
    Root:            ".",
    MaxReadBytes:    256_000,
    MaxWriteBytes:   256_000,
    CreateParentDir: true,
})
if err != nil {
    return err
}

workspaceTools, err := workspacetools.Tools(workspacetools.Config{
    Workspace:              ws,
    RequireReadBeforeWrite: true,
    Snapshots:              workspacetools.NewSnapshotStore(),
    Policy: policy.FilePolicy{
        ReadRoots:       []string{"."},
        WriteRoots:      []string{"docs", "generated"},
        RequireApproval: true,
    },
})
if err != nil {
    return err
}

agent := zenforge.New(zenforge.Config{
    Model: model,
    Tools: workspaceTools,
})
```

The built-in workspace tools are:

- `workspace_read`
- `workspace_list`
- `workspace_grep`
- `workspace_write`

`workspace_read`, `workspace_list`, and `workspace_grep` check
`Policy.ReadRoots`; `workspace_write` checks `Policy.WriteRoots`. When a path is
outside the configured roots and `RequireApproval` is true, the tool returns an
approval request with a file access plan, and writes include a content SHA256
write plan. Approved calls are replayed through the standard approval metadata
fingerprint or rule key. When read-before-write is enabled, existing files must
have a fresh snapshot from the same run before they can be overwritten.

## Shell Tool

```go
shellTool, err := shell.New(shell.Config{
    Policy: policy.ShellPolicy{
        WorkingDir:     ".",
        AllowCommands:  []string{"go test ./...", "go vet ./...", "grep", "find"},
        DenyCommands:   []string{"rm", "curl"},
        MaxTimeout:     30 * time.Second,
        MaxOutputBytes: 64_000,
    },
})
if err != nil {
    return err
}
```

The `shell` tool requires a description, blocks commands outside the allowlist
by default, caps output, enforces timeout, and keeps `cwd` inside the configured
working directory.

## Todo Tools

```go
todoManager := planner.NewMemoryManager(planner.MemoryConfig{})
todoTools, err := todo.Tools(todo.Config{Manager: todoManager})
if err != nil {
    return err
}
```

The core todo tools are:

- `todo_write`
- `todo_read`
- `todo_update`

Compatibility aliases are available through `todo.CompatibilityAliases`:

- `plan_add_tasks`
- `plan_get_tasks`
- `plan_update_task`

`zenforge.PlanningPlanExecute` enables the default plan/execute/summary preset:

```go
agent := zenforge.New(zenforge.Config{
    Model:    model,
    Planning: zenforge.PlanningPlanExecute,
    Tools:    append(workspaceTools, shellTool),
})
```

# API Sketch

This is a historical draft API from the first implementation spike. Some names
below intentionally preserve early design direction and no longer match the
current public API exactly.

For current copy-pasteable examples, use:

- [SDK Guide](./sdk-guide.md)
- [Tool Authoring](./tool-authoring-guide.md)
- [Config Reference](./config-reference.md)
- [Quickstart](./quickstart.md)

## Create An Agent

```go
model := openai.New(openai.Config{
    APIKey: os.Getenv("OPENAI_API_KEY"),
    Model:  "gpt-4.1",
})

agent := zenforge.New(zenforge.Config{
    Model: model,
    Instructions: "You are a senior Go backend engineer.",
    Tools: []zenforge.Tool{
        tools.FileSystem("./repo"),
        tools.Shell(tools.ShellConfig{
            WorkingDir: "./repo",
            AllowCommands: []string{"go test ./...", "go vet ./...", "grep", "find"},
            Timeout: 30 * time.Second,
            MaxOutputBytes: 256_000,
        }),
    },
    Checkpoints: checkpointjsonl.New("./.zenforge/runs"),
    Trace: trace.Stdout(),
    Planning: zenforge.PlanningEnabled,
})
```

## Stream A Task

```go
events, err := agent.Stream(ctx, zenforge.Task{
    Input: "Review this repo and propose a refactor plan.",
    InitialMessages: []model.Message{
        {Role: "user", Content: "The repository is a Go service."},
        {Role: "assistant", Content: "I will keep that context."},
    },
})
if err != nil {
    return err
}

for event := range events {
    switch event.Type {
    case zenforge.EventModelDelta:
        fmt.Print(event.TextDelta)
    case zenforge.EventToolCall:
        log.Printf("tool: %s", event.ToolName)
    case zenforge.EventTodoUpdated:
        renderTodos(event.Todos)
    case zenforge.EventDone:
        return nil
    }
}
```

The current normalized task shape is:

```go
type Task struct {
    RunID           string
    Input           string
    InitialMessages []model.Message
    Meta            map[string]any
}
```

On a new run, `InitialMessages` are persisted in order and the current `Input`
is appended once. Resume reads the checkpointed messages and does not inject
the initial history again. For `PlanningPlanExecute`, history is supplied only
to the planning stage; execute and summary stages use their stage-owned state.

## Resume A Run

```go
events, err := agent.Resume(ctx, "run_123")
```

## Workspace Tools

```go
ws, err := local.New(local.Config{
    Root:            "./repo",
    MaxReadBytes:    256_000,
    MaxWriteBytes:   256_000,
    CreateParentDir: true,
})
if err != nil {
    return err
}

workspaceTools, err := workspacetools.Tools(workspacetools.Config{
    Workspace: ws,
})
if err != nil {
    return err
}

agent := zenforge.New(zenforge.Config{
    Model: model,
    Tools: workspaceTools,
})
```

## Shell Tool

```go
shellTool, err := shell.New(shell.Config{
    Policy: policy.ShellPolicy{
        WorkingDir:     "./repo",
        AllowCommands:  []string{"go test ./...", "go vet ./...", "grep", "find"},
        MaxTimeout:     30 * time.Second,
        MaxOutputBytes: 256_000,
    },
})
if err != nil {
    return err
}
```

## Todo Tools

```go
todoManager := planner.NewMemoryManager(planner.MemoryConfig{})
todoTools, err := todo.Tools(todo.Config{Manager: todoManager})
if err != nil {
    return err
}

agent := zenforge.New(zenforge.Config{
    Model:    model,
    Planning: zenforge.PlanningEnabled,
    Tools:    append(workspaceTools, shellTool),
})
```

## Plan/Execute Preset

```go
agent := zenforge.New(zenforge.Config{
    Model:    model,
    Planning: zenforge.PlanningPlanExecute,
    Tools:    append(workspaceTools, shellTool),
})

result, err := agent.Run(ctx, zenforge.Task{
    Input: "Analyze this repo and produce a refactor plan.",
})
```

## Durable Runtime Stores

S1 exposes replaceable durable runtime pieces:

```go
events := eventlogjsonl.New("./.zenforge/runs")
checkpoints := checkpointjsonl.New("./.zenforge/runs")

rec := recorder.Recorder{
    Events: events,
    Checkpoints: checkpoints,
}
```

The checkpoint schema version is `zenforge.checkpoint.v1`. The checkpoint
store is the resumable execution source of truth, while the event log is the
observable run history. `recorder.Recorder` is a low-level ordered-write helper;
production `zenforge.Agent` lifecycle and resume behavior do not run through
this helper.

## Define A Typed Tool

```go
type SearchInput struct {
    Query string `json:"query" jsonschema:"required,description=Search query"`
}

type SearchOutput struct {
    Results []string `json:"results"`
}

search, err := tools.New("search", "Search internal documents",
    func(ctx context.Context, in SearchInput) (SearchOutput, error) {
        return SearchOutput{Results: []string{"example"}}, nil
    },
)
if err != nil {
    return err
}
```

## Invoke Tools Directly

```go
registry, err := tool.NewRegistry(search)
if err != nil {
    return err
}

invoker := tool.NewInvoker(registry,
    tool.RecoverPanic(),
    tool.Timeout(30*time.Second),
    tool.MaxOutputBytes(256_000),
)

result, err := invoker.Invoke(ctx, tool.Call{
    ID:        "call_1",
    RunID:     "run_123",
    Name:      "search",
    Arguments: json.RawMessage(`{"query":"example"}`),
})
```

## Add Sub-Agents

```go
agent := zenforge.New(zenforge.Config{
    Model: model,
    Tools: baseTools,
    SubAgents: []zenforge.SubAgentSpec{
        {
            Name: "researcher",
            Description: "Searches, reads, and summarizes evidence.",
            Instructions: "Be precise and cite sources from the workspace.",
            Tools: []zenforge.Tool{tools.FileSystem("./repo"), tools.HTTP()},
        },
        {
            Name: "reviewer",
            Description: "Finds risks, bugs, and missing tests.",
            Instructions: "Prioritize concrete findings.",
            Tools: []zenforge.Tool{tools.FileSystem("./repo"), tools.Shell(...)},
        },
    },
})
```

## CLI

```bash
zenforge run "Analyze this repository"
zenforge code ./repo "Find risky areas and suggest tests"
zenforge resume run_123
```

## Config File

```json
{
  "model": {
    "provider": "openai",
    "name": "gpt-4.1"
  },
  "agent": {
    "instructions": "You are a senior Go backend engineer.",
    "mode": "plan_execute"
  },
  "workspace": {
    "root": ".",
    "readRoots": ["."],
    "writeRoots": ["./tmp"]
  },
  "shell": {
    "workingDir": ".",
    "allow": ["go test ./...", "go vet ./...", "grep", "find"],
    "timeout": "30s"
  },
  "checkpoint": {
    "type": "jsonl",
    "path": "./.zenforge/runs"
  }
}
```

# API Sketch

This is a draft API. Names can change after the first implementation spike.

## Create An Agent

```go
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
    Checkpoints: checkpoint.JSONL("./.zenforge/runs"),
    Trace: trace.Stdout(),
    Planning: zenforge.PlanningEnabled,
})
```

## Stream A Task

```go
events, err := agent.Stream(ctx, zenforge.Task{
    Input: "Review this repo and propose a refactor plan.",
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

## Resume A Run

```go
events, err := agent.Resume(ctx, "run_123")
```

## Define A Typed Tool

```go
type SearchInput struct {
    Query string `json:"query" jsonschema:"required,description=Search query"`
}

type SearchOutput struct {
    Results []string `json:"results"`
}

search := tools.New("search", "Search internal documents",
    func(ctx context.Context, in SearchInput) (SearchOutput, error) {
        return SearchOutput{Results: []string{"example"}}, nil
    },
)
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

```yaml
model:
  provider: openai
  name: gpt-4.1

agent:
  instructions: |
    You are a senior Go backend engineer.
  planning: true
  subagents: true

tools:
  filesystem:
    root: .
    read:
      - .
    write:
      - ./tmp
  shell:
    workingDir: .
    allow:
      - go test ./...
      - go vet ./...
      - grep
      - find
    timeout: 30s

checkpoint:
  type: jsonl
  path: ./.zenforge/runs
```


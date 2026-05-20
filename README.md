# ZenForge

ZenForge is a production-first Go agent runtime for long-running, tool-using,
observable, and recoverable agents.

It is not a Go clone of LangChain. The goal is a batteries-included agent
harness: a default agent that can run real multi-step work, while keeping the
model, tools, workspace, planner, checkpoint store, trace sink, and policy layer
replaceable.

## Why This Project Exists

The current ZenMind codebase already contains a working internal agent runtime:
model streaming, tool execution, plan/execute mode, HITL approvals, sub-agent
delegation, memory, sandbox execution, event streaming, and chat trace storage.

ZenForge extracts the reusable runtime core from that platform so it can become
a standalone Go library and CLI.

## Product Positioning

ZenForge is:

- A Go-native runtime for production agent applications.
- A long-task harness with planning, tools, checkpoints, and streaming events.
- A practical backend library for teams that do not want to embed a Python agent
  runtime inside Go services.
- A runtime core that can power HTTP servers, CLIs, desktop apps, gateways, and
  private deployments.

ZenForge is not:

- A prompt-template zoo.
- A vector-store marketplace.
- A chain/pipeline abstraction framework.
- A full application platform by itself.

## Target Shape

```go
package main

import (
    "context"

    "github.com/zenmind-ai/zenforge"
    "github.com/zenmind-ai/zenforge/model/openai"
    "github.com/zenmind-ai/zenforge/tools"
)

func main() {
    ctx := context.Background()

    agent := zenforge.New(zenforge.Config{
        Model: openai.New(openai.Config{
            Model: "gpt-4.1",
        }),
        Instructions: "You are a senior Go backend engineer.",
        Tools: []zenforge.Tool{
            tools.FileSystem("./repo"),
            tools.Shell(tools.ShellConfig{
                WorkingDir: "./repo",
                AllowCommands: []string{
                    "go test ./...",
                    "go vet ./...",
                    "grep",
                    "find",
                },
            }),
            tools.HTTP(),
        },
        Planning:  zenforge.PlanningEnabled,
        SubAgents: zenforge.SubAgentsEnabled,
    })

    events, err := agent.Stream(ctx, zenforge.Task{
        Input: "Analyze this repository and propose a refactor plan.",
    })
    if err != nil {
        panic(err)
    }

    for event := range events {
        println(event.String())
    }
}
```

## Repository Layout

```text
zenforge/
  README.md
  docs/
    vision.md
    current-project-mapping.md
    architecture.md
    mvp-scope.md
    extraction-plan.md
    api-sketch.md
```

Implementation should begin only after the boundaries in these documents are
accepted.


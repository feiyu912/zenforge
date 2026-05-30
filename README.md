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
    "os"
    "time"

    "github.com/feiyu912/zenforge"
    checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
    eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
    "github.com/feiyu912/zenforge/model/openai"
    "github.com/feiyu912/zenforge/policy"
    shelltool "github.com/feiyu912/zenforge/tools/shell"
    workspacetools "github.com/feiyu912/zenforge/tools/workspace"
    workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

func main() {
    ctx := context.Background()

    ws, _ := workspacelocal.New(workspacelocal.Config{Root: "."})
    workspaceTools, _ := workspacetools.Tools(workspacetools.Config{Workspace: ws})
    shellTool, _ := shelltool.New(shelltool.Config{Policy: policy.ShellPolicy{
        WorkingDir:     ".",
        AllowCommands:  []string{"go test ./...", "grep", "find"},
        MaxTimeout:     30 * time.Second,
        MaxOutputBytes: 256_000,
    }})

    agent := zenforge.New(zenforge.Config{
        Model: openai.New(openai.Config{
            APIKey: os.Getenv("OPENAI_API_KEY"),
            Model: "gpt-4.1",
        }),
        Instructions: "You are a senior Go backend engineer.",
        Tools: append(workspaceTools, shellTool),
        Events: eventlogjsonl.New(".zenforge/runs"),
        Checkpoints: checkpointjsonl.New(".zenforge/runs"),
        Planning: zenforge.PlanningPlanExecute,
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

## CLI Quick Start

```bash
go run ./cmd/zenforge init
export OPENAI_API_KEY=...
go run ./cmd/zenforge run --config zenforge.json "Analyze this repo"
```

More CLI setup, event inspection, resume, approval modes, and examples are in
[docs/quickstart.md](./docs/quickstart.md).

Useful examples:

```bash
go run ./examples/sdk-embedded-agent
OPENAI_API_KEY=... go run ./examples/repo-refactor-agent
OPENAI_API_KEY=... go run ./examples/code-review-agent
OPENAI_API_KEY=... go run ./examples/simple-tool-agent
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

Start with [docs/preparation-plan.md](./docs/preparation-plan.md) before
porting implementation code. It captures the current extraction strategy after
reviewing `agent-platform` and `agent-container-hub`.

For the full path from project start to MVP and the first usable product, see
[docs/product-roadmap.md](./docs/product-roadmap.md).

The design document index is [docs/README.md](./docs/README.md).

The SDK guide is [docs/sdk-guide.md](./docs/sdk-guide.md).

The detailed durable runtime design for S1 is
[docs/s1-durable-runtime-spec.md](./docs/s1-durable-runtime-spec.md).

The tool runtime design for S2 is
[docs/s2-tool-runtime-spec.md](./docs/s2-tool-runtime-spec.md).

The safety and workspace design for S3 is
[docs/s3-safety-workspace-spec.md](./docs/s3-safety-workspace-spec.md).

The minimal harness design for S4 is
[docs/s4-minimal-harness-spec.md](./docs/s4-minimal-harness-spec.md).

The planner and todo design for S5 is
[docs/s5-planner-todo-spec.md](./docs/s5-planner-todo-spec.md).

The approval and HITL design for S6 is
[docs/s6-approval-hitl-spec.md](./docs/s6-approval-hitl-spec.md).

The sub-agent runtime design for S7 is
[docs/s7-subagent-runtime-spec.md](./docs/s7-subagent-runtime-spec.md).

The sandbox adapter design for S8 is
[docs/s8-sandbox-adapter-spec.md](./docs/s8-sandbox-adapter-spec.md).

The MVP assembly plan is
[docs/mvp-assembly-plan.md](./docs/mvp-assembly-plan.md).

The current MVP validation evidence is
[docs/mvp-validation.md](./docs/mvp-validation.md).

The release checklist is
[docs/release-checklist.md](./docs/release-checklist.md).

The checkpoint/resume guide is
[docs/checkpoint-resume-guide.md](./docs/checkpoint-resume-guide.md).

The server/SSE adapter guide is
[docs/server-sse-guide.md](./docs/server-sse-guide.md).

The server/HTTP adapter guide is
[docs/server-http-guide.md](./docs/server-http-guide.md).

The ZenMind adapter guide is
[docs/zenmind-adapter-guide.md](./docs/zenmind-adapter-guide.md).

The trace guide is
[docs/trace-guide.md](./docs/trace-guide.md).

The CLI config reference is
[docs/config-reference.md](./docs/config-reference.md).

The MVP limitations are
[docs/limitations.md](./docs/limitations.md).

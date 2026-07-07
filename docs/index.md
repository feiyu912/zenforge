# ZenForge

**A Go framework for application-owned AI agents.**

ZenForge gives you the durable agent loop — streaming, tool dispatch,
approval, checkpointing, tracing — and lets your application own everything
else: the model, the tools, the approval policy, the sandbox.

<div class="grid cards" markdown>

-   :material-rocket-launch:{ .lg .middle } **Get started in 15 minutes**

    ---

    Build a self-contained agent from scratch and exercise every major
    ZenForge surface. No prior framework knowledge required.

    [:octicons-arrow-right-24: Open the tutorial](tutorial.md)

-   :material-book-open-variant:{ .lg .middle } **Understand the mental model**

    ---

    The four interfaces your code touches, the persistence trio, and the
    application-owned boundary. Five minutes, one diagram.

    [:octicons-arrow-right-24: Read the concepts](concepts.md)

-   :material-code-braces:{ .lg .middle } **Copy from working examples**

    ---

    Four reference agents, from a single typed tool up to a flagship
    plan / execute / summary workflow with approval gating.

    [:octicons-arrow-right-24: Browse the examples](examples.md)

-   :material-api:{ .lg .middle } **Dive into the API**

    ---

    Every public package has its own godoc page on pkg.go.dev.

    [:octicons-arrow-right-24: Open the API reference](api.md)

</div>

## A self-contained agent in one file

ZenForge is a library, not a CLI. The smallest useful program wires up a
model, a tool, and the agent loop, then hands user input to `agent.Run`:

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/feiyu912/zenforge"
    "github.com/feiyu912/zenforge/model/anthropic"
    "github.com/feiyu912/zenforge/tools"
)

type lookupInput struct {
    Query string `json:"query" jsonschema:"required,description=What to look up"`
}

func main() {
    lookup := tools.Must("lookup", "Look up an internal fact.",
        func(_ context.Context, in lookupInput) (string, error) {
            return "answer for " + in.Query, nil
        })

    agent := zenforge.New(zenforge.Config{
        Model: anthropic.New(anthropic.Config{
            APIKey: os.Getenv("ANTHROPIC_API_KEY"),
            Model:  "claude-opus-4-7",
        }),
        Instructions: "Use the lookup tool when asked about the project.",
        Tools:        []zenforge.Tool{lookup},
        MaxSteps:     4,
    })

    out, err := agent.Run(context.Background(), zenforge.Task{
        Input: "What does the project say about ZenForge?",
    })
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
    fmt.Println(out.Output)
}
```

That program imports ZenForge as a Go library. The same library powers a
durable backend, a CLI, an HTTP service, or an in-memory test — the runtime
is identical in every host.

## Why application-owned?

Most AI agent frameworks ship a single CLI binary that picks a model, a
tool registry, a sandbox, and an approval policy on your behalf. That's
convenient for getting started and painful when you outgrow any of those
choices.

ZenForge splits the work in half:

| | Framework owns | Application owns |
| --- | --- | --- |
| **Runtime** | Agent loop, streaming, tool dispatch, approval lifecycle, checkpointing, resume, trace redaction | — |
| **Choices** | — | Model, tools, approval broker, sandbox backend, workspace, event log, checkpoint store, trace sink, sub-agent roster |

The boundary is the `zenforge.Config` struct. Every adapter point is a Go
interface that you can satisfy, wrap, mock, or leave nil.

## What's in the box

<div class="grid cards" markdown>

-   :material-robot:{ .lg .middle } **Agent loop**

    ---

    ReAct-style loop with streaming deltas, tool dispatch, approval
    integration, and a `MaxSteps` safety cap.

    [:octicons-arrow-right-24: Learn more](concepts.md#the-agent-loop)

-   :material-tools:{ .lg .middle } **Tool registry**

    ---

    Typed tools via `tools.Must`, schema-inferred from Go structs, dispatched
    through middleware-aware invokers.

    [:octicons-arrow-right-24: Tool authoring](tool-authoring-guide.md)

-   :material-shield-check:{ .lg .middle } **Approval broker**

    ---

    `approval.AlwaysAllow` for tests, `approval/cli` for interactive prompts,
    `PendingBroker` for simple HTTP gateways, and durable inboxes for shared
    HTTP approval routing.

    [:octicons-arrow-right-24: Approval guide](approval-guide.md)

-   :material-server:{ .lg .middle } **Sandbox adapter**

    ---

    Tools route shell commands through the configured `Sandbox` —
    `fake` for tests, `containerhub` for Docker isolation, your own for
    anything else.

    [:octicons-arrow-right-24: Sandbox guide](sandbox-guide.md)

-   :material-database-clock:{ .lg .middle } **Checkpoint store**

    ---

    Versioned, durable resume state under `zenforge.checkpoint.v1`. Save
    happens before the next decision, never after.

    [:octicons-arrow-right-24: Resume guide](checkpoint-resume-guide.md)

-   :material-graph:{ .lg .middle } **Trace sink**

    ---

    Best-effort, redacted view of execution for OpenTelemetry or JSONL logs.
    Never authoritative; never blocks the loop.

    [:octicons-arrow-right-24: Tracing guide](trace-guide.md)

</div>

## Where to look next

1. **New to ZenForge?** → [Tutorial](tutorial.md) builds a working agent in
   15 minutes.
2. **Coming from LangChain or another framework?** → [Concepts](concepts.md)
   maps ZenForge's vocabulary onto what you already know.
3. **Picking a model?** → [Providers](provider-guide.md) covers Anthropic,
   OpenAI, MiniMax, and custom adapters.
4. **Embedding ZenForge in an existing Go service?** → [SDK guide](sdk-guide.md)
   covers the import paths, lifecycle, and edge cases.
5. **Looking for code to copy?** → [Examples](examples.md) ships four
   reference agents you can read top-to-bottom.

Source on [GitHub](https://github.com/feiyu912/zenforge) ·
[godoc on pkg.go.dev](https://pkg.go.dev/github.com/feiyu912/zenforge) ·
Apache 2.0

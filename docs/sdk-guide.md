# SDK Guide

ZenForge can be embedded directly in Go services. The SDK surface is centered
on `zenforge.New`, `zenforge.Config`, `Agent.Stream`, `Agent.Run`, and
`Agent.Resume`.

## Create An Agent

```go
agent := zenforge.New(zenforge.Config{
    Model:        model,
    Instructions: "Use tools when useful and answer briefly.",
    Tools:        []zenforge.Tool{lookup},
    Events:       eventlogjsonl.New(".zenforge/runs"),
    Checkpoints:  checkpointjsonl.New(".zenforge/runs"),
    Trace:        trace.Redact(trace.Stdout()),
    MaxSteps:     8,
})
```

`Events` and `Checkpoints` are separate on purpose:

- events are the observable run history;
- checkpoints are the resume source of truth.

For a single local database file, use the SQLite stores:

```go
events, _ := eventlogsqlite.Open(ctx, ".zenforge/runs.db")
checkpoints, _ := checkpointsqlite.Open(ctx, ".zenforge/runs.db")
defer events.Close()
defer checkpoints.Close()
```

Trace sinks are equally replaceable. For services that already configure
OpenTelemetry exporters, use `trace/otel`:

```go
agent := zenforge.New(zenforge.Config{
    Trace: trace.WithFields(trace.Redact(oteltrace.New(tracer)), map[string]any{
        "service":  "api",
        "tenantId": "tenant_1",
    })),
})
```

MCP tools can be bridged at the edge:

```go
mcpClient, _ := mcp.NewStdioClient(ctx, mcp.StdioConfig{
    Command: "my-mcp-server",
    Args:    []string{"--stdio"},
})
mcpTools, _ := mcp.Tools(ctx, mcpClient)

agent := zenforge.New(zenforge.Config{
    Tools: mcpTools,
})
```

Retrieved memory can be normalized into a task before execution. Platform
metadata can be used as a final scope guard before injection:

```go
scopedMemory := memory.ScopedStore{
    Store:    memoryStore,
    MetaKeys: []string{"tenantId", "sessionId"},
}
augmenter := memory.Augmenter{Store: scopedMemory, MaxEntries: 5}
task, _, err := augmenter.AugmentTask(ctx, zenforge.Task{
    Input: "Summarize the current project direction.",
    Meta: map[string]any{
        "tenantId":  "tenant_1",
        "sessionId": "session_abc",
    },
})
if err != nil {
    return err
}

events, err := agent.Stream(ctx, task)
```

Use the provider adapter that matches your deployment:

```go
agent := zenforge.New(zenforge.Config{
    Model: anthropic.New(anthropic.Config{
        APIKey: os.Getenv("ANTHROPIC_API_KEY"),
        Model:  "claude-model",
    }),
})
```

## Run Once

```go
result, err := agent.Run(ctx, zenforge.Task{
    Input: "Review this package and summarize the risk.",
})
```

`Run` consumes the event stream internally and returns the final output.

## Stream Events

```go
events, err := agent.Stream(ctx, zenforge.Task{
    RunID: "run_123",
    Input: "Analyze this repository.",
    Meta:  map[string]any{"sessionId": "session_abc"},
})
if err != nil {
    return err
}

for event := range events {
    switch event.Type {
    case zenforge.EventModelDelta:
        fmt.Print(event.Payload["textDelta"])
    case zenforge.EventToolCall:
        log.Printf("tool call: %s", event.Payload["toolName"])
    case zenforge.EventRunDone:
        log.Printf("done: %s", event.Payload["output"])
    }
}
```

## Define A Typed Tool

```go
type LookupInput struct {
    Query string `json:"query" jsonschema:"required,description=Lookup query"`
}

lookup := tools.Must("lookup", "Look up internal facts.",
    func(ctx context.Context, in LookupInput) (string, error) {
        return "result for " + in.Query, nil
    },
)
```

Typed tools infer JSON schema from Go structs and return normalized tool
results.

## Resume

```go
events, err := agent.Resume(ctx, "run_123")
```

Resume loads the latest checkpoint for the run. Terminal checkpoints do not
rerun model/tool work. Waiting approval checkpoints require a configured
approval broker.

## Embed In A Server

Use `server/harnesshttp` when an HTTP/SSE edge is useful:

```go
handler := harnesshttp.New(agent, sse.Options{RetryMillis: 1500})
handler.Events = eventlogjsonl.New(".zenforge/runs")

mux.HandleFunc("/run", handler.ServeRun)
mux.HandleFunc("/resume", handler.ServeResume)
mux.HandleFunc("/events", handler.ServeEvents)
```

Host services should keep authentication, tenancy, catalog loading, memory, and
policy translation outside the harness. ZenForge receives normalized tasks and
emits normalized runtime events.

## Local Example

The [SDK embedded agent example](../examples/sdk-embedded-agent) compiles and
runs without an API key:

```bash
go run ./examples/sdk-embedded-agent
```

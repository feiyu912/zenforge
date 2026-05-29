# Server SSE Guide

ZenForge core emits stable `zenforge.Event` values. Server and platform
adapters can expose those events over Server-Sent Events without adopting any
ZenMind-specific transport shape.

The `server/sse` package writes standard `text/event-stream` frames:

```go
events, err := agent.Stream(ctx, zenforge.Task{
    Input: "Review this repo and propose a refactor plan.",
})
if err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
}

if err := sse.StreamHTTP(r.Context(), w, events, sse.Options{
    RetryMillis: 1500,
}); err != nil && !errors.Is(err, context.Canceled) {
    log.Printf("sse stream failed: %v", err)
}
```

Each ZenForge event is serialized as one SSE message:

```text
id: 12
event: tool.result
data: {"seq":12,"type":"tool.result","runId":"run_123","toolName":"grep",...}
```

## Adapter Boundary

SSE is only a transport helper. It does not replace the event log or checkpoint
store:

- clients consume SSE for live progress;
- event log remains the replay/read model;
- checkpoint store remains the resume source of truth.

ZenMind or another host platform can map these SSE frames to its own frontend
events, WebSocket protocol, or persisted chat trace. The harness remains
responsible only for normalized runtime events.

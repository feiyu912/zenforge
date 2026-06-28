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

For platform servers that need multiple live observers, wrap the durable event
store with an `eventlog.FanoutStore`:

```go
bus := eventlog.NewBus()
events := eventlog.NewFanoutStore(durableEvents, bus)

live, unsubscribe, err := bus.Subscribe(r.Context(), "run_123", 128)
if err != nil {
    http.Error(w, err.Error(), http.StatusBadRequest)
    return
}
defer unsubscribe()

if err := sse.StreamHTTP(r.Context(), w, live, sse.Options{}); err != nil && !errors.Is(err, context.Canceled) {
    log.Printf("sse stream failed: %v", err)
}
```

The bus is live fanout only. Slow subscribers are disconnected and can recover
from the durable event log using `afterSeq`.

If the host server uses `server/harnesshttp`, configure `handler.Bus` and mount
`handler.ServeLiveEvents` for the same live stream behavior:

```go
handler.Bus = bus
handler.Events = durableEvents
mux.HandleFunc("/live", handler.ServeLiveEvents)
```

`GET /live?runId=run_123&replay=true&afterSeq=42` uses `eventlog.Follow` to
bridge durable replay to live fanout without a subscribe gap. It polls the
durable store as a backstop, recovers from live-buffer overflow, emits each
sequence once, and accepts `Last-Event-ID` when the query cursor is absent.

ZenMind or another host platform can map these SSE frames to its own frontend
events, WebSocket protocol, or persisted chat trace. The harness remains
responsible only for normalized runtime events.

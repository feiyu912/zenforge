# Server HTTP Guide

The `server/harnesshttp` package is a small adapter layer for host platforms
that want to expose a ZenForge agent over HTTP.

It deliberately does not configure models, tools, auth, tenancy, or routing.
Those stay in the host server. The handler receives an already-configured
agent and exposes:

- `POST /run` style JSON input to `Agent.Stream`;
- `GET /resume?runId=...` or `POST /resume` to `Agent.Resume`;
- `GET /events?runId=...` to replay persisted event log entries;
- standard Server-Sent Events responses through `server/sse`.

```go
agent := zenforge.New(config)
handler := harnesshttp.New(agent, sse.Options{RetryMillis: 1500})
handler.Events = eventStore

mux := http.NewServeMux()
mux.HandleFunc("/run", handler.ServeRun)
mux.HandleFunc("/resume", handler.ServeResume)
mux.HandleFunc("/events", handler.ServeEvents)
```

Run request:

```json
{
  "runId": "run_123",
  "input": "Review this repository and propose a refactor plan.",
  "meta": {
    "sessionId": "session_abc"
  }
}
```

Resume request:

```json
{
  "runId": "run_123"
}
```

Event replay request:

```text
GET /events?runId=run_123&afterSeq=42&limit=100
```

`afterSeq` and `limit` are optional. Replay uses the configured event store as
the read model and streams matching events as SSE frames.

## Platform Boundary

Use this package below the platform edge:

- platform auth and session lookup happen before calling the handler;
- platform catalog, memory, and policy are translated into `zenforge.Config`;
- ZenForge emits normalized runtime events;
- the handler streams live or replayed events without inventing
  platform-specific DTOs.

This keeps the harness reusable while giving ZenMind or another host platform a
concrete integration point.

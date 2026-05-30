# Trace Guide

ZenForge can emit runtime events to a trace sink through `zenforge.Config`.
Trace sinks are for observability and adapter integration. They are not the
checkpoint source of truth.

```go
agent := zenforge.New(zenforge.Config{
    Model: model,
    Trace: trace.Redact(trace.Stdout()),
})
```

## Built-In Sinks

- `trace.Stdout()` writes JSONL trace events to stdout.
- `trace.NewJSONSink(w)` writes JSONL trace events to any `io.Writer`.
- `trace.NewMemorySink()` stores trace events in memory for tests and embedded
  observers.
- `trace/otel.New(tracer)` emits each runtime trace event as an OpenTelemetry
  span with ZenForge attributes.
- `trace.Discard()` ignores events while still honoring context cancellation.

## OpenTelemetry

Use the OpenTelemetry sink when the host service already owns exporter setup:

```go
sink := trace.Redact(oteltrace.New(provider.Tracer("zenforge")))

agent := zenforge.New(zenforge.Config{
    Trace: sink,
})
```

The sink records short spans named like `zenforge.tool.call` and attaches
attributes such as `zenforge.run_id`, `zenforge.seq`, `zenforge.event.type`,
and `zenforge.data.*`.

## Redaction

Trace events can contain tool arguments, tool output, approval payloads, and
model metadata. Treat traces as sensitive and redact before writing them to
shared logs.

```go
sink := trace.Redact(trace.NewJSONSink(writer))
```

Default redaction matches common secret-bearing keys:

- `api_key`;
- `apikey`;
- `authorization`;
- `password`;
- `secret`;
- `token`.

The key match is case-insensitive and ignores `_`, `-`, and `.` separators, so
`apiKey`, `access_token`, and `Authorization` are covered.

Use `trace.RedactWith` when a host platform has additional secret fields:

```go
sink := trace.RedactWith(trace.Stdout(), trace.Redactor{
    Keys:        []string{"session_id", "resource_ticket"},
    Replacement: "***",
})
```

## Adapter Boundary

Application and platform adapters decide exporter configuration, sampling,
resource metadata, service names, and retention. ZenForge only defines the sink
interface and local helpers.

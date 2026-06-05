# ADR 0007: Run State Schema

Status: proposed

## Context

ZenForge needs durable resume. A checkpoint must represent the execution state
without relying on in-memory goroutines, event buses, or UI replay logs.

## Decision

ZenForge will define an explicit `RunState` schema. Every resumable subsystem
must store its state under this schema or an extension field with a versioned
payload.

## Required State Areas

- run identity;
- phase and step;
- model messages;
- pending/active tool call;
- latest tool result;
- todos;
- approval wait/decision;
- subtask status;
- run control state;
- usage;
- workspace references;
- model metadata.

## Versioning

Every run state includes:

```go
Version string `json:"version"`
```

Initial version:

```text
zenforge.run_state.v1
```

The containing checkpoint record also has its own schema version:

```text
zenforge.checkpoint.v1
```

Breaking changes require a migration function or a new loader path.

## Extension Policy

Adapters may store extra fields in:

```go
Meta map[string]any
```

Rules:

- core must not depend on adapter-specific keys;
- adapter keys should be namespaced, for example `zenmind.chatId`;
- secrets must not be written by default;
- large artifacts should be referenced, not embedded.

## Non-Goals

- Store every streamed model token in checkpoint.
- Store full workspace file content in checkpoint.
- Store platform-specific chat summaries.
- Store frontend-specific UI snapshots.

## Consequences

This design makes checkpoint more verbose than a pure event log, but gives
ZenForge a real durable execution boundary.

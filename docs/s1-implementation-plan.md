# S1 Implementation Plan

This document turns `s1-durable-runtime-spec.md` into implementation tasks.

## Goal

Implement durable event/checkpoint foundations without implementing the full
agent loop yet.

## Task List

### 1. Package Skeleton

Create:

```text
eventlog/
eventlog/memory/
eventlog/jsonl/
harness/
checkpoint/memory/
checkpoint/jsonl/
```

Acceptance:

- packages compile;
- no cyclic imports;
- root package remains small.

### 2. Event Schema

Implement:

- `Event`;
- `EventType`;
- `EventData`;
- sequence helper;
- validation helper.

Acceptance:

- event requires `runId`, `type`, `timestamp`;
- persisted events require `seq`;
- tests cover invalid events.

### 3. RunEventLog Interface

Implement:

```go
type Store interface {
    Append(ctx context.Context, event zenforge.Event) error
    Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error)
    LatestSeq(ctx context.Context, runID string) (int64, error)
}
```

Acceptance:

- memory implementation passes tests;
- JSONL implementation passes tests.

### 4. Run State Types

Implement in `harness`:

- `RunState`;
- `RunPhase`;
- `MessageState`;
- `ToolState`;
- `TodoState`;
- `ApprovalState`;
- `SubtaskState`;
- `RunControlState`;
- `UsageState`;
- `WorkspaceState`;
- `ModelState`.

Acceptance:

- JSON round-trip tests;
- phase constants tested;
- zero value is safe.

### 5. Checkpoint Schema

Update `checkpoint` package:

- `Checkpoint`;
- `Store`;
- `ErrNotFound`;
- `Validate`.

Acceptance:

- checkpoint contains version/runID/seq/state/savedAt;
- load missing run returns `ErrNotFound`.

### 6. Memory Checkpoint Store

Implement:

- save latest by runID;
- load latest by runID;
- delete.

Acceptance:

- tests cover save/load/delete;
- stored checkpoint is cloned or immutable enough for tests.

### 7. JSONL Checkpoint Store

Implement file layout:

```text
root/
  run_123/
    checkpoints.jsonl
    latest.json
```

Acceptance:

- appends checkpoint JSONL;
- writes latest checkpoint atomically where practical;
- loads latest;
- handles corrupt historical JSONL line by using latest file;
- tests use temp dirs.

### 8. JSONL Event Store

Implement file layout:

```text
root/
  run_123/
    events.jsonl
```

Acceptance:

- append preserves order;
- latest seq scans or caches correctly;
- read supports `afterSeq`;
- read supports limit;
- corrupt line behavior is documented and tested.

### 9. Run Recorder

Add helper:

```go
type Recorder struct {
    Events      eventlog.Store
    Checkpoints checkpoint.Store
}
```

Responsibilities:

- save checkpoint;
- append `checkpoint.created`;
- append runtime event;
- keep write order consistent.

Acceptance:

- tests verify checkpoint before checkpoint event;
- tests verify terminal write order.

### 10. Documentation Update

Update:

- `api-sketch.md`;
- `architecture.md`;
- `mvp-scope.md` if needed.

Acceptance:

- S1 package names are consistent across docs and code.

## Out Of Scope

- model streaming;
- tool execution;
- approval broker implementation;
- sub-agent execution;
- CLI.

## Done Criteria

- `go test ./...` passes;
- S1 tests are meaningful, not just compile tests;
- no dependency on `agent-platform`;
- checkpoint schema version is documented;
- JSONL stores are usable by S4 harness.


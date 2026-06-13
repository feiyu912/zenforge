# S1 Durable Runtime Spec

S1 builds the durable foundation that every later stage depends on.

The goal is not to run a full agent yet. The goal is to define and test the
state model that can later support agent loop, tools, approvals, sub-agents, and
resume.

The event wire format is extracted from `agent-platform/internal/stream`: event
JSON is flattened as `seq`, `type`, payload fields, then `timestamp`, instead of
wrapping payload under a separate `data` field.

## S1 Outcome

After S1, ZenForge should have:

- stable event types;
- append/read event log;
- checkpoint save/load;
- serializable run state;
- explicit resume boundaries;
- in-memory and JSONL implementations;
- tests for persistence and replay.

## Runtime Source Of Truth

ZenForge uses two persistence streams:

```text
RunEventLog      observable history
CheckpointStore  resumable execution state
```

The checkpoint store is the execution source of truth. The event log is the
observable trace.

## Package Plan

```text
eventlog/
  interface.go
  memory/
  jsonl/

checkpoint/
  interface.go
  memory/
  jsonl/

harness/
  state.go
  run_control.go
  resume.go

recorder/
  recorder.go
```

## Core Types

### Run Phase

```go
type RunPhase string

const (
    RunPhaseCreated       RunPhase = "created"
    RunPhaseModel         RunPhase = "model"
    RunPhaseTool          RunPhase = "tool"
    RunPhaseApproval      RunPhase = "approval"
    RunPhaseSubtask       RunPhase = "subtask"
    RunPhaseFinalizing    RunPhase = "finalizing"
    RunPhaseCompleted     RunPhase = "completed"
    RunPhaseFailed        RunPhase = "failed"
    RunPhaseCancelled     RunPhase = "cancelled"
)
```

### Run State

```go
type RunState struct {
    Version     string            `json:"version"`
    RunID       string            `json:"runId"`
    ParentRunID string            `json:"parentRunId,omitempty"`
    TaskID      string            `json:"taskId,omitempty"`
    Input       string            `json:"input"`
    Phase       RunPhase          `json:"phase"`
    Step        int               `json:"step"`
    CreatedAt   time.Time         `json:"createdAt"`
    UpdatedAt   time.Time         `json:"updatedAt"`

    Messages    []MessageState    `json:"messages,omitempty"`
    Todos       []TodoState       `json:"todos,omitempty"`
    Tool        ToolState         `json:"tool,omitempty"`
    Approval    ApprovalState     `json:"approval,omitempty"`
    Subtasks    []SubtaskState    `json:"subtasks,omitempty"`
    Control     RunControlState   `json:"control"`
    Usage       UsageState        `json:"usage,omitempty"`
    Workspace   WorkspaceState    `json:"workspace,omitempty"`
    Model       ModelState        `json:"model,omitempty"`
    Meta        map[string]any    `json:"meta,omitempty"`
}
```

### Message State

```go
type MessageState struct {
    ID         string         `json:"id,omitempty"`
    Role       string         `json:"role"`
    Name       string         `json:"name,omitempty"`
    Content    string         `json:"content,omitempty"`
    ToolCallID string         `json:"toolCallId,omitempty"`
    ToolCalls  []ToolCallSpec `json:"toolCalls,omitempty"`
    Meta       map[string]any `json:"meta,omitempty"`
}
```

### Tool State

```go
type ToolState struct {
    Pending []ToolCallState `json:"pending,omitempty"`
    Active  *ToolCallState  `json:"active,omitempty"`
    Last    *ToolResultState `json:"last,omitempty"`
}

type ToolCallState struct {
    ID        string          `json:"id"`
    Name      string          `json:"name"`
    Arguments json.RawMessage `json:"arguments"`
    Status    ToolCallStatus  `json:"status"`
    StartedAt *time.Time      `json:"startedAt,omitempty"`
    Meta      map[string]any  `json:"meta,omitempty"`
}

type ToolCallStatus string

const (
    ToolCallPending  ToolCallStatus = "pending"
    ToolCallRunning  ToolCallStatus = "running"
    ToolCallDone     ToolCallStatus = "done"
    ToolCallFailed   ToolCallStatus = "failed"
    ToolCallSkipped  ToolCallStatus = "skipped"
)

type ToolResultState struct {
    ToolCallID string         `json:"toolCallId"`
    Output     string         `json:"output,omitempty"`
    Structured map[string]any `json:"structured,omitempty"`
    Error      string         `json:"error,omitempty"`
    ExitCode   int            `json:"exitCode"`
}
```

### Todo State

```go
type TodoState struct {
    ID      string     `json:"id"`
    Content string     `json:"content"`
    Status  TodoStatus `json:"status"`
}

type TodoStatus string

const (
    TodoPending    TodoStatus = "pending"
    TodoInProgress TodoStatus = "in_progress"
    TodoDone       TodoStatus = "done"
    TodoFailed     TodoStatus = "failed"
    TodoCancelled  TodoStatus = "cancelled"
)
```

### Approval State

```go
type ApprovalState struct {
    Waiting  *ApprovalRequestState  `json:"waiting,omitempty"`
    Resolved []ApprovalDecisionState `json:"resolved,omitempty"`
    Grants   []ApprovalGrantState    `json:"grants,omitempty"`
}

type ApprovalRequestState struct {
    ID          string         `json:"id"`
    ToolCallID  string         `json:"toolCallId,omitempty"`
    Title       string         `json:"title"`
    Description string         `json:"description,omitempty"`
    Risk        string         `json:"risk,omitempty"`
    Options     []string       `json:"options,omitempty"`
    Payload     map[string]any `json:"payload,omitempty"`
    ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
}

type ApprovalDecisionState struct {
    RequestID string         `json:"requestId"`
    Action    string         `json:"action"`
    Reason    string         `json:"reason,omitempty"`
    Scope     string         `json:"scope,omitempty"`
    Payload   map[string]any `json:"payload,omitempty"`
    DecidedAt time.Time      `json:"decidedAt"`
}

type ApprovalGrantState struct {
    RequestID   string    `json:"requestId"`
    Action      string    `json:"action"`
    Scope       string    `json:"scope"`
    Fingerprint string    `json:"fingerprint,omitempty"`
    RuleKey     string    `json:"ruleKey,omitempty"`
    GrantedAt   time.Time `json:"grantedAt"`
}
```

Run/rule grants are explicit checkpoint state so approval reuse remains
deterministic after process restart. They are scoped to one run and never imply
cross-run authorization.

### Subtask State

```go
type SubtaskState struct {
    ID        string         `json:"id"`
    ParentID  string         `json:"parentId,omitempty"`
    AgentName string         `json:"agentName"`
    Input     string         `json:"input"`
    Status    SubtaskStatus  `json:"status"`
    RunID     string         `json:"runId,omitempty"`
    Output    string         `json:"output,omitempty"`
    Error     string         `json:"error,omitempty"`
    Meta      map[string]any `json:"meta,omitempty"`
}

type SubtaskStatus string

const (
    SubtaskPending   SubtaskStatus = "pending"
    SubtaskRunning   SubtaskStatus = "running"
    SubtaskCompleted SubtaskStatus = "completed"
    SubtaskFailed    SubtaskStatus = "failed"
    SubtaskCancelled SubtaskStatus = "cancelled"
)
```

### Run Control State

```go
type RunControlState struct {
    Status      RunStatus     `json:"status"`
    Interrupt   bool          `json:"interrupt,omitempty"`
    Steers      []SteerState  `json:"steers,omitempty"`
    AwaitingIDs []string      `json:"awaitingIds,omitempty"`
}

type RunStatus string

const (
    RunStatusIdle      RunStatus = "idle"
    RunStatusRunning   RunStatus = "running"
    RunStatusWaiting   RunStatus = "waiting"
    RunStatusCompleted RunStatus = "completed"
    RunStatusCancelled RunStatus = "cancelled"
    RunStatusFailed    RunStatus = "failed"
)

type SteerState struct {
    ID        string    `json:"id"`
    Message   string    `json:"message"`
    CreatedAt time.Time `json:"createdAt"`
}
```

## Checkpoint

```go
type Checkpoint struct {
    Version string    `json:"version"`
    RunID   string    `json:"runId"`
    Seq     int64     `json:"seq"`
    State   RunState  `json:"state"`
    SavedAt time.Time `json:"savedAt"`
}
```

The checkpoint schema version is `zenforge.checkpoint.v1`.

## Event Log

The event log stores events by run:

```text
.zenforge/runs/
  run_123/
    events.jsonl
    checkpoints.jsonl
    latest.json
```

For MVP, JSONL is enough. Later, SQLite should become the default local durable
store.

JSONL event reads use `json.Decoder`, matching platform storage behavior: both
single-line JSON objects and pretty-printed JSON objects are accepted. Corrupt
event JSON fails fast with a parse error. JSONL checkpoint loads use
`latest.json` as the source of truth, so a corrupt historical line in
`checkpoints.jsonl` does not block loading the latest checkpoint.

## Resume Semantics

S1 defines allowed resume points, even before S4 implements the full loop.

Supported resume boundaries for MVP:

- before model call;
- after model call and before tool execution;
- after tool result has been injected into messages;
- while waiting for approval;
- before final summary turn;
- completed or failed terminal state.

Unsupported or limited in MVP:

- resuming a streaming model response mid-token;
- resuming a shell command already running;
- resuming a child sub-agent midway through an uncheckpointed tool call.

Rule:

```text
If an operation cannot be resumed safely, checkpoint before it and retry or
surface a clear error after restart.
```

## S1 Tests

Minimum tests:

- in-memory event log append/read/latest seq;
- JSONL event log append/read/latest seq;
- checkpoint save/load latest;
- checkpoint schema round trip;
- run state phase transition serialization;
- event sequence monotonicity;
- terminal checkpoint behavior;
- corrupt JSONL line handling;
- missing run handling.

## S1 Exit Criteria

- public interfaces compile;
- JSONL event and checkpoint stores are implemented;
- `go test ./...` passes;
- docs explain unsupported resume points;
- no model or tool execution logic is required yet.

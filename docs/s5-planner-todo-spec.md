# S5 Planner And Todo Runtime Spec

S5 makes long tasks first-class.

The goal is to give ZenForge a default planning/todo runtime that can break a
task into steps, execute each step, update status, checkpoint progress, and
produce a final summary.

## S5 Outcome

After S5, ZenForge should support:

- todo state in `RunState`;
- `todo_write`;
- `todo_read`;
- `todo_update`;
- plan/execute/summary preset;
- task lifecycle events;
- per-task max work rounds;
- deterministic failed/cancelled behavior;
- checkpointed todo updates;
- compatibility aliases for current plan tools.

## Design Principles

1. Todos are runtime state, not just prompt text.
2. Todo updates must be checkpointed.
3. Planning is a preset, not mandatory for every agent.
4. Plan/execute/summary should reuse the S4 harness loop.
5. Tool names should be understandable outside ZenMind.
6. Compatibility aliases can exist, but public names should be clean.

## Package Plan

```text
planner/
  todo.go
  state.go
  preset.go
  prompts.go

tools/todo/
  read.go
  write.go
  update.go
```

## Todo State

```go
type Todo struct {
    ID        string         `json:"id"`
    Content   string         `json:"content"`
    Status    TodoStatus     `json:"status"`
    Notes     string         `json:"notes,omitempty"`
    CreatedAt time.Time      `json:"createdAt"`
    UpdatedAt time.Time      `json:"updatedAt"`
    Meta      map[string]any `json:"meta,omitempty"`
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

## Todo Store

In MVP, todos live inside `RunState`.

The tools operate through a run-scoped todo manager:

```go
type Manager interface {
    List(ctx context.Context, runID string) ([]Todo, error)
    Replace(ctx context.Context, runID string, todos []Todo) ([]Todo, error)
    Update(ctx context.Context, runID string, id string, patch Patch) ([]Todo, error)
}
```

The manager mutates run state and triggers checkpoint/event through the harness
recorder.

## Tools

### `todo_write`

Purpose:

Create or replace the current todo list.

Input:

```json
{
  "todos": [
    {"id": "1", "content": "Inspect project structure", "status": "pending"}
  ]
}
```

Rules:

- empty todo list is invalid in planning mode;
- missing IDs can be generated;
- duplicate IDs are rejected;
- status defaults to `pending`;
- every write emits `todo.updated`.

### `todo_read`

Purpose:

Return the current todo list.

Input:

```json
{}
```

### `todo_update`

Purpose:

Update one todo status or notes.

Input:

```json
{
  "id": "1",
  "status": "in_progress",
  "notes": "optional detail"
}
```

Rules:

- id must exist;
- status must be valid;
- terminal status clears active task if applicable;
- every update emits `todo.updated`.

## Compatibility Aliases

Support these as aliases:

```text
plan_add_tasks    -> todo_write
plan_get_tasks    -> todo_read
plan_update_task  -> todo_update
```

The compatibility layer should live in tools or adapter config, not in the core
todo manager.

## Plan/Execute/Summary Preset

Preset name:

```text
PlanExecute
```

Stages:

1. Plan
2. Execute each todo
3. Summary

### Plan Stage

Allowed tools:

- `todo_write`
- optionally read-only research tools configured for planning.

Requirement:

- model must call `todo_write` before plan stage ends.

If no todos are created:

- run fails with `plan_not_created`.

### Execute Stage

For each todo:

- set status to `in_progress`;
- run S4 harness with a task-specific prompt;
- require `todo_update` to terminal status before task ends;
- if task does not reach terminal status, mark failed.

Default per-task prompt:

```text
Task list:
{{todo_list}}

Current task ID: {{todo_id}}
Current task:
{{todo_content}}

Rules:
1. Work only on the current task.
2. Use tools as needed.
3. Before finishing this task, call todo_update with done, failed, or cancelled.
```

### Summary Stage

No tools by default.

Input:

- original request;
- final todo list;
- accumulated execution messages or task summaries.

Output:

- final user-facing answer.

## Events

S5 emits:

- `todo.updated`;
- `task.started`;
- `task.done`;
- `task.error`;
- `task.cancelled`;
- normal S4 model/tool/checkpoint events.

## Checkpoint Behavior

Checkpoint after:

- todo list write;
- todo update;
- task start;
- task terminal status;
- summary start;
- final summary.

Resume behavior:

- if plan stage incomplete, resume planning;
- if executing todo N, resume from active todo;
- if active todo is terminal, move to next todo;
- if all todos terminal, resume summary;
- if summary complete, return terminal result.

## Migration From agent-platform

Source inspiration:

- `internal/llm/plan_execute.go`;
- `internal/tools/tool_plan.go`;
- `contracts.PlanRuntimeState`;
- `contracts.PlanTask`.

Keep:

- plan/execute/summary shape;
- tool-enforced todo creation/update;
- per-task lifecycle events;
- failure when no plan created;
- final summary stage.

Change:

- public tool names become `todo_*`;
- internal state becomes checkpoint-native;
- no dependency on `QuerySession`;
- no Java compatibility payloads in core.

## S5 Tests

Minimum tests:

- `todo_write` creates todos;
- missing IDs generated;
- duplicate IDs rejected;
- `todo_read` returns current todos;
- `todo_update` changes status;
- invalid status rejected;
- plan stage fails without todo write;
- execute stage runs todos in order;
- task without terminal update fails;
- failed task stops plan by default;
- summary runs after all tasks done;
- resume from active todo;
- events and checkpoints emitted on todo changes.

## S5 Exit Criteria

- todo tools work through S2 tool runtime;
- plan/execute preset works with fake model/tools;
- todo state is durable;
- current ZenMind `PLAN_EXECUTE` behavior has a clear migration path;
- no platform imports.


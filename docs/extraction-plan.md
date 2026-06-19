# Extraction Plan

This is the recommended path from the current project to a standalone ZenForge
repo.

## Phase 0: Freeze The Runtime Boundary

Do not move code yet.

Create a small design branch in `agent-platform` that identifies which existing
types are core runtime concepts and which are platform concepts.

Core runtime concepts:

- run
- task
- model request/response
- tool definition
- tool call/result
- event
- approval request/decision
- workspace operation
- checkpoint
- subtask

Platform concepts:

- chat summary
- unread count
- API response envelope
- agent catalog files
- team catalog files
- gateway notifications
- resource tickets
- app archive behavior
- Java compatibility payloads

## Phase 1: Create Public Types In ZenForge

In the new repo, define interfaces and structs from existing platform shapes
where those shapes already exist. Avoid copying large logic at first, but do not
invent replacement wire/storage schemas when the platform already has a proven
one.

Deliverables:

- `model.Model`
- `tool.Tool`
- `workspace.Workspace`
- `checkpoint.CheckpointStore`
- `trace.TraceSink`
- `Event`
- `Task`
- `RunState`
- `Agent`

This lets the API shape stabilize before implementation details arrive while
keeping the extraction faithful to platform behavior.

## Phase 2: Port Event Stream

Start with the event layer because everything else reports through it.

Port from:

```text
agent-platform/internal/stream
```

Keep the platform `stream.EventData` wire shape: flattened `seq`, `type`,
payload fields, and `timestamp`. Publish a smaller core event name set on top of
that shape:

```text
run.started
model.delta
tool.call
tool.result
todo.updated
subtask.started
checkpoint.created
run.done
```

Keep compatibility mappers for ZenMind UI names separately.

## Phase 3: Port Tool Runtime

Port the shape, not the platform coupling.

Source:

```text
agent-platform/internal/contracts/interfaces.go
agent-platform/internal/tools/tool_router.go
agent-platform/internal/tools/tool_executor.go
```

Changes:

- Replace `api.ToolDetailResponse` with public `tool.Definition`.
- Replace `contracts.ExecutionContext` with public `tool.CallContext`.
- Move chat/memory/catalog dependencies behind adapters.
- Keep middleware for timeout, budget, audit, approval, and retry.

## Phase 4: Port Todo And Workspace

Source:

```text
agent-platform/internal/tools/tool_plan.go
agent-platform/internal/filetools
agent-platform/internal/tools/tool_file.go
agent-platform/internal/tools/tool_grep.go
```

Changes:

- Rename public tools to `todo_read`, `todo_write`, `todo_update`.
- Keep compatibility aliases for `plan_add_tasks`, `plan_get_tasks`,
  `plan_update_task`.
- Put file operations behind `workspace.Workspace`.
- Keep file access policy separate from workspace implementation.

## Phase 5: Port Agent Loop

Source:

```text
agent-platform/internal/llm/llm_engine.go
agent-platform/internal/llm/run_stream*.go
agent-platform/internal/llm/mode.go
agent-platform/internal/llm/plan_execute.go
```

Changes:

- Rename `LLMAgentEngine` to `harness.Runner` or keep it internal.
- Convert `QuerySession` into a smaller public `RunConfig`.
- Make prompt construction pluggable.
- Make checkpoint save happen after each model turn and tool result.
- Keep `REACT`, `ONESHOT`, and `PLAN_EXECUTE` as presets.

## Phase 6: Move Sub-Agent Orchestration Into Core

Source:

```text
agent-platform/internal/server/frame_orchestrator.go
agent-platform/internal/llm/orchestration.go
```

Changes:

- Move orchestration out of server package.
- Use public `SubAgentSpec`.
- Keep nested sub-agent protection.
- Route child events through the same event stream.
- Inject aggregated subtask result back into the parent tool call.

## Phase 7: Checkpoint And Resume

Do not treat current chat JSONL as the only checkpoint.

There is no single platform checkpoint package to copy. Create the thinnest
purpose-built checkpoint that captures platform runtime state needed for durable
resume:

```text
run id
task input
messages
todo state
active step
pending tool calls
tool results
workspace snapshot refs
approval state
subtask states
usage
model metadata
```

Then provide adapters:

- memory checkpoint
- JSONL checkpoint
- SQLite checkpoint
- ZenMind chat trace adapter

## Phase 8: CLI And Examples

Status: implemented. Both command entry points use the same runtime assembly;
`code` additionally binds workspace and shell execution to its positional
repository. The SDK, simple-tool, repository-refactor, and code-review examples
exercise embedded, tool-using, and repository-oriented harness flows.

Build:

```bash
zenforge run "task"
zenforge code ./repo "task"
```

Examples should be small but real. Avoid demos that only print model output.

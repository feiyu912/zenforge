# S0 Kickoff

S0 is the project setup and boundary-freeze stage. Its job is not to port all
runtime code. Its job is to make the extraction safe, scoped, and reviewable.

Read `preparation-plan.md` first. It is the current source of truth for what
must be designed before implementation code is copied from `agent-platform`.

## S0 Goal

Create a standalone ZenForge repo skeleton and freeze the public runtime
boundary before copying implementation logic out of `agent-platform`.

By the end of S0, contributors should be able to answer:

- What belongs in ZenForge core?
- What remains a ZenMind platform adapter?
- What public interfaces are stable enough to implement against?
- Which current files are source material for each runtime module?
- What is explicitly out of scope for MVP?

## S0 Deliverables

1. Repository skeleton

```text
zenforge/
  README.md
  docs/
  go.mod
  agent.go
  config.go
  task.go
  events.go
```

2. Public type stubs

```text
model.Model
tool.Tool
workspace.Workspace
checkpoint.CheckpointStore
trace.TraceSink
zenforge.Agent
zenforge.Event
zenforge.Task
```

3. Extraction map

The mapping in `docs/current-project-mapping.md` should be treated as the first
source-of-truth for where runtime logic comes from.

4. MVP acceptance checklist

The checklist in `docs/mvp-scope.md` should decide whether the first version is
actually useful.

## S0 Non-Goals

- No full code port.
- No memory system extraction.
- No MCP extraction.
- No server API compatibility layer.
- No UI work.
- No migration of existing ZenMind users.
- No large package rename inside `agent-platform`.

## Recommended S0 Tasks

1. Create GitHub repo.
2. Copy this `zenforge/` folder into the repo.
3. Decide final module path.
4. Add `go.mod`.
5. Add empty packages and public interfaces.
6. Add CI for `go test ./...`.
7. Open tracking issues for S1 extraction work.

## S0 Acceptance Criteria

- The repo has a clear name and positioning.
- The public package names are agreed.
- The first interfaces compile.
- `go test ./...` passes.
- There is no dependency on `agent-platform/internal/...`.
- The team agrees that implementation starts with event stream, tool runtime,
  todo/workspace, then agent loop.

## S1 Preview

S1 should port the smallest useful vertical slice:

```text
OpenAI-compatible model
  + event stream
  + simple agent loop
  + todo tools
  + local filesystem read/grep
  + shell tool with allowlist
  + JSONL trace/checkpoint
  + CLI run command
```

That gives ZenForge a real demo without dragging the entire platform across the
boundary.

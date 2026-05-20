# S7 Implementation Plan

This document breaks `s7-subagent-runtime-spec.md` into concrete work.

## 1. Subagent Package

Create:

```text
subagent/
```

Implement:

- `SubAgentSpec`;
- `TaskSpec`;
- `TaskResult`;
- `Options`;
- validation helpers.

Acceptance:

- validation tests;
- JSON round-trip tests.

## 2. Registry

Implement in-memory registry.

Acceptance:

- register/list/lookup;
- duplicate name rejected;
- sorted list.

## 3. Task Tool

Create:

```text
tools/task/
```

Implement:

- `task` tool;
- optional `agent_invoke` alias;
- schema.

Acceptance:

- tool satisfies `tool.Tool`;
- invalid args tested;
- alias tested.

## 4. Orchestrator

Implement orchestrator with fake child runner first.

Acceptance:

- single task;
- multiple tasks;
- stable aggregation order;
- child error handling.

## 5. Harness Integration

Teach harness to route runtime task tool calls to orchestrator.

Acceptance:

- parent fake model calls task;
- child fake runner result injected as parent tool result.

## 6. Event Routing

Emit:

- `subtask.started`;
- `subtask.event`;
- `subtask.done`;
- `subtask.error`.

Acceptance:

- event order tests;
- child event wrapping tests.

## 7. Checkpoint/Resume

Add subtask state handling in `RunState`.

Acceptance:

- checkpoint contains subtask states;
- resume skips completed child;
- resume restarts/resumes non-terminal child.

## 8. Nested Guard

Implement max depth or simple boolean guard.

MVP:

- parent depth 0 can call task;
- child depth 1 cannot call task by default.

Acceptance:

- nested call returns `nested_subagent_not_allowed`.

## Done Criteria

- `go test ./...` passes;
- fake parent-child integration test passes;
- no server/chat imports;
- compatibility with future Container Hub sandbox scoping is documented.


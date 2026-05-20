# S5 Implementation Plan

This document breaks `s5-planner-todo-spec.md` into concrete work.

## 1. Planner Package

Create:

```text
planner/
```

Implement:

- `Todo`;
- `TodoStatus`;
- validation helpers;
- ID generation helper;
- formatting helper.

Acceptance:

- status validation tests;
- formatting tests.

## 2. Todo Manager

Implement run-state-backed todo manager.

Acceptance:

- list/replace/update tests;
- duplicate ID rejection;
- checkpoint hook test with fake recorder.

## 3. Todo Tools

Create:

```text
tools/todo/
```

Implement:

- `todo_write`;
- `todo_read`;
- `todo_update`.

Acceptance:

- tools satisfy `tool.Tool`;
- tools work through S2 invoker;
- invalid args return stable errors.

## 4. Compatibility Aliases

Implement optional aliases:

- `plan_add_tasks`;
- `plan_get_tasks`;
- `plan_update_task`.

Acceptance:

- aliases call same underlying manager;
- aliases can be disabled if desired.

## 5. Plan/Execute Preset

Implement:

- plan stage;
- execute stage;
- summary stage;
- stage prompts;
- tool restrictions per stage.

Acceptance:

- fake model plan creates todos;
- fake model execute completes todos;
- fake summary produces final output.

## 6. Resume

Implement resume logic:

- active todo detection;
- terminal todo skip;
- summary resume.

Acceptance:

- resume from todo 2 of 3;
- resume after all todos before summary.

## 7. Events

Emit:

- `todo.updated`;
- `task.started`;
- `task.done`;
- `task.error`;
- `task.cancelled`.

Acceptance:

- event order tests;
- checkpoint follows todo updates.

## Done Criteria

- `go test ./...` passes;
- fake plan/execute integration test passes;
- todo state survives checkpoint/load;
- no ZenMind imports.


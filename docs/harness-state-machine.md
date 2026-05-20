# Harness State Machine

This document describes the minimal S4 runtime state machine.

## Main Loop

```mermaid
stateDiagram-v2
    [*] --> Created
    Created --> BeforeModel: initialize run state
    BeforeModel --> ModelStreaming: checkpoint + model.started
    ModelStreaming --> AfterModel: model.done
    ModelStreaming --> Failed: model.error
    AfterModel --> Completed: no tool calls
    AfterModel --> BeforeTool: has tool calls
    BeforeTool --> ToolExecuting: checkpoint + tool.call
    ToolExecuting --> AfterTool: tool.result
    ToolExecuting --> Failed: tool.error and fatal
    AfterTool --> BeforeModel: append tool message
    BeforeModel --> FinalTurn: max steps reached
    FinalTurn --> Completed: final answer
    FinalTurn --> Failed: final turn failed
    BeforeModel --> Cancelled: context cancelled
    BeforeTool --> Cancelled: context cancelled
    Completed --> [*]
    Failed --> [*]
    Cancelled --> [*]
```

## Checkpoint Boundaries

Checkpoint before:

- model call;
- tool execution;
- final turn;
- terminal event.

Checkpoint after:

- model turn;
- tool result message injection;
- final answer;
- approval decision in later stages.

## Resume Map

| Phase | Resume Action |
| --- | --- |
| `created` | start from first model call |
| `model` before request | call model |
| `model` after response with tools | execute pending tools |
| `tool` before execution | execute active/pending tool |
| `tool` after result | continue next model turn |
| `approval` | wait through approval broker once S6 exists |
| `finalizing` | run final no-tool turn |
| `completed` | return stored result |
| `failed` | return stored error |
| `cancelled` | return cancelled state |

## Tool Call State

```mermaid
stateDiagram-v2
    [*] --> Pending
    Pending --> Running
    Running --> Done
    Running --> Failed
    Pending --> Skipped
    Done --> [*]
    Failed --> [*]
    Skipped --> [*]
```

## Notes

- S4 does not support resuming a provider stream mid-token.
- S4 does not support resuming an OS process mid-command.
- The retry behavior for tools belongs to S2 middleware.
- Approval pause belongs to S6, but S4 state must be able to represent it.


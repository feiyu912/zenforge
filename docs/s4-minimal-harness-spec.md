# S4 Minimal Agent Harness Spec

S4 builds the first real agent loop.

The goal is a small but durable ReAct-style harness that can call a model,
execute tools, checkpoint state, stream events, and finish with a final answer.

## S4 Outcome

After S4, ZenForge should support:

- `Agent.Stream`;
- `Agent.Run`;
- model streaming;
- tool-call parsing;
- tool invocation through S2 runtime;
- checkpointing through S1 runtime;
- workspace/shell tools from S3;
- max steps;
- cancellation;
- final no-tool answer turn;
- platform-compatible `react`, `oneshot`, and `plan_execute` presets;
- basic resume from safe boundaries.

## Design Principles

1. The harness receives normalized inputs.
2. Prompt assembly is not platform-specific.
3. Tool execution goes only through the tool runtime.
4. Every state boundary checkpoints.
5. Events are emitted in public ZenForge names.
6. The model adapter is replaceable.
7. The minimal loop should be boring and testable.

## Package Plan

```text
harness/
  runner.go
  config.go
  loop.go
  turn.go
  messages.go
  resume.go
  final.go

model/openai/
  client.go
  stream.go
  tool_call.go
```

## Runner

```go
type Runner struct {
    MaxSteps int
    Mode     string

    Emit            func(RuntimeEvent, map[string]any) error
    Checkpoint      func(context.Context, RunState) error
    CallModel       func(context.Context, RunState, model.ToolChoice) (MessageState, model.Usage, error)
    RunPendingTools func(context.Context, *RunState) error
}
```

The production runner also receives resume and error-classification hooks.
Root `zenforge.Agent` owns concrete stores and integrations, then adapts them to
these hooks. This preserves the package boundary because `checkpoint` stores a
`harness.RunState` and therefore cannot be imported back into `harness`.

## Run Config

```go
type RunConfig struct {
    RunID        string
    Input        string
    Instructions string
    Mode         AgentMode
    Messages     []MessageState
    MaxSteps     int
    ToolChoice   ToolChoice
    Metadata     map[string]any
}
```

Mode behavior follows `agent-platform/internal/llm/mode.go`: `react` uses the
configured loop budget, `oneshot` caps the loop at two model/tool rounds, and
`plan_execute` selects the S5 durable planner preset. The chosen mode is stored
in `RunState` and remains authoritative on resume.

`RunConfig` is deliberately smaller than ZenMind `QuerySession`.

It does not include:

- chat summaries;
- agent catalog definitions;
- memory store handles;
- frontend tool registries;
- gateway notifications;
- resource tickets.

## Loop State

The loop uses `RunState` from S1.

Simplified flow:

```text
create/load RunState
emit run.started or run.resumed
for step < maxSteps:
  checkpoint before model
  call model
  stream model.delta
  collect assistant message/tool calls
  checkpoint after model
  if no tool calls:
    finish run
  for each tool call:
    checkpoint before tool
    invoke tool
    append tool result message
    checkpoint after tool
  continue
final no-tool answer turn if max steps reached with tools
terminal checkpoint
emit run.done/run.error
```

## Tool Call Handling

Model adapters should normalize tool calls into:

```go
type ToolCallSpec struct {
    ID        string
    Name      string
    Arguments json.RawMessage
}
```

The harness should not parse provider-specific chunk formats. That belongs in
model adapters.

## Model Interface Requirements

The model-facing event type is internal to model adapters. It is not the public
durable `zenforge.Event` wire shape from S1.

The current placeholder model interface may need to evolve into:

```go
type Model interface {
    Stream(ctx context.Context, req Request) (<-chan Event, error)
}

type Event struct {
    Type      EventType
    Delta     string
    Message   *Message
    ToolCalls []ToolCallSpec
    Usage     Usage
    Error     error
}
```

Model adapter responsibilities:

- provider HTTP request;
- provider streaming parse;
- tool-call chunk accumulation;
- usage extraction;
- provider errors.

Harness responsibilities:

- runtime state;
- event log/checkpoint;
- tool execution;
- loop decisions.

## Message Model

The harness needs model-facing messages:

```go
type Message struct {
    Role       string
    Content    string
    Name       string
    ToolCallID string
    ToolCalls  []ToolCallSpec
}
```

Roles:

- system;
- user;
- assistant;
- tool.

## Final Answer Turn

When max steps is reached and the last model turn requested tools, the harness
should do one final no-tool turn:

```text
You have reached the tool-use limit. Provide the best final answer using the
available context.
```

This mirrors the useful behavior already present in `agent-platform`.

## Resume

MVP resume should support:

- before model call: call model;
- after model call with pending tools: execute pending tools;
- after tool result injected: continue next model turn;
- waiting approval: wait for approval broker once S6 exists;
- terminal state: return terminal result.

MVP should not support:

- resuming mid-provider stream;
- resuming an already-running OS command;
- resuming uncheckpointed child sub-agent internals.

## Events

Harness emits:

- `run.started`;
- `run.resumed`;
- `step.started`;
- `model.started`;
- `model.delta`;
- `model.done`;
- `tool.call`;
- `tool.result`;
- `tool.error`;
- `checkpoint.created`;
- `run.done`;
- `run.error`;
- `run.cancelled`.

## Prompt Assembly

Minimal prompt assembly in S4:

```text
system instructions
history messages
current user input
```

Do not port ZenMind prompt sections yet:

- agent identity;
- SOUL;
- memory;
- owner context;
- skill catalog;
- sandbox prompt;
- all-agents prompt.

Those should enter through adapters or later prompt providers.

## Migration From agent-platform

Source inspiration:

- `internal/llm/run_stream_turn.go`;
- `internal/llm/run_stream_tools.go`;
- `internal/llm/protocol*.go`;
- `internal/llm/mode.go`;
- `internal/contracts/delta.go`.

Do not directly copy:

- `LLMAgentEngine` as public API;
- `QuerySession`;
- `buildSystemPrompt`;
- `RunExecutorParams`;
- `StepWriter`;
- server frame orchestrator;
- frontend submit coordinator.

## S4 Tests

Minimum tests:

- model returns text, run completes;
- model returns tool call, tool runs, model gets tool result;
- max steps triggers final no-tool turn;
- oneshot caps the loop at two rounds and persists its mode across resume;
- checkpoint before and after tool call;
- event order is stable;
- cancellation before model call;
- cancellation before tool call;
- resume from pending tool state;
- tool error becomes model-visible tool result;
- final output returned by `Agent.Run`.

## S4 Exit Criteria

- a fake model and fake tool can complete a multi-step run;
- checkpoints are written at every boundary;
- event log replay shows meaningful progress;
- no ZenMind platform imports;
- root `Agent.Stream` uses the harness runner.

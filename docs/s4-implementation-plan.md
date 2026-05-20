# S4 Implementation Plan

This document breaks `s4-minimal-harness-spec.md` into concrete work.

## 1. Refine Model Interface

Update `model` package:

- message type;
- tool call spec;
- stream event type;
- usage type;
- request type.

Acceptance:

- fake model can emit text and tool calls;
- model package has no tool runtime dependency cycle.

## 2. Add Harness Package

Create:

```text
harness/
  runner.go
  config.go
  loop.go
  messages.go
  resume.go
```

Acceptance:

- runner can be constructed with fake dependencies;
- no root package cycle.

## 3. Implement Basic Loop With Fake Model

Implement:

- run started;
- step started;
- model started/done;
- model delta;
- run done.

Acceptance:

- text-only fake model test passes;
- events persisted in memory event log;
- checkpoint saved.

## 4. Tool Call Loop

Implement:

- collect tool calls from model event;
- checkpoint pending tools;
- invoke tool runtime;
- append tool result message;
- continue next model turn.

Acceptance:

- fake model asks for tool then final answer;
- tool result appears in messages;
- tool events emitted.

## 5. Max Steps And Final Turn

Implement:

- max step guard;
- final no-tool request;
- fallback error if final turn also cannot finish.

Acceptance:

- deterministic max step test;
- final answer event.

## 6. Resume

Implement minimal resume from checkpoint:

- before model;
- pending tool;
- after tool.

Acceptance:

- test creates checkpoint then resumes;
- pending tool executes once;
- terminal checkpoint returns terminal result.

## 7. Root Agent Integration

Make root `zenforge.Agent` delegate to `harness.Runner`.

Acceptance:

- existing root tests still pass;
- `Agent.Stream` returns harness events;
- `Agent.Run` returns final output.

## 8. OpenAI-Compatible Adapter Stub

Add package:

```text
model/openai/
```

MVP for S4 can include types and request shaping; full streaming parser can land
incrementally.

Acceptance:

- package compiles;
- docs show intended config.

## Done Criteria

- `go test ./...` passes;
- fake model/tool integration test proves the harness;
- no platform imports;
- event/checkpoint/tool packages are used through public interfaces.


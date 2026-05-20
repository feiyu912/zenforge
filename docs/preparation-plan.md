# Preparation Plan

This document refines the ZenForge extraction plan after a deeper read of
`agent-platform` and `agent-container-hub`.

The most important conclusion:

```text
Do not start by copying packages.
Start by freezing the runtime boundary and durable state model.
```

The existing project already has a strong runtime, but core execution logic is
interwoven with ZenMind platform concerns: chat JSONL, catalog loading, prompt
assembly, gateway notifications, resource tickets, frontend awaiting protocol,
memory stores, and server orchestration.

## Executive Summary

ZenForge should extract four reusable core layers:

1. Harness loop
2. Tool runtime and policy middleware
3. Event/checkpoint runtime
4. Sub-agent orchestration

ZenMind-specific layers should remain adapters:

1. Chat store and replay UI
2. Agent/team/skill catalog directories
3. HTTP/SSE/WebSocket server APIs
4. Memory store
5. Gateway/resource-ticket/artifact protocols
6. Desktop/frontend tool protocols

## What The Current Platform Already Has

| Area | Current Status | Extraction Readiness |
| --- | --- | --- |
| ReAct loop | Strong | Good, after interface cleanup |
| Oneshot mode | Thin wrapper over loop | Good |
| Plan/execute mode | Strong | Good, after plan state abstraction |
| Tool router | Strong | Good |
| Tool safety | Strong | Very good |
| File tools | Strong | Good, after workspace abstraction |
| Bash security | Strong | Very good |
| HITL rules | Strong | Good, but approval protocol must be abstracted |
| Sub-agent | Works | Medium, server-coupled |
| Event stream | Strong for UI | Medium, needs public event contract |
| Live attach | Works in-process | Not durable |
| Checkpoint/resume | Partial trace only | Needs new core design |
| Memory | Rich | Defer; adapter after MVP |
| Container Hub | Strong external sandbox | Adapter/backend |

## Critical Finding: Trace Is Not Checkpoint

Current `agent-platform` can:

- reattach to an active in-process run;
- replay chat history from JSONL for UI;
- persist pending awaiting metadata;
- rebuild front-end event snapshots.

It cannot yet:

- resume a half-finished model/tool loop after process restart;
- restore assembler state;
- restore pending tool calls;
- restore run-control waiters and steer queue;
- restore sub-agent execution state;
- guarantee every stream event is durably written before a crash.

Therefore ZenForge needs first-class durable runtime primitives:

```text
RunEventLog
CheckpointStore
RunState
AssemblerSnapshot
RunControlSnapshot
ToolCallSnapshot
PlanSnapshot
SubtaskSnapshot
ApprovalSnapshot
```

Chat JSONL can become a trace/read-model adapter, not the checkpoint source of
truth.

## Recommended Extraction Order

### S0: Boundary Freeze

No implementation migration yet.

Deliverables:

- Public interfaces compile.
- Runtime state model is written down.
- Event contract is named and documented.
- Adapter boundaries are clear.
- Existing platform files are mapped to target modules.

### S1: Event And Durable State Core

Start here before agent loop extraction.

Build:

- `Event`
- `RunEventLog`
- `CheckpointStore`
- `RunState`
- `RunControl` snapshot model
- in-memory event bus as fanout only
- JSONL event/checkpoint store for local development

Reason:

The agent loop, HITL, tools, sub-agents, and resume all depend on this boundary.
If this is wrong, later extraction will inherit platform assumptions.

### S2: Tool Runtime Core

Extract the tool layer before the full LLM loop.

Source concepts:

- `contracts.ToolExecutor`
- `tools.ToolRouter`
- `tools.ToolExecutionResult`
- `ToolRouter.invokeWithPolicy`
- embedded tool definitions

Core features:

- tool registry;
- typed tool helper;
- schema handling;
- timeout;
- retry;
- budget;
- audit event;
- approval request hook;
- structured result normalization.

Do not include in core:

- memory tools;
- session search;
- artifact gateway;
- desktop bridge;
- skill candidate store;
- ZenMind API DTOs.

### S3: Safety Middleware And Workspace

Extract:

- `bashast`
- `bashsec`
- file access plan logic
- file write plan logic
- read-before-write snapshots
- local workspace implementation

Important boundary:

`filetools` should become policy + workspace operations, not a direct dependency
on `contracts.ExecutionContext` or `config.FileToolsConfig`.

### S4: Minimal Harness Loop

Only after events, checkpoints, and tools exist.

Extract from:

- `LLMAgentEngine`
- `llmRunStream`
- provider protocol code
- `mode.go`

But do not copy the current engine shape directly. The current `LLMAgentEngine`
is an assembly facade that knows about config, model registry, frontend tools,
sandbox, prompt builder, HITL checker, and session DTOs.

Core should receive already-normalized inputs:

```text
RunConfig
Model
Prompt
Messages
ToolRegistry
Policies
CheckpointStore
EventSink
```

### S5: Planner/Todo

Port `PLAN_EXECUTE` after the minimal harness loop works.

Extract:

- plan/execute/summary state machine;
- plan state;
- task lifecycle events;
- `todo_write`, `todo_read`, `todo_update`.

Compatibility aliases can map:

```text
plan_add_tasks    -> todo_write
plan_get_tasks    -> todo_read
plan_update_task  -> todo_update
```

### S6: Approval/HITL

Extract the policy model, not the ZenMind submit protocol.

Core should define:

```text
ApprovalRequest
ApprovalDecision
ApprovalBroker
ApprovalPolicy
```

ZenMind adapter can map these to:

```text
awaiting.ask
request.submit
awaiting.answer
/api/submit
WebSocket notification
```

### S7: Sub-Agent Orchestration

Extract `agent_invoke` as a core orchestration concept.

Current behavior:

- `agent_invoke` is not a normal backend tool;
- the LLM loop turns it into `DeltaInvokeSubAgents`;
- server frame orchestrator intercepts and runs children;
- child runs are concurrent;
- nested sub-agent invocation is blocked;
- child sandbox sessions are isolated by `SubTaskID`.

Target boundary:

```text
SubAgentOrchestrator
AgentRegistry
SessionFactory
ChildRunner
EventRouter
```

Do not directly move `frame_orchestrator.go`; it mixes chat, resource tickets,
system init, proxy behavior, and server routing.

### S8: Container Hub Adapter

Keep Container Hub as an external sandbox backend.

ZenForge core should only define:

```text
Sandbox
SandboxSession
EnvironmentPromptProvider
```

The adapter can call Container Hub HTTP APIs.

## Core vs Adapter Boundary

### Core

- event types and event log;
- checkpoint store interface;
- run state and run control;
- model interface;
- provider protocol adapters;
- tool interface and router;
- tool policy middleware;
- bash/file safety primitives;
- workspace interface;
- ReAct loop;
- plan/execute loop;
- approval interface;
- sub-agent orchestrator interface.

### ZenMind Adapter

- `chat.FileStore`;
- `StepWriter`;
- `LoadChat` replay projector;
- `BuildQuerySession`;
- `catalog.AgentDefinition` loading from directories;
- memory context building;
- skill catalog prompt injection;
- resource tickets;
- gateway notifications;
- HTTP and WebSocket routes;
- frontend tools;
- artifact publish;
- desktop bridge;
- schedule execution.

## High-Risk Areas

1. Checkpoint semantics
   If checkpoint is treated as chat replay, resume will be fake. Define durable
   state explicitly.

2. Prompt assembly
   The current prompt builder is product-specific. Core should accept prompt
   sections or a prompt provider.

3. Approval protocol
   The current protocol is tightly tied to ZenMind frontend/server. Core needs a
   neutral approval broker.

4. Sub-agent orchestration
   The current implementation is correct but server-coupled. Extract the state
   machine, not the file.

5. Tool DTO leakage
   Avoid importing `api.ToolDetailResponse` or `contracts.ExecutionContext` into
   ZenForge.

6. Memory scope
   Memory is valuable but heavy. Defer until core loop and checkpoint are stable.

## Immediate Next Steps

1. Replace the current S0 stub with stable public interfaces for:
   - `RunEventLog`
   - `CheckpointStore`
   - `RunState`
   - `RunControl`
   - `ApprovalBroker`
   - `ToolRegistry`
   - `Workspace`

2. Write architecture decision records for:
   - event log vs checkpoint;
   - tool policy middleware;
   - approval broker;
   - sub-agent orchestration;
   - Container Hub adapter boundary.

3. Add package skeletons, but keep implementations minimal.

4. Do not port `llmRunStream` until S1/S2 compile.


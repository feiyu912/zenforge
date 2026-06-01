# ZenMind Adapter Guide

ZenForge core emits neutral runtime events. ZenMind can keep its existing UI
and server protocols by mapping those events at the platform boundary.

The `adapters/zenmind` package provides compatibility helpers without importing
ZenMind platform packages.

## Catalog And Session Mapping

Host platform code can translate catalog/session data into a normalized
ZenForge config and task:

```go
run, err := zenmind.BuildRun(ctx, zenmind.CatalogAgent{
    Name:         "reviewer",
    Instructions: "Review carefully.",
    Model:        zenmind.ModelRef{Provider: "openai", Name: "gpt-4.1"},
    ToolNames:    []string{"workspace_read", "workspace_grep"},
    MaxSteps:     20,
    Planning:     "plan_execute",
}, zenmind.Session{
    RunID:          "run_123",
    Input:          "Analyze this repo.",
    UserID:         "user_1",
    ConversationID: "chat_1",
    Memory: []zenmind.MemoryEntry{{
        ID:   "profile",
        Text: "The user prefers concise answers.",
    }},
}, zenmind.Runtime{
    Model:       model,
    Tools:       tools,
    Events:      events,
    Checkpoints: checkpoints,
})
if err != nil {
    return err
}

agent := zenforge.New(run.Config)
events, err := agent.Stream(ctx, run.Task)
```

The adapter filters tools by catalog names, maps planning/sub-agent modes, adds
platform session metadata under `task.Meta["zenmind"]`, and uses
`adapters/memory` to inject retrieved memory into the normalized task input.
The host still owns catalog loading, auth, tenancy, model construction, tool
construction, and storage selection.

## Feature Flag And Fallback Routing

ZenMind should not replace its current runtime in one jump. Use `Router` to
decide whether a catalog/session should run through ZenForge or the legacy
runtime:

```go
router := zenmind.Router{
    Default: zenmind.RouteLegacy,
    Agents: map[string]zenmind.RouteDecision{
        "reviewer": zenmind.RouteZenForge,
    },
}

decision := router.Decide(agent, session)
if !decision.UseZenForge() {
    return runLegacy(ctx, session)
}

run, err := zenmind.BuildRun(ctx, agent, session, runtime)
```

Routing can be controlled by explicit agent/session maps or by metadata such as
`{"zenforge": true}`. The default is `legacy`, so rollout is opt-in.

```go
mapped := zenmind.MapEvent(event)
```

Default event names follow the compatibility mapping from ADR 0002:

| ZenForge event | Adapter event |
| --- | --- |
| `run.started` | `run.start` |
| `run.done` | `run.complete` |
| `model.delta` | `content.delta` |
| `tool.call` | `tool.start` |
| `tool.result` | `tool.result` |
| `todo.updated` | `plan.update` |
| `approval.requested` | `awaiting.ask` |
| `approval.resolved` | `awaiting.answer` |
| `subtask.started` | `task.start` |
| `subtask.done` | `task.complete` |

Host code can override names when a specific frontend stream expects a
different split, for example mapping model deltas to `reasoning.delta`.

```go
mapper := zenmind.NewMapper()
mapper.Types[zenforge.EventModelDelta] = "reasoning.delta"
```

## Approval Submit Mapping

ZenMind submit routes should translate their payloads into
`approval.Decision`, then pass the decision to the configured approval broker.

```go
decision, err := zenmind.DecisionFromJSON(body)
```

The adapter helper understands the neutral fields:

```json
{
  "requestId": "approval_123",
  "action": "approve",
  "scope": "once",
  "reason": "",
  "payload": {}
}
```

Core still does not know about `/api/submit`, WebSocket messages, pending
awaiting repositories, or frontend form DTOs. Those remain in the host
platform.

## Chat JSONL Read Model

ZenMind can keep a chat JSONL-style read model by projecting ZenForge events at
the platform boundary:

```go
writer := zenmind.NewChatJSONLWriter(".zenmind/chats", zenmind.NewMapper())

for event := range events {
    if err := writer.Append(ctx, event); err != nil {
        return err
    }
}
```

Each line stores a `zenmind.chat_trace.v1` record with the mapped event type,
source ZenForge event type, run id, sequence, timestamp, payload, and write time.

```go
records, err := zenmind.ReadChatRecords(ctx, ".zenmind/chats", "run_123")
```

This is a read-model projection. It is not the checkpoint source of truth.

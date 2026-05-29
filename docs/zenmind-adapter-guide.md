# ZenMind Adapter Guide

ZenForge core emits neutral runtime events. ZenMind can keep its existing UI
and server protocols by mapping those events at the platform boundary.

The `adapters/zenmind` package provides compatibility helpers without importing
ZenMind platform packages.

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

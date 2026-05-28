# Current Project Mapping

This document maps the existing ZenMind codebase to the future ZenForge runtime.

## Main Source Repository

The reusable core is mostly inside:

```text
agent-platform/
```

Important supporting runtime:

```text
agent-container-hub/
```

Product and platform layers that should remain adapters:

```text
zenmind-app-server/
zenmind-desktop/
agent-webclient/
platform-bridge-*
wecom-go-skill/
agw-cli/
```

## Existing Capabilities

| Capability | Current Location | Extraction Decision |
| --- | --- | --- |
| Agent loop | `agent-platform/internal/llm/llm_engine.go`, `run_stream*.go` | Extract into `harness.Runner` |
| ReAct mode | `internal/llm/mode.go`, `run_stream_turn.go` | Core |
| Oneshot mode | `internal/llm/mode.go` | Core preset |
| Plan/execute mode | `internal/llm/plan_execute.go` | Core planner preset |
| Todo/plan tools | `internal/tools/tool_plan.go` | Core planner tools |
| Tool interface | `internal/contracts/interfaces.go` | Extract shape as public `tool.Tool` |
| Tool router | `internal/tools/tool_router.go` | Core registry/router with adapters |
| Built-in tools | `internal/tools/tool_executor.go` | Split into core builtins and platform adapters |
| File tools | `internal/tools/tool_file.go`, `internal/filetools` | Core, behind `workspace.Workspace` and policy |
| Shell tool | `internal/tools/tool_bash*.go`, `internal/bashsec` | Core optional builtin |
| Sandbox execution | `internal/sandbox`, `agent-container-hub` | Adapter/backend |
| HITL approval | `internal/hitl`, `run_stream_hitl*.go` | Core policy/approval layer |
| Sub-agent delegation | `internal/server/frame_orchestrator.go`, `agent_invoke` | Core, but currently too server-coupled |
| Event stream | `internal/stream` | Core; preserve `stream.EventData` wire shape and narrow core names |
| Active run manager | `internal/contracts/run_control.go` | Core run control; preserve `RunLoopState` names |
| Live observers | `internal/stream/event_bus.go` | Core |
| Chat JSONL trace | `internal/chat` | Adapter; not the core checkpoint format |
| Memory | `internal/memory` | Adapter after MVP |
| MCP tools | `internal/mcp` | Adapter after core tool API stabilizes |
| Catalog agents | `internal/catalog` | Platform adapter |
| HTTP/SSE/WS APIs | `internal/server`, `internal/ws` | Platform adapter |

## What Is Already Strong

- The runtime already supports streaming model output and tool calls.
- `PLAN_EXECUTE` is a real long-task workflow, not just a prompt convention.
- Tool safety is unusually mature for an early agent runtime:
  file allowlists, bash security review, approvals, sandbox execution, timeouts,
  and audit-friendly results are all present.
- Sub-agent delegation exists through `agent_invoke`.
- Event replay and active-run observer mechanics already exist.
- Container Hub is a practical sandbox backend.

## Main Gaps Before ZenForge Can Stand Alone

1. Public API boundary.
   Current types depend on `internal/api`, `contracts.QuerySession`, chat
   storage, catalog, server lifecycle, and Java-compatible payload shapes.

2. Durable checkpoint/resume.
   Current chat JSONL is excellent trace/replay material, but not a clean
   runtime checkpoint that can resume a partially completed loop after process
   restart.

3. Workspace abstraction.
   File tools exist, but there is no first-class `Workspace` interface that can
   be backed by local files, sandbox files, object storage, or memory.

4. Sub-agent coupling.
   Sub-agent orchestration currently lives in the server frame orchestrator.
   It should move into the harness runtime.

5. Event contract.
   Existing events are good, but public events should be stable, documented, and
   less coupled to chat UI naming.

6. Separation from platform concerns.
   Catalog, chat summaries, gateway notifications, resource tickets, app
   unread state, and archive behavior should not be runtime dependencies.

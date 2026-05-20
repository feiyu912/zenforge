# ZenForge Vision

## Name

Working name: **ZenForge**.

Why this name works:

- It keeps a subtle link to ZenMind without making the runtime sound like a
  private internal component.
- "Forge" suggests building durable agents from models, tools, memory,
  policies, and execution state.
- The Go package name can be simple: `zenforge`.
- The GitHub repo can be `zenforge`, `zenforge-go`, or `zenforge-runtime`.

Other names worth considering:

- **TaskForge**: clearer, more generic, but less distinctive.
- **AgentForge**: obvious, but already close to many existing project names.
- **ZenRun**: clean and runtime-oriented, but less expressive.
- **MindForge**: memorable, but slightly broader than the actual runtime.

Recommendation: start with **ZenForge** unless the GitHub name is unavailable.

## One-Line Positioning

ZenForge is a production-first Go harness for long-running, tool-using agents.

## Longer Positioning

ZenForge gives Go teams a default agent runtime with planner, todo state,
workspace tools, model streaming, tool execution, sub-agent delegation,
human-in-the-loop approvals, trace events, and checkpoint/resume support.

It should feel like a runtime you can deploy, not a pile of tiny abstractions you
must assemble before doing useful work.

## Core Promise

```text
Create one agent.
Give it tools.
Stream its work.
Pause for approvals.
Resume after failures.
Observe every step.
```

## Design Principles

1. Default agent first.
   The happy path is `agent.Stream(ctx, task)`, not a chain builder.

2. Production boundaries first.
   Tool permissions, audit, timeout, cancellation, checkpoints, and trace are
   core runtime concerns, not optional demos.

3. Go-native ergonomics.
   Prefer interfaces, structs, contexts, typed options, and explicit errors.
   Avoid fluent chains and magic global registries.

4. Replaceable internals.
   Models, tools, workspace, checkpoint store, memory, trace sink, approval
   policy, and sandbox backend must be swappable.

5. Platform adapters stay outside core.
   ZenMind chat storage, catalog files, WebSocket routing, mobile gateways,
   app-server APIs, and UI-specific payloads should be adapters, not runtime
   dependencies.

## Non-Goals For The First Public Version

- Competing with LangChain's ecosystem breadth.
- Shipping many retrievers/vector stores.
- Building a full multi-tenant SaaS platform.
- Recreating every current `agent-platform` API.
- Preserving every Java-runtime compatibility detail in the public core.


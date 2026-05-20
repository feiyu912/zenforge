# ADR 0006: Package Layout

Status: proposed

## Context

ZenForge must be easy to use at the top level but still provide replaceable
runtime parts. A flat package would become crowded; a too-deep package tree
would feel like a framework maze.

## Decision

Use a small high-level root package and focused subpackages.

## Layout

```text
github.com/feiyu912/zenforge
  agent.go
  config.go
  task.go
  events.go

harness/
  runner.go
  state.go
  loop.go
  checkpoint.go

model/
  interface.go
model/openai/
model/anthropic/

tool/
  interface.go
  registry.go
  middleware.go

tools/
  todo/
  workspace/
  shell/
  http/
  task/

workspace/
  interface.go
workspace/local/
workspace/memory/

checkpoint/
  interface.go
checkpoint/memory/
checkpoint/jsonl/
checkpoint/sqlite/

eventlog/
  interface.go
eventlog/memory/
eventlog/jsonl/

approval/
  interface.go
approval/cli/

subagent/
  orchestrator.go

sandbox/
  interface.go
sandbox/containerhub/

trace/
  interface.go
trace/stdout/
trace/otel/

cmd/zenforge/
  main.go
```

## Root Package Responsibility

The root package should expose the easy path:

```go
agent := zenforge.New(zenforge.Config{...})
events, err := agent.Stream(ctx, zenforge.Task{Input: "..."})
```

It should re-export only the most important interfaces:

- `Tool`;
- `Model`;
- `Event`;
- `Task`;
- `Result`.

## Avoid

- importing platform packages;
- huge root package;
- hidden global registries;
- requiring CLI/server code for SDK use;
- forcing Container Hub dependency for local tools.


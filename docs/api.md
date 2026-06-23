# API Reference

The full Go API documentation for ZenForge is auto-generated on
[pkg.go.dev](https://pkg.go.dev/github.com/feiyu912/zenforge). Every public
package has its own page.

## Root

- [github.com/feiyu912/zenforge](https://pkg.go.dev/github.com/feiyu912/zenforge) —
  `Agent`, `Task`, `Event`, `Config` — the public surface. The `Agent` type
  and its method set (`Run`, `Stream`, `Resume`) live in the root package.

## Adapters

- [github.com/feiyu912/zenforge/model](https://pkg.go.dev/github.com/feiyu912/zenforge/model) —
  `Model` interface, request/response/event types
- [github.com/feiyu912/zenforge/model/anthropic](https://pkg.go.dev/github.com/feiyu912/zenforge/model/anthropic) —
  Anthropic Messages API adapter
- [github.com/feiyu912/zenforge/model/openai](https://pkg.go.dev/github.com/feiyu912/zenforge/model/openai) —
  OpenAI Chat Completions adapter
- [github.com/feiyu912/zenforge/tool](https://pkg.go.dev/github.com/feiyu912/zenforge/tool) —
  `Tool` interface, `Invoker`, middleware
- [github.com/feiyu912/zenforge/tools](https://pkg.go.dev/github.com/feiyu912/zenforge/tools) —
  registry helpers (`Must`, `FromFunc`)
- [github.com/feiyu912/zenforge/approval](https://pkg.go.dev/github.com/feiyu912/zenforge/approval) —
  `Broker` interface, decisions, scopes
- [github.com/feiyu912/zenforge/approval/cli](https://pkg.go.dev/github.com/feiyu912/zenforge/approval/cli) —
  interactive CLI broker
- [github.com/feiyu912/zenforge/sandbox](https://pkg.go.dev/github.com/feiyu912/zenforge/sandbox) —
  `Sandbox` interface, sessions, mount types

## Runtime

- [github.com/feiyu912/zenforge/harness](https://pkg.go.dev/github.com/feiyu912/zenforge/harness) —
  `Runner` — the agent loop
- [github.com/feiyu912/zenforge/policy](https://pkg.go.dev/github.com/feiyu912/zenforge/policy) —
  `ShellPolicy`, `FilePolicy`
- [github.com/feiyu912/zenforge/planner](https://pkg.go.dev/github.com/feiyu912/zenforge/planner) —
  plan / execute / todo planner
- [github.com/feiyu912/zenforge/subagent](https://pkg.go.dev/github.com/feiyu912/zenforge/subagent) —
  sub-agent spawning and routing
- [github.com/feiyu912/zenforge/workspace](https://pkg.go.dev/github.com/feiyu912/zenforge/workspace) —
  workspace adapters

## Persistence

- [github.com/feiyu912/zenforge/eventlog](https://pkg.go.dev/github.com/feiyu912/zenforge/eventlog) —
  event log interface and types
- [github.com/feiyu912/zenforge/checkpoint](https://pkg.go.dev/github.com/feiyu912/zenforge/checkpoint) —
  checkpoint store interface
- [github.com/feiyu912/zenforge/trace](https://pkg.go.dev/github.com/feiyu912/zenforge/trace) —
  trace sink, redaction, OpenTelemetry

## Extensions

- [github.com/feiyu912/zenforge/recorder](https://pkg.go.dev/github.com/feiyu912/zenforge/recorder) —
  run recorder
- [github.com/feiyu912/zenforge/safety](https://pkg.go.dev/github.com/feiyu912/zenforge/safety) —
  AST and shell safety checks
- [github.com/feiyu912/zenforge/server](https://pkg.go.dev/github.com/feiyu912/zenforge/server) —
  server adapters (HTTP, SSE)
- [github.com/feiyu912/zenforge/adapters](https://pkg.go.dev/github.com/feiyu912/zenforge/adapters) —
  third-party adapters (MCP, memory, zenmind)

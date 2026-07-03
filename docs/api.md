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
- [github.com/feiyu912/zenforge/model/provider](https://pkg.go.dev/github.com/feiyu912/zenforge/model/provider) —
  strict OpenAI/Anthropic adapter construction from config or environment
- [github.com/feiyu912/zenforge/tool](https://pkg.go.dev/github.com/feiyu912/zenforge/tool) —
  `Tool` interface, `Invoker`, middleware
- [github.com/feiyu912/zenforge/tools](https://pkg.go.dev/github.com/feiyu912/zenforge/tools) —
  registry helpers (`Must`, `FromFunc`)
- [github.com/feiyu912/zenforge/skill](https://pkg.go.dev/github.com/feiyu912/zenforge/skill) —
  Agent Skill descriptors, catalogs, immutable `Bundle`, and `load_skill`
- [github.com/feiyu912/zenforge/skill/fs](https://pkg.go.dev/github.com/feiyu912/zenforge/skill/fs) —
  validated filesystem `SKILL.md` catalog
- [github.com/feiyu912/zenforge/approval](https://pkg.go.dev/github.com/feiyu912/zenforge/approval) —
  `Broker` and `GrantStore` interfaces, decisions, scopes, in-memory grants
- [github.com/feiyu912/zenforge/approval/cli](https://pkg.go.dev/github.com/feiyu912/zenforge/approval/cli) —
  interactive CLI broker
- [github.com/feiyu912/zenforge/approval/sqlite](https://pkg.go.dev/github.com/feiyu912/zenforge/approval/sqlite) —
  SQLite-backed persistent rule grants
- [github.com/feiyu912/zenforge/sandbox](https://pkg.go.dev/github.com/feiyu912/zenforge/sandbox) —
  `Sandbox` interface, sessions, mount types
- [github.com/feiyu912/zenforge/sandbox/docker](https://pkg.go.dev/github.com/feiyu912/zenforge/sandbox/docker) —
  local Docker CLI sandbox with conservative isolation defaults

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
- [github.com/feiyu912/zenforge/adapters/zenmind](https://pkg.go.dev/github.com/feiyu912/zenforge/adapters/zenmind) —
  `BuildRun` host resolvers, `ApprovalEventBridge` snapshot correlation,
  run-bound `ProjectStrict`, and event-line projection. It does not implement
  platform transport, pending-awaiting storage, or complete Chat Storage.

## Agent Skills

Applications create a catalog and snapshot it before constructing the agent:

```go
catalog, err := skillfs.New("./skills", skillfs.Options{Source: "my-app"})
if err != nil {
    return err
}
bundle, err := skill.NewBundle(ctx, catalog, nil)
if err != nil {
    return err
}
agent := zenforge.New(zenforge.Config{Model: modelClient, Skills: bundle})
```

`Config.Skills` adds descriptor-only system context and the `load_skill` tool.
Ordinary `Config.Tools` remain callable operations and are not skills. A nil
allowlist snapshots all discovered skills; pass an explicit list to restrict
the bundle.

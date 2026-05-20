# MVP Scope

The first ZenForge version should prove one thing:

```text
A Go developer can create a useful long-running agent with tools, planning,
streaming events, approvals, and checkpoints without adopting the ZenMind
platform.
```

## MVP Features

1. `zenforge.New`
2. `Agent.Run`
3. `Agent.Stream`
4. Model interface plus OpenAI-compatible adapter
5. Tool interface and typed helper
6. Built-in todo tools
7. Built-in local filesystem workspace tools
8. Built-in shell tool with allowlist, timeout, and output limit
9. Event stream
10. In-memory checkpoint store
11. JSONL checkpoint/trace store
12. Basic resume by `runID`
13. Sub-agent task tool
14. CLI: `zenforge run`
15. Examples

## Defer Until After MVP

- Full memory system extraction
- MCP client extraction
- Container Hub as a polished public sandbox backend
- Multi-tenant server APIs
- WebSocket gateway support
- Full ZenMind catalog compatibility
- Schedule execution
- Skill marketplace
- Vector store integrations

## MVP Examples

```text
examples/code-review-agent
examples/research-agent
examples/repo-refactor-agent
examples/customer-support-agent
```

The most important one is `repo-refactor-agent`, because it exercises planning,
file reading, grep, shell, todo updates, trace, and final summary.

## Success Criteria

- A user can run:

```bash
zenforge run "Analyze this repo and produce a refactor plan"
```

- The run streams visible events.
- The agent can create and update todos.
- The agent can read and grep files inside a configured workspace.
- The shell tool is permissioned and time-limited.
- A run produces a checkpoint after every step.
- A process restart can resume from the latest checkpoint for non-awaiting runs.
- The public API does not import `agent-platform/internal/...`.


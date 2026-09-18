# ADR 0061: MCP Server Time Bounds Belong To The Server Entry

Status: accepted

## Context

The MCP client had exactly two time bounds, and both were constants in the
CLI: a 30-second cap on a server's `initialize` + `tools/list` handshake, and
a 60-second budget declared for one tool call. That is the reference's
*default*, but not its model: the reference lets a server entry carry its own
`startupTimeout` and `toolCallTimeout`, because the right bound is a property
of the server, not of the client.

One constant per client forces every server onto the same clock. A server
that is slow to wake (a cold container, a JVM, a remote HTTP bridge) either
fails the handshake that the operator knows is fine, or — if the operator
raises the constant — slows down every other server's failure detection. A
tool that legitimately runs for minutes (a build, a crawl, a batch query)
either gets cut off at the default, or the operator raises the default for
every tool of every server. Both are the same defect: a bound that belongs to
one entry is configured globally.

## Decision

`mcpServers.<name>` accepts two new optional keys:

```json
{
  "mcpServers": {
    "slow-index": {
      "command": "index-server",
      "args": ["--stdio"],
      "startupTimeout": "2m",
      "toolCallTimeout": "10m"
    }
  }
}
```

- Both are Go duration strings, trimmed. Empty means the default
  (30 seconds to start, 60 seconds per call), so an entry that says nothing
  keeps today's behaviour.
- A value that is not a duration, or is not positive, is a configuration
  error naming the key (`parse mcpServers.slow-index.startupTimeout`,
  `mcpServers.slow-index.toolCallTimeout must be positive`). The whole
  section is still validated before any process is started, so a mistyped
  bound cannot half-start a client.
- `mcpServerSpec` carries the two overrides, and two accessors compute the
  effective bounds (`startupTimeout()`, `toolCallTimeout()`), so the defaults
  live in exactly one place and the caller never re-implements the fallback.
- The startup bound is the context that covers `initialize` *and*
  `tools/list` on that server, so one override governs the whole handshake —
  a server is not "started" until it has listed its tools.
- The tool-call bound travels the existing path: it becomes each adapted
  tool's `tool.TimeoutDeclarer` budget, which the timeout policy arms. Nothing
  about the call path changes; only the number does.

## Consequences

- An operator can be patient with a specific server without making every
  other server's failures slower to surface, and can bound a specific
  server's tools without raising the global default.
- The bounds are decided at configuration load, so changing one needs a
  restart — consistent with the rest of the file. No CLI flag is added: flags
  are for modes (`--approve`, `--sandbox`), while per-server facts belong to
  the server's own entry, which is also the only place a name can be.
- A tightened startup bound fails the command loudly when the server does not
  meet it, rather than silently running without the tool set the file asked
  for. That is the same stance as the rest of `mcpServers` (ADR 0054): this
  file is the operator's own, and a quiet degradation is worse than a failure.
- A per-server override is not clamped by any maximum. A caller who writes
  `"24h"` gets 24 hours; the file is trusted, and a bound that the client
  silently second-guessed would be worse than an obviously wrong one.

## Alternatives Rejected

### One global pair of flags

`--mcp-startup-timeout` / `--mcp-tool-call-timeout` would be less code, but a
single value cannot serve a fast local server and a cold remote one at the
same time, which is the whole problem this ADR exists to solve.

### Per-tool time bounds

The reference's unit is the server. A per-tool map would have to be merged
into tool definitions that the server itself owns and renames, and "why did
this call time out" would then depend on two files instead of one.

### Warn and use the default for an unusable value

An operator who writes a bound and is silently ignored believes a protection
is in place that is not. Fail closed at load.

### Reuse the tool-call timeout for the handshake

They bound different things: a handshake is a fixed exchange with a startup
cost, a call is arbitrary work. One server can be slow to start and fast
afterwards, or the reverse, and the two keys exist so that can be said.

## Verification

`go test ./cli/` — the section validator accepts parsed bounds and refuses an
unparseable or non-positive one; the spec accessors return the default for an
entry without bounds and the override for one with them; a helper server that
answers its handshake half a second late fails under a 50 ms per-server bound
in well under the delay and then succeeds under a 10 s bound, so a widened
bound is proven to widen rather than replace; the adapted tools of a server
with a 3 s bound each declare 3 s. `go test ./... -count=1`, `go vet ./...`,
and `gofmt -l`.
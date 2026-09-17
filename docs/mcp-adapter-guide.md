# MCP Adapter Guide

ZenForge can adapt MCP tools into the core `tool.Tool` interface through
`adapters/mcp`.

The adapter keeps MCP at the edge:

- core runtime still only sees ZenForge tools;
- host services own MCP server discovery, auth, and trust; the CLI owns the
  process lifecycle for the servers its own configuration declares;
- tool calls stay visible as normal `tool.call` and `tool.result` events.

## Adapt Tools

```go
client, err := mcp.NewStdioClient(ctx, mcp.StdioConfig{
    Command: "my-mcp-server",
    Args:    []string{"--stdio"},
    Stderr:  os.Stderr,
})
if err != nil {
    return err
}
defer client.Close()

if err := client.Initialize(ctx, mcp.InitializeParams{}); err != nil {
    return err
}

mcpTools, err := mcp.ToolsWithOptions(ctx, client, mcp.ServerOptions{
    Server:          "files",
    Deferred:        true,
    ToolCallTimeout: time.Minute,
})
if err != nil {
    return err
}

agent := zenforge.New(zenforge.Config{
    Tools: mcpTools,
})
```

`ToolsWithOptions` calls `tools/list` and wraps every remote MCP tool as a
ZenForge tool. A model calling that tool causes the adapter to send
`tools/call`. `Server` namespaces the model-visible name
(`mcp__files__read_file`), `Deferred` marks every definition for lazy
activation through `tool_search`, and `ToolCallTimeout` declares the
cooperative budget the timeout policy arms. `Tools` is the short form for a
caller that only ever attaches one server.

## Approval

A remote call crosses a process and possibly a network boundary, so it is a
side effect until the server says otherwise. Unless `ServerOptions.SkipApproval`
is set, a call is gated behind the approval channel:

- `readOnlyHint: true` runs without asking;
- `destructiveHint: true` always asks;
- anything else asks unless the server declared the tool both
  non-destructive and closed-world (`destructiveHint: false` and
  `openWorldHint: false`).

An absent hint is never read as "safe": that is the same rule the reference
harness applies, and it is the difference between a documented read and a
remote delete with no annotations. The request carries the server, the remote
tool name, the hints, and two keys — the rule key names the tool
(`mcp:<server>:<tool>`, what a session-wide "always allow this tool" is
scoped to) and the fingerprint covers the arguments (what a run-scoped
approval is scoped to, so a broad grant cannot be replayed for a different
payload). `ServerOptions.SkipApproval` exists for a caller that has already
made the trust decision outside this package.

## Serving ZenForge

The same package serves the other direction: `mcp.NewServer` (any reader/writer
pair, plus `Handle` for one raw message) and `zenforge mcp-server` over stdio.
A remote caller can list recorded runs and ask for the version; starting a run
requires the operator's `--allow-run` grant, because the client's approval
protects the client's human and not the machine the server runs on. Once
granted, `zenforge_run` starts a run in the server's configured workspace and
returns its final text, its run id, and its status, and the served run's own
tool calls follow the server's `--approve` mode — with `prompt` downgraded to a
refusal, because a stdio server has no keyboard and the interactive broker
would read the protocol stream. Refused calls are reported with the run's
outcome. See the [CLI Design](cli-design.md) for the full policy.

## Result Mapping

MCP text content is joined into `tool.Result.Output`.

If the MCP result includes `structuredContent`, ZenForge copies it into
`tool.Result.Structured`. If the MCP response sets `isError`, the ZenForge tool
result uses `ExitCode: 1` and the text output as `Error`. The result metadata
carries `mcp.server`, `mcp.tool`, `mcp.readOnly`, and `mcp.isError`.

## Transport

`mcp.NewJSONRPCClient` speaks newline-delimited JSON-RPC over any `io.Reader`
and `io.Writer`. One goroutine reads the stream and routes each response to
the call waiting for that id, so a call whose context ends is abandoned
without stopping the reader: the late response is dropped by id and the
connection stays usable. Server-initiated frames (notifications, and requests
for capabilities this client never advertises) are ignored, and an id on such
a frame is never mistaken for the response to a call in flight.

`mcp.NewStdioClient` starts a local command and connects the JSON-RPC client to
the process stdin/stdout. `StdioConfig.Stderr` optionally receives server
diagnostics; it defaults to `io.Discard`, so hosts that need logs must provide
an `io.Writer`. The child environment is the ambient environment with
credential-shaped names scrubbed, plus `StdioConfig.Env`.

`StdioClient.Close` is safe to call repeatedly or concurrently. It closes the
JSON-RPC client, unblocks outstanding RPC calls, closes stdin, allows a short
graceful-exit window, then kills and reaps a process that has not exited, and
waits for the reader to observe the closed stream. Calls after close, including
calls unblocked by close, return an error matching `mcp.ErrClientClosed`.
Normal process exit errors are returned by `Close`; forced shutdown and
parent-context cancellation are treated as expected cleanup.

## Safety Boundary

MCP servers can expose broad filesystem, network, or account access. ZenForge
does not treat MCP tools as inherently safe. Host platforms should:

- choose trusted MCP servers;
- pass least-privilege credentials;
- use approval middleware for risky operations;
- run untrusted MCP servers behind OS or container isolation;
- redact traces before exporting tool arguments or results.

The CLI applies the first three: a server is started only because the
operator's `mcpServers` section names it, its credentials come from that
section's `env` rather than the ambient environment, and a tool that is not
declared read-only goes through the approval broker.

## Deferred

This adapter intentionally starts with tools. MCP resources, prompts, sampling,
elicitation, and server discovery/`listChanged` remain host/platform
responsibilities until the public MCP integration surface is clearer.

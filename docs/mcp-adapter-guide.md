# MCP Adapter Guide

ZenForge can adapt MCP tools into the core `tool.Tool` interface through
`adapters/mcp`.

The adapter keeps MCP at the edge:

- core runtime still only sees ZenForge tools;
- host services own MCP server discovery, auth, trust, and process lifecycle;
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

mcpTools, err := mcp.Tools(ctx, client)
if err != nil {
    return err
}

agent := zenforge.New(zenforge.Config{
    Tools: mcpTools,
})
```

`mcp.Tools` calls `tools/list` and wraps every remote MCP tool as a ZenForge
tool. A model calling that tool causes the adapter to send `tools/call`.

## Result Mapping

MCP text content is joined into `tool.Result.Output`.

If the MCP result includes `structuredContent`, ZenForge copies it into
`tool.Result.Structured`. If the MCP response sets `isError`, the ZenForge tool
result uses `ExitCode: 1` and the text output as `Error`.

## Transport

`mcp.NewJSONRPCClient` implements MCP's `Content-Length` JSON-RPC framing over
any `io.Reader` and `io.Writer`.

`mcp.NewStdioClient` starts a local command and connects the JSON-RPC client to
the process stdin/stdout. `StdioConfig.Stderr` optionally receives server
diagnostics; it defaults to `io.Discard`, so hosts that need logs must provide
an `io.Writer`.

`StdioClient.Close` is safe to call repeatedly or concurrently. It closes the
JSON-RPC client, unblocks outstanding RPC calls, closes stdin, allows a short
graceful-exit window, then kills and reaps a process that has not exited.
Calls after close, including calls unblocked by close, return an error matching
`mcp.ErrClientClosed`. Normal process exit errors are returned by `Close`;
forced shutdown and parent-context cancellation are treated as expected
cleanup.

## Safety Boundary

MCP servers can expose broad filesystem, network, or account access. ZenForge
does not treat MCP tools as inherently safe. Host platforms should:

- choose trusted MCP servers;
- pass least-privilege credentials;
- use approval middleware for risky operations;
- run untrusted MCP servers behind OS or container isolation;
- redact traces before exporting tool arguments or results.

## Deferred

This adapter intentionally starts with tools. MCP resources, prompts, sampling,
server discovery, and OAuth flows remain host/platform responsibilities until
the public MCP integration surface is clearer.

# ADR 0053: An MCP Server, And The Stdio Framing The Spec Actually Uses

Status: accepted

## Context

ZenForge could *consume* MCP servers but not *be* one, so another agent (DSH,
codex, Claude Desktop) had no way to call a ZenForge run. That is the missing
half of the MCP capability, and it is the half that makes the harness
composable rather than only extensible.

Writing the server exposed a second problem in the existing client: it framed
messages with `Content-Length: N\r\n\r\n`, which is the LSP convention, not
MCP's. MCP over stdio is newline-delimited JSON — one message per line, with
no embedded newlines. Every conforming server reads a line and ignores
headers, so the client was speaking a private dialect that happened to work
only against a peer using the same helper.

## Decision

### The framing is newline-delimited JSON, and the reader also accepts headers

`writeFrame` writes one JSON message followed by a newline. `readFrame`
accepts both forms: a line beginning with `{` is the message, and anything
else is treated as the first line of a `Content-Length` header block. Blank
lines between messages are skipped rather than fatal.

Writing only the spec form and reading both is deliberate. A peer that frames
with headers is out of spec but harmless, and failing a connection over a
cosmetic difference is worse than tolerating it; writing headers, by contrast,
breaks against every conforming server, so it had to go.

### The server is transport agnostic and handles one message at a time

`Server.Serve` drives any reader/writer pair, which is what the stdio CLI
wiring and the tests both need, and `Server.Handle` processes a single raw
message for a caller that owns the transport. Requests are handled in order:
MCP over stdio is one ordered stream, the tools this server exposes (a run, a
job) are long and stateful, and concurrency would only make interleaved output
harder to attribute.

### Tool failures are results; protocol failures are JSON-RPC errors

A handler error becomes `{content: [...], isError: true}`, and a malformed
request, unknown method, or unknown tool becomes a JSON-RPC error with the
code the MCP schema names (`-32700`, `-32600`, `-32601`, `-32602`,
`-32603`). A client has to distinguish "the tool failed" from "your request
was malformed"; the first is often worth retrying with different arguments,
the second never is.

### A notification is never answered, and an id is echoed verbatim

A request without an id is a notification: the handler runs and nothing is
written, because the protocol has no way to answer one and an id-less
response would desynchronize the peer. The id is kept as raw JSON, so a
string id comes back as a string — a client that sent `"abc"` and got `1`
back could not match the response to its request.

### Version negotiation never refuses the handshake

A protocol version the server knows is echoed verbatim, as the spec requires.
A version it does not know falls back to the newest it implements, and a
missing version is treated as the newest. Refusing to start because the client
is newer makes every protocol bump a breaking change for no benefit, and the
capabilities the server promises are minimal (`tools`, with `listChanged`
false because the tool set is fixed at startup).

### A panic in a tool is a failed call

`invoke` recovers a panic and returns it as a tool error. A panicking tool
must not take the stream down: the peer would see a closed pipe instead of a
failed call, and the reason would be lost.

## Consequences

Benefits:

- a ZenForge run can be exposed to another agent as an MCP tool, which is the
  composability the reference has and this harness did not;
- the client now interoperates with conforming MCP servers, which is a bug fix
  that was invisible while both ends were the same code;
- the server is testable without a subprocess: the protocol tests drive
  `Handle` directly and one round-trip test drives `Serve` over real pipes;
- the error taxonomy is the protocol's, so a caller can act on it.

Costs and limits:

- no `resources`, `prompts`, `elicitation`, sampling, or `listChanged`
  notifications: the server advertises only `tools`, and a client that asks
  for more finds nothing rather than a promise that never arrives;
- one message at a time, so a long tool call blocks the stream — acceptable
  for a run-oriented server, and a client that wants parallelism starts more
  than one server process;
- the server does not stream progress notifications from a run (a run's
  events are not forwarded as `notifications/progress`), so a long call looks
  silent until it finishes;
- the CLI wiring for the run tools is a separate step; this ADR covers the
  protocol layer.

## Alternatives Rejected

### Keep `Content-Length` Framing

It works against a peer built from the same helper and fails against every
real MCP server, which is the worst combination: it looks tested and is not.

### Answer Notifications With An Error

The spec forbids it, and a client that receives an unsolicited response
cannot match it to anything.

### Report A Tool Failure As A JSON-RPC Error

The client would treat a retryable tool failure as a client bug, and the
message would lose its place in the model's context.

### Run Tools Concurrently

The stream is ordered, the tools are stateful, and interleaving two runs'
output in one stream would make the responses unattributable.

# ADR 0077: The Client Can Lend Its Model

Status: accepted

## Context

Sampling is the last unported piece of C20: the server asks its client to run a
model call. It matters for a served run because the caller already has a model
and an API key — the client is often an agent host — while the ZenForge process
serving the run may have neither. ADR 0074 built the request channel this needs;
what was left was the shape of the delegation and what a run does when the
client will not or cannot answer.

## Decision

### The protocol layer asks; the client answers

`initialize` records whether the client advertised `sampling`, and
`Server.Sample(ctx, SamplingRequest)` sends `sampling/createMessage` through the
existing request machinery, returning the assistant message, the model the
client used, and its stop reason. `SamplingRequest` carries the messages, an
optional system prompt, an optional model hint, and a maximum token count. No
new server capability is advertised: sampling is a client capability, and the
advertised block is unchanged.

A caller's deadline always wins and the default is documented, so a sample can
never wait forever; a client that never advertised the capability, or a server
with no stream, gets a sentinel error (`ErrSamplingUnsupported`) instead of a
hang. A timed-out sample leaves no pending entry.

### A served run can borrow the caller's model, but only by asking

`mcp-server --allow-run --sampling` makes served runs use a `model.Model`
implementation that delegates each generation to the client
(`adapters/mcpsampling`), so the server needs no local API key. The model is
bound to the connection per run, after checking that the client actually
advertised sampling.

The opt-in is deliberate in both directions. Without `--sampling`, nothing
changes: the server uses its own model and needs its own credentials. With the
flag, a client that cannot sample makes the run **fail with an actionable
error** rather than falling back to a local model the operator may not have
configured, or silently refusing the run: a run that quietly used a different
model than the operator asked for would make its answer untraceable to its
configuration. `--sampling` without `--allow-run` is a usage error rather than
an ignored flag.

### A sampled run reasons in text

`sampling/createMessage` has no tool field, so tool definitions cannot be
delegated: a sampled run drops them and the client's model never calls the
server's tools. A request that *requires* a tool call is refused loudly rather
than answered with text that pretends otherwise. Everything else about a served
run — approvals, events, checkpoints, the run timeout, detaching, cancelling —
is unchanged and still works, because only the model call changed hands.

Streaming is mapped honestly: the protocol is non-streaming, so the adapter
delivers the client's single answer as one delta followed by done, buffered and
closed before returning, with no producer goroutine to leak.

## Consequences

- Part C is complete. Every capability the plan's rows listed is either shipped
  or explicitly recorded as deliberately unported in an ADR.
- A served run can now be hosted without a model credential of its own, which
  is what makes "run this for me with your model" a usable arrangement between
  two agent processes.
- The tool-calling limitation is the price of the protocol's shape, and it is
  documented: sampling suits text-only work, and a run that needs the harness's
  tools must be served with the server's own model.
- `--sampling` deliberately lives only on `mcp-server`, and only beside
  `--allow-run`: it is about how that server serves runs, not a general model
  option, and a flag that silently did nothing elsewhere would be worse than
  its absence.

## Alternatives Rejected

### Fall back to the local model when the client cannot sample

The operator asked for the client's model. Quietly using another one makes the
run's configuration untrue, and its answer would be attributed to the wrong
model.

### Emulate tool calling by describing tools in the prompt

The client's model would have to emit tool calls as text, and the server would
parse them: that is a protocol of our invention inside a protocol that has a
field for it. Refusing what cannot be delegated is honest and cheap.

### Require sampling whenever a client supports it

Then a deployment that deliberately runs on its own model would silently switch
to the caller's. The flag is the operator's choice, and the capability only
makes it possible.

## Verification

`go test ./adapters/mcp/` — `TestSamplingRoundTripsTheClientsAnswer`,
`TestSamplingRefusedWhenTheClientDidNotAdvertiseIt`,
`TestSamplingRefusedWithNoStream`,
`TestSamplingTimesOutAndLeavesNoPendingState`,
`TestSamplingRejectsAnEmptyMessageList`,
`TestClientSupportsSamplingFollowsTheHandshake`. `go test ./adapters/mcpsampling/`
— `TestModelStreamDelegatesToTheClient`,
`TestModelGenerateSurfacesAnUnsupportedClientAsAnError`,
`TestModelStreamSurfacesAClientRefusalAsAnError`, `TestModelRefusesWhatItCannotDelegate`.
`go test ./cli/` — `TestServedRunUsesTheClientsModelWhenSamplingIsEnabled`,
`TestServedRunFailsWhenTheClientCannotSample`,
`TestServedRunWithoutSamplingKeepsTheLocalModel`,
`TestMCPServerSamplingRequiresTheRunGrant`. Plus `go test -race ./adapters/...
./cli/ -count=2`, `go test ./... -count=1`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.

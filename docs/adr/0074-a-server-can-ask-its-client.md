# ADR 0074: A Server Can Ask Its Client

Status: accepted

## Context

Every protocol exchange so far was client-initiated: the server answered tools,
resources, and prompts, and (since ADR 0073) sent one-way notifications. The
last unported piece of C20 was the reverse direction — a server-initiated
*request* to its client, which is what elicitation needs: a run that hits an
approval gate should be able to ask the person on the other end of the
connection, and a stdio server has no other way to reach them.

ADR 0073 recorded why this was the hard one: a notification can be written by
the goroutine already handling the request, but a request needs a *response*,
and that response arrives on the reader.

## Decision

### The reader keeps reading while a handler waits

`Serve` now reads frames on the serving goroutine and hands each non-response
frame to its own handler goroutine, while a frame that is a response (`method`
empty, id present) is routed straight to the waiter that asked for it. A handler
blocked inside `Server.Request` therefore cannot stall the reader, which is what
lets a ping be answered while an elicitation is outstanding. This is the one
place in the package where a handler runs concurrently with the loop, and it is
what buys the round trip; ADR 0073's inline notification write is still how
notifications work.

Responses are matched by id, and a response nobody is waiting for — an unknown
id, or one that arrives after a timeout — is dropped rather than answered or
treated as a protocol error. `Handle` behaves the same way, so a caller feeding
it a stray response gets silence, not a method-not-found.

### Server ids cannot collide with client ids

Server requests mint ids in their own namespace (`srv-<counter>`) and the
registry is keyed by the id exactly as it appears on the wire, so the echoed id
is compared byte-for-byte. Client ids are never parsed, rewritten, or guessed
at; the server's own responses still echo what the client sent.

### No request waits forever

`Request` requires the caller's context to carry a deadline; without one it
returns an error rather than risking a hang. On cancellation it removes its own
pending entry. When `Serve` returns it fails every pending request with
`ErrStreamClosed` first (so a handler blocked in `Request` cannot wait for an
answer that will never come), then waits for in-flight handlers — which is what
lets each of them emit its one complete line — and only then detaches the
stream. A pending request leaves no goroutine behind, and a shutdown cannot
hang. The original order (detach, then wait) silently dropped the response of
any handler still running when the reader reached EOF, because a detached
stream makes the write fail; the half-closed-client case is pinned by a test
now.

### Elicitation needs the client to have asked for it

`initialize` records whether the client advertised the `elicitation`
capability. `Elicit` sends `elicitation/create` with `{message, requestedSchema}`
and decodes `{action, content}` with action accept|decline|cancel, applying a
documented default timeout when the caller's context has none. If the server has
no stream, or the client never advertised the capability, it returns a clear
error instead of hanging, so a caller can fall back. The server advertises no
new capability of its own: elicitation is a *client* capability, and the
advertised server capability map is unchanged.

## Consequences

- C20 has only `resources/subscribe` and `resources/templates/list` left; the
  reverse channel that `sampling` would also need now exists.
- Responses are no longer guaranteed to arrive in request order when two calls
  are handled concurrently, because concurrent handlers are the point of the
  change. MCP pairs a response with its request by id; the round-trip tests in
  both packages now assert by id, and this is a wire-visible note for any
  consumer that assumed ordering. The first push of this change was red because
  the CLI's protocol test still read positionally while the package's own test
  had been relaxed: a wire change has to be propagated to every reader of the
  wire.
- The CLI's approval path is unchanged in this batch: a served run still
  refuses an approval-gated tool rather than eliciting, because wiring
  elicitation into approvals is a separate decision about what a run does when
  the client declines. It is the next batch, and until then the refusal text
  remains the honest answer.
- Client capability recording is deliberately narrow (the elicitation bit), so
  extending it later is an addition rather than a rewrite.

## Alternatives Rejected

### Answer the client's next message with the server's question

That is a protocol abuse: a response frame would carry a request, and a
conforming client would ignore or mis-handle it. A server request needs its own
id and its own response.

### Queue the question and return "pending" to the run

Then the run continues past a decision it did not get, which for an approval
gate means proceeding without one. A gate must block or refuse.

### A background writer goroutine and a channel per pending request

The reader still has to route the response, and a dedicated writer would not
remove that. The concurrency this change does add is one goroutine per
in-flight frame, bounded by the handlers the client actually sent, and `Serve`
waits for them.

### No deadline, relying on the client to answer

A client that never answers would pin a handler and (with `Serve` waiting)
prevent a clean shutdown. Requiring a caller deadline makes the bound the
caller's decision instead of a mystery hang.

## Verification

`go test ./adapters/mcp/` — `TestServerRequestRoundTripsWhileTheCallContinues`,
`TestServerKeepsAnsweringWhileARequestIsPending` (the deadlock the restructure
fixes), `TestServerIgnoresAResponseWithAnUnknownID`,
`TestServerRequestTimesOutAndLeavesNoPendingState`,
`TestServerRequestFailsWhenServeEnds`, `TestServerRequestRefusedWithNoStream`,
`TestServerRequestNeedsACallerDeadline`,
`TestServerConcurrentPanicDoesNotKillTheStream`,
`TestServerServeShutdownLeavesNothingRunning`,
`TestElicitationAcceptsDeclinesAndCancels`,
`TestElicitationRefusedWhenTheClientDidNotAdvertiseIt`,
`TestElicitationRefusedWithNoStream`,
`TestElicitationRejectsAnUnknownAction`. Plus `go test ./adapters/mcp/ -race
-count=2`, `go test ./... -count=1`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.

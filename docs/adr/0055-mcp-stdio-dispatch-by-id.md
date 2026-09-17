# ADR 0055: The Stdio Client Dispatches By Id, So A Deadline Is Real

Status: accepted

## Context

The client shipped in ADR 0053 read the stream inline. `call` wrote a request,
then looped over `readFrame` until a frame carried the id it was waiting for,
checking the context only *between* frames and holding the write lock for the
whole exchange. That is correct for a cooperative server and useless against
an uncooperative one: a server that accepts a request and never answers blocks
that read forever. Cancelling the context does not help, because the
cancellation is only noticed after a frame arrives, and abandoning the read in
a goroutine instead would leak one goroutine per timed-out call while leaving
the connection's framing to a reader nobody controls.

Nothing exposed this while the adapter was library-only. Wiring configured
servers into the CLI (ADR 0054) changes that: `zenforge exec` is run
unattended, and a wedged server would hang it with no operator to send
SIGINT.

## Decision

### One goroutine reads; calls wait on their own id

`JSONRPCClient` starts a reader that owns the stream and routes every inbound
frame to the channel registered for its id. A call writes its request, then
selects on its channel and the context. An expired call deletes its id and
returns: the reader is untouched, the connection stays usable, and a late
response is dropped because nothing is waiting for that id any more. The same
structure makes concurrent calls safe — each response is delivered by id
rather than by whichever caller holds a lock — and it is what lets a
per-call deadline be honest.

### A server-initiated frame is ignored, never mistaken for a response

A frame carrying a method is a server notification or request, not a response:
an id on such a frame must never resolve a call in flight. The client
advertises no capabilities (no sampling, elicitation, or roots), so a
conforming server has nothing to ask. An out-of-spec request is ignored rather
than refused, because the reader is the only reader of the stream: writing a
refusal from there would stop it reading, and a peer that is still writing its
own request would deadlock the connection against a full pipe. The cost — a
non-conforming server waits for an answer that never comes — is lower than a
deadlock risk in the tool-call path.

### The per-call budget travels as a tool declaration

`ServerOptions.ToolCallTimeout` becomes the tool's declared
`tool.TimeoutDeclarer` budget, so the existing timeout policy arms it and the
model never sees it (the reference does the same with a per-server
`toolCallTimeoutMs`, default 60 seconds). The expiry is now genuine: the call
returns, the policy replaces the outcome with the structured timeout result,
and the connection survives for the next call.

### Close releases waiters and waits for the reader

`close` hands every waiting call `ErrClientClosed` and `StdioClient.Close`
waits for the reader goroutine to observe the closed stream, in addition to
closing stdin, allowing a short graceful-exit window, killing a process that
has not exited, and reaping it. "When `Close` returns, nothing this client
started is still running" is then true of the goroutine too, not just the
process.

## Consequences

Benefits:

- a server that stops answering cannot hang an unattended run: the call ends
  at its declared budget and the server stays usable for the next one;
- concurrent calls are safe by construction, so a turn that invokes two remote
  tools does not serialize behind one response;
- `Close` leaves neither a process nor a goroutine behind, which is what makes
  the command-level drain in ADR 0054 meaningful;
- the id-based dispatch removed the "skip frames until the id matches" loop
  and the frame-type ambiguity with it.

Costs and limits:

- a request that times out is abandoned, not cancelled: the server may still
  be working on it, and may eventually do the work. A stdio transport has no
  cancellation channel, so the only alternatives were closing the connection
  (which loses the server for the rest of the run) or pretending the work
  stopped;
- the reader is started in `NewJSONRPCClient`, so a client constructed and
  never used holds one goroutine until its stream is closed — bounded and
  deliberate, and the tests close their pipes;
- an ignored server request is a silent no-op; a future capability (sampling,
  elicitation) needs a write path that does not run on the reader goroutine
  before it can be answered;
- `readFrame` blocking on a pipe still means a *process* that never writes is
  only bounded by the deadline, not by the reader — the deadline covers it,
  and `Close` still unblocks it.

## Alternatives Rejected

### Abandon The Read In A Goroutine And Leave It

The goroutine stays blocked on the pipe until the connection dies, so a run
with repeated timeouts accumulates one stranded goroutine per call, and the
connection's next reader would race the stranded one for frames.

### Close The Connection On Timeout

It unblocks the read with no leaked goroutine, but one slow call then costs
the whole server for the rest of the run, and a slow-but-working tool is a
normal thing to meet.

### Keep Reading Inline And Check The Context Between Frames

It is what the code did: a server that never answers blocks the call forever,
and every bounded operation built on top of it is bounded only in appearance.

### Answer Unadvertised Server Requests From The Reader

Spec-shaped, and it deadlocks against a peer that is still writing. The client
advertises no capabilities, so the frame should not exist; refusing to
deadlock over it is the better trade.

### A Per-Call Timeout In The CLI Instead Of A Declaration

The CLI would have to wrap the remote call itself, duplicating the timeout
policy for one tool family and giving the model a different failure shape than
every other tool. The declaration reuses the machinery that already exists.
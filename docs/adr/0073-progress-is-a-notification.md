# ADR 0073: Progress Is A Notification, Not A Second Protocol

Status: accepted

## Context

The parity row recorded two unported notification families: `listChanged` and
progress. A client that runs a long `zenforge_run` had no way to show that
anything was happening, and a server that changed its tool set had no way to say
so. The row also recorded why server-initiated *requests* stay unported: they
need a write path that does not run on the reader goroutine.

## Decision

### Progress is written inline, and that is why it is cheap

`mcp.ProgressFrom(ctx)` returns a reporter a handler calls as often as it likes;
it is a no-op when the request carried no `params._meta.progressToken`, so a
handler never checks whether the client wants progress. The token is kept as
`json.RawMessage`, so a string, number, bool, or object token is echoed
byte-for-byte as the client sent it, and progress frames are written by the
goroutine already handling the request, before that request's response.

That is the whole reason this was safe to add while server-initiated requests
remain unported: a notification is one-way. There is no response to read, so
nothing needs to be read off the reader goroutine, and no queue or extra
goroutine was introduced. A request from the server would need exactly that, and
it stays a recorded gap.

A non-finite progress value is dropped rather than clamped: JSON has no NaN or
Inf, clamping would invent a number the handler did not report, and the frame is
marshalled whole so no partial line can reach the stream.

### listChanged is opt-in, because a fixed set cannot change

`ServerConfig.DynamicLists` makes `initialize` advertise `listChanged: true` for
tools, resources, and prompts, and enables `NotifyToolsChanged`,
`NotifyResourcesChanged`, and `NotifyPromptsChanged`. Without it the capability
stays `false` and a Notify call fails with a clear error rather than sending a
notification the client was told not to expect.

The CLI server keeps `false`: its tool set is fixed by the flags it was started
with, so advertising the capability would be a lie. The mechanism exists and is
tested for a host whose sets do change; this one's do not.

### The CLI's run tool reports the run's own events

`zenforge_run` now reports progress from the run's event stream — the running
event count and the event type — through the same single event loop that
produces the result. The result, its `IsError` semantics, and the behaviour when
no token was supplied are unchanged. A detached run gets a no-op reporter
automatically, because its context is the server's rather than the request's:
writing progress for a request that has already been answered would be noise.

That required one addition outside the protocol layer: `Task.OnEvent`, an
observer called from `Agent.Run`'s existing event loop, so the served-run path
can see events without duplicating `Agent.Run`'s result and error derivation.

## Consequences

- The remaining C20 gaps are elicitation/sampling, `resources/subscribe`,
  `resources/templates/list`, and server-initiated requests.
- A client that supplies a token gets real progress for the work ZenForge does
  that takes longest; a client that does not gets exactly the frames it got
  before.
- `Notify*` requires a stream (`Serve` attaches one); a caller driving `Handle`
  directly uses `WithNotificationSink` for progress and cannot use Notify —
  documented rather than papered over with a background writer.

## Alternatives Rejected

### A writer goroutine and a frame queue

That is the machinery server-initiated requests need, and buying it for
notifications would add concurrency to the one place that currently has none.
Inline writes are correct precisely because a notification needs no reply.

### Clamp a non-finite progress value

The client would display a number no handler reported. Dropping it says the
report was invalid by producing nothing, and the response still arrives.

### Advertise listChanged unconditionally

The CLI server's sets cannot change, so the capability would promise a
notification that never comes. Opt-in costs one field and keeps the promise
meaningful.

## Verification

`go test ./adapters/mcp/` — `TestServerSendsProgressNotificationsBeforeTheResponse`
(string and numeric tokens), `TestServerSendsNoProgressWithoutAToken`,
`TestServerSendsProgressForResourcesAndPrompts`,
`TestServerWithDynamicListsAdvertisesAndSendsListChanges`,
`TestServerWithoutDynamicListsRefusesListChanges`,
`TestServerDropsNonFiniteProgress`. `go test ./cli/` —
`TestMCPRunToolReportsProgressFromRunEvents`,
`TestMCPRunToolSendsNoProgressWithoutAToken`. Plus `go test ./... -count=1`,
`go test -race ./adapters/mcp/ ./cli/`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.

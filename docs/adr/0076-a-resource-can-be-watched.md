# ADR 0076: A Resource Can Be Watched

Status: accepted

## Context

ADR 0071 exposed resources and listed a `{name}` URI template verbatim in
`resources/list`, recording `resources/templates/list` and
`resources/subscribe` as gaps. Both are small on their own and share one
question: what does a registration *mean* when a URI is a pattern and a client
wants to be told when it changes?

## Decision

### Templates get their own listing

`resources/templates/list` returns exactly the resources whose URI contains a
`{name}` segment, as `{uriTemplate, name, description, mimeType}`; a resource
without a template segment never appears there. The earlier decision to list a
template verbatim in `resources/list` stands — a client that only browses still
sees something — and the proper listing is now what a client that understands
templates asks for. Pagination is not implemented: the registration set is
fixed for the life of the process, so a `cursor` is accepted and the full list
returned without a `nextCursor`, rather than pretending to page.

### Subscription is opt-in, like listChanged

`ServerConfig.ResourceSubscriptions` is what makes `initialize` advertise
`resources.subscribe: true` and what lets `resources/subscribe` succeed.
Without it the capability stays `false` and the method is **method not found**:
the capability block is the server's list of methods, so a method it does not
advertise is one it does not serve — a clear error, not silence. Subscriptions
are per connection: they are cleared when `Serve` returns, so two sequential
servings of the same `*Server` do not inherit each other's watchers.

### A subscription matches templates, so a watched pattern is useful

Subscribing to a concrete URI that a registered template matches is allowed,
and `NotifyResourceUpdated(uri)` is delivered to a subscriber when the
subscription equals the argument or when the subscription is a registered
template the argument matches. So one watcher on `zenforge://runs/{runId}`
hears about every run, and one on a single run hears only about it. An
unregistered URI is refused by `NotifyResourceUpdated` rather than dropped:
announcing a URI the server never registered would tell a client to re-read
something it was never offered.

An update with no subscribers is a success that writes nothing — a resource
changing with nobody watching is normal — while a call after the stream is gone
returns `ErrNotServing`, the same error the other `Notify*` methods return.
`resources/unsubscribe` for a URI that is not subscribed is a normal result:
removing a subscription twice is not a failure, and erroring would force every
client to track server state it cannot see.

### The CLI server keeps subscribe false

The mechanism is available and tested, but the CLI server does not advertise
it: its resources are summaries read from the checkpoint store on each request,
and it has no update source to tell a subscriber about. Advertising a
subscription that would never fire is the kind of promise ADR 0073 and ADR 0071
already refused to make.

## Consequences

- C20 has one piece left: sampling.
- A host whose resources do change can watch them for the cost of one config
  field; a client of the CLI server sees `subscribe: false` and knows not to
  wait for an update.
- A delivery racing shutdown either completes its write or returns
  `ErrNotServing`, because `detach` holds the write lock; a delivery that
  begins before detach cannot be turned into a guaranteed error without
  changing that lock order, which is recorded rather than forced.
- `Handle`-only callers can subscribe with no connection; the state is cleared
  by the next `Serve`. That is a consequence of subscriptions belonging to a
  connection the bare entry point does not have.

## Alternatives Rejected

### Drop an unregistered URI instead of erroring

Then a caller's typo would look like a successful announcement that no client
ever received. The error surfaces a caller bug where it is made.

### Make unsubscribe of an unknown subscription an error

The intent — "I am no longer watching this" — is already satisfied. An error
would make clients track state the server owns, and the useful client
behaviour (unsubscribe defensively) would become wrong.

### Advertise `subscribe: true` always and accept the subscriptions

A subscription that can never fire is a promise the server cannot keep; the
CLI's resources are recomputed per read, so nothing would ever notify.

### Paginate the template listing

The set is fixed at startup and small; a cursor with a single page is noise.
The parameter is accepted for compatibility and the omission of pagination is
documented rather than faked with a `nextCursor` that never advances.

## Verification

`go test ./adapters/mcp/` —
`TestServerListsResourceTemplatesAndExcludesPlainResources`,
`TestServerNotifiesSubscribedResourceUpdates`,
`TestServerNotifiesConcreteURIMatchingASubscribedTemplate`,
`TestServerReportsSubscriptionFailuresDistinctly`,
`TestServerUnsubscribeIsIdempotent`,
`TestServerWithoutResourceSubscriptionsKeepsSubscribeFalse`,
`TestServerWithResourceSubscriptionsAdvertisesSubscribeWithoutChangingListChanged`,
`TestServerSubscriptionsDoNotSurviveServe`,
`TestServerNotifyResourceUpdatedRejectsUnknownAndBlankURIs`,
`TestServerNotifyResourceUpdatedRacingShutdownReturnsErrorsWithoutCorruptingTheStream`.
Plus `go test -race ./adapters/mcp/ -count=2`, `go test ./... -count=1`,
`go vet ./...`, `gofmt -l`, and `go test ./docs/...`.

# ADR 0083: The Streams Are Mounted With The Console

Status: accepted

## Context

ADR 0080 gave the console a boot path and ADR 0081 gave it unary RPC. A console
that boots and can list sessions still cannot *follow* one: sessions, live
events and approvals travel on a WebSocket mux (`WS /api/remote.mux`) with three
logical streams, and an approval is answered out of band on
`POST /api/$events/result`. Without them the shell renders and the sidebar stays
empty, which reads as a broken console rather than a host that has not answered.

This ADR also records the end of the interim console from ADR 0078: with the
rebranded upstream console mounted at `/`, the operator decided the first-party
one is no longer wanted, so it is no longer served.

## Decision

### Frames are concrete structs, because an extra key is a hang

The shipped client parses every mux frame with an exact key set and closes the
socket with 4002 on any extra or missing key. A malformed frame is therefore not
a degraded feature but a console that stops receiving anything. Every frame this
host writes is a struct with the exact upstream key set, and a test pins the key
set of each frame kind, so a later field addition fails a test instead of the
console.

### The same fence, before the upgrade

The stream handler runs the identical trust fence as the RPC handler — cross-site
refused, `Origin` authority must match `Host`, non-loopback refused unless
configured — and it runs **before** the upgrade, so a foreign page cannot hold a
socket open to this process. The same handler value is mounted at both exported
paths, with no `StripPrefix`, so the two halves cannot drift apart.

### An approval answer fails closed

`allowed-once` and `rejected` map onto the harness's approval inbox. Everything
else — an unknown client, an unknown or already-answered approval, an expired
one, an unsupported outcome, an outcome this host has no semantics for — is an
error that leaves the approval **pending**. A failed answer must never be read as
an allow.

### The host says what it does not have

Three things the console expects have no honest source here, and the host sends
only what is true rather than filling them in:

- no `$events` emit frames for session mutations, because this harness has no
  global change feed (the event bus is per-run), so the sidebar does not mutate
  live; `session/list` and `session/follow` still carry the truth;
- an empty `session/control` baseline, because there are no background jobs or
  projections to report, which upstream documents as legal;
- no assistant-stream frames: the harness streams model text as durable
  `model.delta` events, not the console's process-local revision/index protocol,
  so the opted-in baseline is sent (the client requires it) and live prose does
  not render through that path yet.

Raw harness event names are surfaced to the console unchanged, exactly as
`session/page` already does.

## Consequences

- The console gets a session sidebar, live event follow, and working approvals.
- Live sidebar mutation, background-job panels, and streamed assistant prose will
  look absent until a change feed, projections, and a delta mapping exist. Those
  are honest gaps, not regressions.
- `session/follow` ends on a terminal run. Upstream holds the stream open until
  the client closes the session, and the shipped client treats an `end` as
  retryable carrier loss (it retries once, then reports a terminal error). This
  is a known deviation to revisit when multi-turn sessions exist (a session that
  outlives one run should not end its stream when that run does).
- The interim first-party console is gone from the served surface. The Qwen
  credential is therefore host-side — `--base-url/--model/--api-key` or the
  environment — which is where upstream keeps it too: the console sends no
  credentials by design. `/api/settings` remains as the host's configuration
  seam, and it is what feeds the console's model catalog.

## Alternatives Rejected

### Invent `$events` emit frames from the per-run bus

There is no cross-run subscription to derive them from; synthesising "a session
changed" frames would tell the sidebar things the host did not observe.

### Fabricate assistant-stream deltas from durable `model.delta` events

The console's delta protocol is process-local (revision plus index) and the
harness has no durable counter for it. A fabricated revision would let the client
render prose it cannot reconcile after a reconnect. Sending the required baseline
and no deltas fails visibly instead.

### Keep the interim console at `/classic/`

The operator asked for the DSH console; two consoles in one host means two
notions of settings and two places to look when something is wrong. ADR 0078's
console served its purpose (a host without a plugin graph could still be used)
and is recorded there.

### Hold `session/follow` open after the terminal event

Upstream's lifetime is the session, not the run. Holding the socket open with
nothing to send would hide the real gap (this host has no session that outlives a
run) behind a stream that looks alive; ending it is the visible, honest shape
until multi-turn sessions exist.

## Verification

`go test ./internal/dshstream/` (the frame-key, fence, `$events`, `session/control`,
`session/follow` and fail-closed approval tests), `go test ./internal/dshmount/`
(the mounted shell, advertised bundles, module-provider scan), and
`go test ./cli/` (the mount: `/` is the injected shell, the stream paths answer,
the catalog reports the configured model, an unknown endpoint is logged);
plus `go test -race ./internal/dshstream/`, `go test ./... -count=1`,
`go vet ./...`, `gofmt -l`, `go test ./docs/...`.

# ADR 0070: A Signed Webhook Starts A Run

Status: accepted

## Context

The C19 row's last trigger was an external one: something outside ZenForge
should be able to say "run this task" — a CI job that just built an artifact, a
monitoring system that just opened an incident — without holding an agent
credential or a shell on the host. The harness HTTP server already had run
start, status, cancel, and list endpoints; what was missing was an entry point
whose caller is not trusted by identity but by a shared secret it can prove it
knows.

## Decision

### `POST /webhook/run`, registered only with a secret

The endpoint is added to the harness HTTP server's mux only when a secret is
configured. A server with no secret answers 404 for the path: an
unauthenticated run trigger must not exist by accident, and a 404 is a clearer
answer than a 401 that suggests the credential was merely wrong.

The secret comes from the server's own configuration (`-webhook-secret` or the
`ZENFORGE_WEBHOOK_SECRET` environment variable, so it stays out of `argv`), and
it is never logged or echoed.

### The signature covers the timestamp as well as the body

`X-ZenForge-Signature` is `sha256=<hex>` (a bare hex digest is also accepted)
and is HMAC-SHA256 over `timestamp + "." + raw body`; the timestamp arrives in
`X-ZenForge-Timestamp` as unix seconds and must be within -5 minutes/+1 minute
of now. Comparison is `hmac.Equal`, so a wrong signature cannot be found by
timing.

Signing the timestamp is the point: if only the body were signed, a captured
request would stay valid forever, because the timestamp is what the freshness
window checks. Covering it means an attacker who captures a request cannot
move it forward in time without the secret.

### It answers with an acknowledgement, not an answer

A valid call starts a run through the existing run manager and replies `202`
with the run id. The manager's own status stays authoritative for what happens
next; the caller polls the run endpoints, and the durable store is where the
result lives. A webhook that waited for the answer would be an HTTP request
held open for as long as a run takes, which is exactly the coupling the
detached served run (ADR 0067) removed elsewhere.

## Consequences

- C19 has only per-user command directories left. An external system can
  trigger a run without a ZenForge credential, and the trigger is inert unless
  an operator configured it.
- One secret authorises every webhook run a server accepts, and the runs use
  the server's own workspace, tools, and approval mode. There is no per-trigger
  scoping, and that is recorded as a limitation rather than half-built.
- The endpoint reuses the harness's existing run start path and its access
  controller hook, so a deployment that already restricts the other run
  endpoints restricts this one the same way.
- Body and prompt validation (malformed JSON, empty prompt, oversized body) are
  400s, and a non-POST is 405: the failure modes a caller can fix are told
  apart from the ones it cannot.

## Alternatives Rejected

### Authenticate with the server's run token

Then every caller needs the credential that can also inspect and cancel every
run. A webhook is handed to systems with a narrower job, and a separate secret
that can only start work is the smaller grant.

### Sign only the body, and keep the window in a nonce store

A nonce store means durable state and a cleanup policy for a problem the
timestamp already solves: a signature over `timestamp.body` cannot be replayed
outside the window at all.

### Register the endpoint always and reject when no secret is set

The path would exist and answer 401, which reads as "configure a signature and
it will work". It will not: without a secret there is nothing to verify
against, so the honest answer is that the endpoint is not there.

### Return the run's answer

That is the blocking served run again, on a channel whose caller is a machine
that may time out at 30 seconds. An acknowledgement plus polling is the shape
that survives long work.

## Verification

`go test ./server/harnesshttp/` — `TestWebhookRunStartsSignedRun`,
`TestWebhookRunRouteRegisteredWithSecret`,
`TestWebhookRunRejectsWrongSignatureAndStartsNoRun`,
`TestWebhookRunRejectsMissingSignature`,
`TestWebhookRunRejectsMissingOrUnparsableTimestamp`,
`TestWebhookRunRejectsStaleTimestampEvenWithValidSignature`,
`TestWebhookRunRejectsFutureTimestampEvenWithValidSignature`,
`TestWebhookRunRejectsSignatureWithoutTimestampPrefix` (a body-only signature
is refused, which is what makes the prefix load-bearing),
`TestWebhookRunAcceptsBareHexSignature`,
`TestWebhookRunRejectsMalformedJSONAndEmptyPrompt`,
`TestWebhookRunRouteAbsentWithoutSecret`, `TestWebhookRunRejectsNonPostMethod`.
The clock is injected, so the suite is deterministic and sleeps nowhere. Plus
`go test ./server/... -count=1`, `go test -race ./server/harnesshttp/`,
`go test ./... -count=1`, `go vet ./...`, `gofmt -l`, and `go test ./docs/...`.
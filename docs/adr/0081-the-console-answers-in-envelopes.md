# ADR 0081: The Console Answers In Envelopes

Status: accepted

## Context

ADR 0080 landed the console's boot path. The console's next need is the unary
RPC surface it calls after boot: `POST /api/<namespace>/<method>`, and the
`session` namespace it uses to list, open, prompt, and cancel work. This
repository's model is a run (a detached execution with a durable event log),
while the console's model is a session. The mapping between them, and the exact
shape of a reply, decide whether the console works or appears broken.

## Decision

### A session is a run, and the mapping refuses rather than guesses

`session/create` allocates a session id with this repository's own run id
generator and holds it as a pending session; `session/prompt` starts a run with
that pre-assigned id; `session/list`, `session/cancel`, `session/rename`, and
`session/page` read and act on the run manager and the durable event log. Where
the two models genuinely differ — a prompt to a run that has already finished, a
run owned by another process that cannot be steered, a per-run working
directory, an agent preset, a subagent address — the answer is a method error
with code `unimplemented` that names what is missing. A console panel that shows
an error is a better failure than one that shows a lie, and the error is what
tells the next person which mapping to build.

### A method error is HTTP 200; a protocol error is not

The shipped console client throws on any non-2xx **before** it parses the
result. So a method-level failure (an unknown session, unsupported content) is a
200 with `{"ok":false,"error":{...}}`, while a malformed envelope — bad JSON,
duplicated keys, a missing `args`, a method that disagrees with the path — is a
4xx with a plain error body, because that request never became a method call.
Unknown namespaces and methods are a 404, which the client turns into a
per-feature transport failure: an unimplemented feature degrades instead of
breaking the console. `rpcId` is spliced back as the exact bytes received, and
every error object carries `details` even when empty, because the client's
parser type-asserts it.

### The fence runs before routing

The cross-site check, the `Origin`-versus-`Host` check, and the loopback check
run before the method, route, and body are examined, so a refused caller learns
nothing about which endpoints exist. `Config.AllowRemote` is the seam
`zenforge serve` will use for its own `--allow-remote`, so the address policy
lives in one place rather than two.

### Revisions and titles come from the durable log

`session/rename` appends the repository's existing session-title event and
returns the committed sequence position, and `session/list` reads the title back
from the log. A title is a durable fact here (ADR 0037's event family), not a
field on a live structure, so the log is where a rename belongs — including for
a run that has already finished.

## Consequences

- The console can boot, list sessions, start work, cancel it, page history, and
  show titles. What it cannot do is send a **second** message to a session after
  the first run finished: a run is one execution, and appending a turn to a
  finished run needs either a session-to-current-run map or an agent that
  accepts a new turn. This is recorded as a limitation rather than papered over.
- Panels that need namespaces this host does not serve (model catalog, forks,
  workspace files, subagents) show a feature-level error, not a boot failure.
- `session/list` currently reads each run's log to find its latest title; a
  projection or a tail read is the follow-up when logs grow.

## Alternatives Rejected

### Answer a method error with a 4xx

Faithful to HTTP's instincts and fatal here: the client never parses the error
envelope on a non-2xx, so every "unknown session" would surface as a transport
failure with no message. The status codes follow the client's control flow, not
the other way round.

### Invent a session object beside the run

Then the console's sessions and the harness's runs would be two records of the
same work, and a cancel or a title would have to be kept in step by hand.
Mapping onto the run keeps one source of truth.

### Report a plausible value for what cannot be implemented

A fabricated working directory or a fake accepted prompt would make the console
look healthy while the work it claims to have queued does not exist. The
`unimplemented` error is cheaper to debug and honest to read.

### Serve every namespace with an empty success

Then a panel would render as if it had data, and an operator would trust a
screen that means nothing. A 404 makes the client degrade the feature it cannot
have.

### Enforce the address policy in the listener as well

Two places deciding who may talk to the console would drift; the listener binds
an address, the handler decides trust, and only the latter knows about
`Origin`.

## Verification

`go test ./internal/dshapi/` — 45 tests covering the envelope
(`TestEnvelopeSuccessEchoesRPCID`,
`TestEnvelopeEchoesUnusualRPCIDByteForByte`, `TestEnvelopeUnknownMethodIs404`,
`TestEnvelopeMethodPathMismatchIs400`, `TestEnvelopeRejectsMalformedRequests`,
`TestEnvelopeRejectsNonPost`,
`TestEnvelopeMalformedRequestStillEchoesUsableRPCID`,
`TestMethodFailureIsWellFormedEnvelope`,
`TestMethodFailureContainsNoSuccessValue`), the fence
(`TestFenceRejectsCrossSite`, `TestFenceRejectsOriginMismatch`,
`TestFenceRejectsNullOrigin`, `TestFenceRejectsNonLoopbackRemoteByDefault`,
`TestFenceAllowsRemoteWhenConfigured`, `TestFenceAllowsSameOriginLoopback`,
`TestFenceAllowsIPv6Loopback`, `TestFenceFencesBeforeRouting`), and the session
namespace (`TestSessionCreateAllocatesPendingSession`,
`TestSessionCreateAdoptsExplicitSessionID`,
`TestSessionCreateRejectsUnsupportedFields`,
`TestSessionCreateRejectsInvalidSessionID`,
`TestSessionCreateAdoptsExistingDurableSession`,
`TestSessionListIsNewestFirst`, `TestSessionListReportsRunningRun`,
`TestSessionListAcceptsOpaqueCursor`, `TestSessionPromptStartsPendingRun`,
`TestSessionPromptUnknownSessionIsMethodError`,
`TestSessionPromptCrossProcessRunIsUnimplemented`,
`TestSessionPromptRejectsBadArguments`,
`TestSessionPromptRefusesUnsupportedContent`,
`TestSessionPromptQueuesIntoActiveRun`,
`TestSessionPromptOnFinishedRunIsUnimplemented`,
`TestSessionCancelActiveRun`, `TestSessionCancelUnknownSession`,
`TestSessionCancelFinishedRunIsHonest`,
`TestSessionCancelAlreadyCancelledIsAccepted`,
`TestSessionRenameCommitsDurableTitle`,
`TestSessionRenamePendingSessionIsUnimplemented`,
`TestSessionRenameRejectsEmptyTitle`, `TestSessionRenameUnknownSession`,
`TestSessionPageReturnsRecordsAndHasMore`,
`TestSessionPageReadsDurableStore`, `TestSessionPageUnknownSession`,
`TestSessionPageSubagentAddressIsUnimplemented`,
`TestSessionPageRejectsBadArguments`). Plus
`go test -race ./internal/dshapi/ -count=1`, `go test ./... -count=1`,
`go vet ./...`, `gofmt -l`, and `go test ./docs/...`.

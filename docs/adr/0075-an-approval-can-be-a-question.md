# ADR 0075: An Approval Can Be A Question

Status: accepted

## Context

A served run that hit an approval gate had one answer: refuse. ADR 0056's
reason was that a stdio server has no operator to prompt and no way to reach
one — and the parity row recorded the refusal text as the honest limit. ADR
0074 built the missing channel: a server can now send its client a request and
wait for the answer, which is exactly what elicitation is. The remaining
decision was not mechanical but behavioural: what a run does with the answer.

## Decision

### Ask when the client can answer, refuse when it cannot

With `--approve prompt` (the default), an approval gate now sends
`elicitation/create` naming the tool and the harness's reason, asking for a
boolean decision, when the client advertised the elicitation capability. The
approval is granted for that one call only.

When the client cannot answer — it never advertised the capability, there is no
stream, the question times out, the stream ends, the RPC fails, or the action is
unknown — the run falls back to **exactly** the previous refusal, byte for byte.
A client that cannot answer must be in precisely the position it was in before,
so nothing about the old contract changes for it.

`--approve always` and `--approve never` install no elicitation at all: they are
the operator pre-answering the question, and asking anyway would be noise.

### A refusal now says who refused

A decline, a cancel, an `approve: false`, or a non-boolean answer denies the
call and the run is told that **the client declined**, not that the server could
not ask. The old sentence ("this server cannot ask a human") would be false at
that point, and a run that believes its operator was never consulted will
retry; a run told the operator declined will stop. The refusal summary keeps its
old wording only when every denial actually used the fallback.

### A non-boolean answer is never consent

An `accept` whose content lacks a boolean `approve` is a denial. An approval
gate that reads a typo, a null, or a string as consent is worse than one that
refuses: the failure mode of the second is a wasted call, and of the first is an
unapproved tool call. This is the one place where the code must not be
permissive.

### The server travels with the run, not with the request

`mcp.ServerFrom(ctx)` is captured **before** the detach branch, and the recorder
keeps it for the run, so a detached run can still elicit — its context belongs
to the server, and the question is bounded by the default elicitation timeout
(and never by the request that has already been answered). Every question has a
deadline; the timeout is injectable so the fallback is testable without waiting
five minutes.

## Consequences

- The C20 gap recorded since ADR 0056 is closed on the client side: an
  MCP client that supports elicitation can approve a served run's tool calls,
  and one that does not sees the same refusal it always saw.
- `--allow-run` did not change. The grant lets a caller start work; answering
  the resulting question is the client's own capability, not a second grant.
- The refusal summary now has two shapes (fallback and answered-denial), which
  is a user-visible change for a run whose client answered and a
  no-op for every other run.
- The detached path has no dedicated end-to-end test: proving a detached run
  finished needs polling a status tool against a clock. It shares the blocking
  path's code, the server is captured before detaching, and the reviewer note
  records the gap rather than pretending it is covered.
- `sampling`, `resources/subscribe`, and `resources/templates/list` are the
  last unported pieces of C20.

## Alternatives Rejected

### Treat a non-boolean answer as "no answer" and fall back

Then a client with a bug in its form would see the server-cannot-ask refusal
while the server *did* ask, and the operator's intent (they answered something)
would vanish. Denying with the reason that the answer was not a boolean is the
truthful report.

### Ask on every gate even for `--approve always`

The operator has already answered for this server; forwarding each gate would
make every tool call a question and train the operator to click through. The
flag is the answer.

### Grant a standing approval when the client says yes

One question, one call. A standing grant from a yes would let a later call
inherit consent nobody gave for it, and the harness already has an explicit
grant mechanism (ADR 0062) for that intent.

## Verification

`go test ./cli/` — `TestMCPRunElicitsApprovalFromTheClient`,
`TestMCPRunDeniesWhenTheClientDeclines`,
`TestMCPRunTreatsANonBooleanApprovalAsADenial`,
`TestMCPRunRefusesWithoutElicitationUsingTheOldText`,
`TestMCPRunFallsBackWhenTheElicitationStreamEnds`,
`TestMCPRunFallsBackWhenTheClientCannotAnswer`,
`TestMCPRunFallsBackWhenTheElicitationTimesOut`,
`TestMCPRunNeverModeDoesNotElicit`, plus the pre-existing
`TestServedRunRefusesToolCallsThatNeedAnOperatorAndSaysSo` unchanged.
`go test ./adapters/mcp/` — `TestClientSupportsElicitationFollowsTheHandshake`,
`TestServerFromReachesTheServerFromAHandlerContext`. Plus
`go test ./cli/ -run MCP -count=5`, `go test -race ./adapters/mcp/ ./cli/
-count=2`, `go test ./... -count=1`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.

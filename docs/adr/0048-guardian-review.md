# ADR 0048: A Guardian Review At The Stop Boundary

Status: accepted

## Context

An agent that grades its own homework finishes confident and wrong. The
reference adds a review/guardian pass: a second model looks at the finished
work adversarially and can send it back. The value is obvious and the risk is
equally obvious — a reviewer that can silently block every run is as
dangerous as one that never speaks.

Three questions had to be answered: what does the reviewer see, what can it
do, and what happens when it fails.

## Decision

### The reviewer sees the run's own evidence

The review input is the task, the final answer, the files the run changed,
the unified diffs of those changes, the commands it ran, and the failures it
observed. The diffs come from the run's existing `turn.diff` events, read
back through the event store; nothing new is tracked for the reviewer's
benefit, and the agent pays no bookkeeping cost. Everything is bounded, so a
huge diff cannot crowd out the task.

### A verdict is JSON, and a malformed verdict is an error

`review.ParseVerdict` requires a decision
(`approve`/`request_changes`/`comment`), accepts fenced JSON, drops findings
with no issue text, defaults a missing severity to medium, and **errors** on
an unknown decision or severity. A reviewer that failed to answer must never
be read as "looks good" — the same failure-is-never-an-opinion rule the hooks
engine follows. `request_changes` with nothing to change is rejected, because
"keep working" without a reason sends the agent in circles.

### Report by default, enforce on request

`review.ModeReport` records the verdict as a `review.completed` event and
lets the run finish. `review.ModeEnforce` turns a `request_changes` verdict
into a refusal at the stop boundary, with the findings rendered as the
agent's next instruction. A reviewer that can block is opted into via
`--review enforce`, never assumed.

### The reviewer shares the stop hook's refusal budget

An enforcement is a refusal, so it goes through the same bounded mechanism as
a Stop hook: at most three refusals, then the run finishes with the answer it
has. That bound already existed to stop a hook from holding a run hostage,
and it is exactly the right bound for a reviewer too. A run that ends after
the bound reports every refusal, so the user can see the disagreement.

### A failed review never blocks, and never approves

If the reviewer errors, times out, or returns unparsable output, the run
finishes and the `review.completed` event carries `review failed: ...`. The
guardian refuses to convert a broken reviewer into either a block or an
approval, because both would be readings the reviewer never gave.

### The reviewer runs after the hooks

The reviewer is the last gate before the run finishes. A Stop hook that
refuses already means another turn, so paying for a review whose outcome
cannot matter would be waste; the review happens only when the hooks allow
the stop.

## Consequences

Benefits:

- an independent pass can catch what the run's own optimism missed, with the
  diff as evidence rather than a summary of itself;
- the same model adapter serves the reviewer, so no new provider
  configuration exists and a cheaper model can be wired in later;
- reporting is a real capability on its own: `--review report` gives users
  findings without changing run behaviour;
- enforcement is bounded by an existing, tested refusal mechanism, so a
  reviewer cannot loop a run;
- a broken reviewer is inert: it can neither block nor approve.

Costs and limits:

- one extra model call per finished run in report mode, plus another turn in
  enforce mode when changes are requested;
- reports and enforcements share the three-refusal budget, so a reviewer and
  a Stop hook cannot each refuse three times;
- the review runs at run end, so it cannot intervene mid-run;
- there is no severity-based escalation, no per-finding acknowledgement, and
  no memory of repeated findings across runs (the reference's guardian
  process handles adjudication and post-review messages);
- findings are not attached to a code-review UI or a PR; they are an
  instruction and an event.

## Alternatives Rejected

### Block On Every Finding

Low-severity remarks would turn into extra turns for stylistic noise. Only
`request_changes` refuses, and the agent decides what to do with the rest.

### Review Before Every Turn

The reviewer sees a finished diff, not a work in progress; running it mid-run
would cost a model call per step and produce findings about code that is
about to change.

### Treat Reviewer Failure As Approval

Silently approving because the reviewer broke is the worst option: the user
believes a review happened. Reporting the failure is the honest reading.

### A Separate Review Agent With Its Own Tools

A tool-using reviewer could inspect more, but it also needs its own sandbox,
budget, and containment, and its findings would be as unverifiable as the
diff it was handed. One bounded call over the run's own evidence is the
smaller, testable step.

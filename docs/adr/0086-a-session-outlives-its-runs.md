# ADR 0086: A Session Outlives Its Runs

Status: accepted

Amended by ADR 0108: a session's turns are served as one session-wide sequence, so an
earlier turn's transcript is reachable.

## Context

The console treats a session as a conversation: it creates one, prompts it, and
prompts it again when the operator has more to say. This host mapped a session to
exactly one run, so the second prompt answered `unimplemented` and the
conversation ended with its first answer.

The pieces that decide how to fix it were established by research, not
assumption: `RunManager.Resume(ctx, runID)` takes **no new input** (it continues
an interrupted run), while `zenforge.Task` already carries
`InitialMessages []model.Message`, and the durable log records the user's turn as
`run.started.input` and the assistant's reply as `run.done.output`.

## Decision

### A run id chain names the turns, and nothing is stored to remember it

A session's first turn is the run whose id is the session id; the k-th turn is
`<sessionId>~<k>` for k >= 2 (`internal/dshsession`). Existence is decided by
probing the run manager and the durable log, so a restart -- or a second process
owning the next turn -- loses no part of the conversation, and no in-memory table
has to be threaded through the console's packages.

The separator is `~`, not `#`: a run id can reach a URL path, where `#` starts a
fragment. `Base` only recognises a suffix `fmt.Sprintf` could have written
(`~2`, never `~1`, `~0`, `~02`, `~-3` or an out-of-range number), and a
continuation is only grouped with its session when the base run is present, so an
adopted id that merely looks like a continuation keeps its own identity.

### A prompt to a finished session starts the next turn

`session/prompt` steers an active turn (unchanged), and otherwise starts a new run
from the chain carrying `InitialMessages` rebuilt from the durable logs. The
rebuild replays the conversation at run granularity: the user's turn and the
assistant's reply. The intermediate tool traffic a run performed is **not**
replayed, because it records one execution rather than something to hand a new
one, and a turn whose log is missing contributes nothing rather than a fabricated
message. A prompt naming a continuation run id resolves back to its session
instead of starting a chain under a chain. An unknown session is still
`session/not-found`: continuing a conversation is not the same as inventing one.

### The list shows conversations, not turns

`session/list` groups a chain under its session and reports the newest turn's
state and title. Only a continuation whose base is present is grouped.

### Follow and page agree on which run the session is

`session/follow` resolves a session to its newest turn before attaching, or a
stream opened after a second prompt would attach to the first, finished run and
never deliver the answer being waited for. `session/page` resolves the same way,
so history and the live stream describe the same run.

## Known gap

**Closed by ADR 0108**: a session's turns are now served as one session-wide sequence, so
`session/page` and `session/follow` reach every turn. The decision below is kept as the
record of what was true when it was written.

A session's turns are **not** merged into one paged log. The wire cursor is a
sequence number and each run's log numbers its own events from one, so a merged
page needs a synthetic coordinate plus rewritten per-event ids (which the console
reads as the session's identity). Until that exists, `session/page` serves the
newest turn and an earlier turn's transcript is not reachable through it. The
model's context for a continuation is complete; the console's rendered transcript
shows the current turn. This is recorded here, in `docs/limitations.md` and in
the console handover, rather than left to be discovered.

## Alternatives Rejected

### An in-memory session -> runs table

It is the obvious first design and it is wrong twice: a restart forgets every
conversation's later turns, and both `internal/dshapi` and `internal/dshstream`
would need the table, which means a new shared object threaded through the
console's construction for state that a naming convention derives for free.

### Continue by reusing the first run's id

The run manager would refuse it (the id already has a log), and forcing it
through would overwrite the history the continuation is supposed to carry.

### Use `RunManager.Resume`

Resume takes no input, so it cannot express "the operator said something new". It
remains the right call for recovering an interrupted run, which is a different
thing.

### Let the client own the chain

The console has no notion of a second turn's run id, and asking it to invent one
would put conversation continuity in the browser, where it is lost on refresh.

## Verification

`go test ./internal/dshsession/` (the naming round trip, the suffixes that must
not parse, the next turn past a gap, and the base-must-exist rule);
`go test ./internal/dshapi/` (a finished session continues under the chain's run
id with the prior exchange as `InitialMessages`, a continuation run id resolving
to its session, an unknown session still not-found, the list grouping turns under
one session, a foreign id kept apart, the page serving the newest turn, and every
pre-existing session, catalog, credential and llm test);
`go test ./internal/dshstream/` (follow resolving a session to its newest turn,
including a durable-only turn, and leaving a foreign id alone).

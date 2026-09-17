# ADR 0037: Goals And Ralph As Two Different Loops

Status: accepted

## Context

Two capabilities in the references both answer "keep working until the
job is done", and they are easy to confuse:

- DSH's **goal** domain is a *same-session* objective. A human or the
  model creates one goal per session, the session's own conversation is
  the context, and the domain tracks a durable lifecycle (`active`,
  `paused`, `blocked`, `complete`), a revision per change, and a round
  budget. A driver re-invokes the session once per round.
- DSH's **Ralph** tool is a *fresh-agent* loop. Every round opens a new
  child with no parent conversation and no prior child session, so the
  shared workspace is the only long-term memory; exactly one bounded
  structured report crosses from round to round.

ZenForge needed both, and the interesting design question is what belongs
in a shared lifecycle package versus in each loop.

## Decision

### The goal lifecycle is a pure, revision-checked state machine

`goals/` owns the definition of a goal: a snapshot (`id`, `revision`,
`objective`, `maxGoalRounds`, `phase`, optional `blockedReason{code,
message}`) plus session counters (`roundsStarted`, `createdAt`,
`updatedAt`, `seenGoalIds`). Every operation is a pure function from the
previous `State` to the next, so the rules are testable without a store,
an agent, or a clock:

- `create` requires a session with no goal or a **complete** one, produces
  revision one with zero rounds, and a goal id may never be reused
  (`seenGoalIds`);
- `edit`, `pause`, `resume`, `complete`, and `block` each advance the
  revision by exactly one and preserve the counters and timestamps, so a
  change is never ambiguous about what it replaced;
- `resume` is refused once the round budget is exhausted;
- `AdmitRound` requires exactly `roundsStarted+1` and a round within the
  budget, which is what makes the budget real rather than advisory;
- failures are typed (`goals.Error` with a stable code), so a host can
  distinguish "you asked for the wrong revision" from "the budget is
  gone".

### The blocked-round minimum lives in the lifecycle, not the driver

The model-facing contract is that a goal may only be reported blocked
after the same blocking condition has persisted for three consecutive
rounds. Implementing that in the driver would leave the same hole through
the tool: a model could call `update_goal blocked` three times inside one
round. So `Block` counts the streak in durable state (`blockedStreak`,
`blockedRound`, `lastBlocker`) and advances it **at most once per round**:
three calls in one round record one round of evidence, and the transition
is rejected with `blocked-rounds-remaining` until the count is satisfied.
A rejected attempt still persists the observation — the count is progress,
not a failure to record.

`RunGoal` therefore calls `Block` on every blocked report and treats the
rejection as "keep going", so the rule has exactly one implementation.

### The two loops share the report, not the driver

`Report` (`status`, `summary`, `evidence`, `nextSteps`, `blocker`) is the
same bounded handoff the reference defines, validated exactly: normalized
strings, `continue` needs `nextSteps` and an empty blocker, `complete`
needs evidence and no next steps, `blocked` needs a concrete blocker, and
the serialized size is capped (`maxHandoffChars`, 16384 by default).
`RunRalph` uses it as the only carried-over context between fresh rounds.
`RunGoal` collects the same reports for a human, but its context is the
session itself.

### The CLI splits the loops along the same line

`zenforge goal "<objective>"` creates a persisted goal and drives rounds
by resuming the same run: round one starts it, later rounds continue the
conversation, and the model owns the lifecycle through the goal tools, so
the host stops the moment the stored goal is complete or blocked and never
overrides a decision the model already made.

`zenforge ralph "<objective>"` runs a fresh run per round against the
shared workspace. Each round ends when the model emits its structured
report, which the CLI extracts (last balanced JSON object, braces inside
strings respected), validates, injects into the next round's prompt, and
persists under `<checkpointDir>/ralph/<loop>/round-N.json`.

### Persistence is per session, and atomic

`goals.Store` is keyed by session id, with a memory implementation for
tests and a file implementation that writes a temporary file and renames
it, refusing session ids that would escape the root. The CLI stores goals
under `<checkpointDir>/goals`.

## Consequences

Benefits:

- the lifecycle rules are the reference's rules verbatim and are tested
  directly against the transition table, including the rejected-then-
  accepted blocked sequence;
- blocked-gating cannot be bypassed through the tool, because the tool and
  the driver both call the same `Block`;
- the round budget is enforced by admission rather than by counting at the
  end, so a run cannot exceed it by crashing;
- goal ids are never reused, so a completed goal's evidence cannot be
  silently inherited by a new objective in the same session;
- the CLI's goal loop keeps the model authoritative (the tools update the
  same store the driver reads), which is what makes `--goals` useful
  without the `goal` command.

Costs and limits:

- one goal per session at a time: a second `create_goal` is refused until
  the first is complete, matching the reference;
- the blocked-streak heuristic compares the blocker *message*, so a model
  that rewords an identical condition restarts the count. That is the
  conservative direction;
- `zenforge goal` requires a checkpoint store, since resuming round two
  needs round one's checkpoint; the run store is the same one the CLI
  already configures;
- Ralph rounds share a workspace, so concurrent loops over the same
  workspace can interfere; the tool description says so, and the reference
  has the same property;
- a round whose model never emits a report fails the loop with a clear
  error rather than inventing a report, which keeps the handoff honest at
  the cost of a wasted round;
- the CLI does not yet expose goal tools inside `ralph` rounds (a fresh
  round has no session to own a goal), which matches the reference's
  separation between the two mechanisms.

## Alternatives Rejected

### One Loop Parameterized By "Fresh Or Same Session"

The two loops differ in more than context seeding: a goal has a durable
lifecycle, a revision, and a round budget owned by a store, while a Ralph
round has no state at all beyond the report. Merging them would force
every Ralph round to carry goal bookkeeping it does not use, and would
make the goal lifecycle harder to reason about. They share `Report`,
`DriverOptions`, and the validation rules — the parts that are genuinely
common.

### Keep Blocked Counting In The Driver

Tested first, and it does not hold: the tool path bypasses the driver, and
three tool calls in one round satisfy any driver-local counter. Durable
state plus a per-round guard is the only version that cannot be gamed.

### Store Goals Inside The Run Checkpoint

The goal outlives a single run's checkpoint and, in the `goal` command,
drives several runs (one initial run plus resumes). Hanging it off the run
would make the session key ambiguous and complicate time travel. A
separate per-session store keeps the two lifecycles independent, and the
CLI can point both at the same checkpoint directory.

### Let The Model Mark Blocked Immediately

Removing the minimum would make "blocked" the cheapest way out of a hard
round, and the reference explicitly guards against it. Three consecutive
rounds is a low bar for a genuine blocker and a real one for a model that
is merely stuck.
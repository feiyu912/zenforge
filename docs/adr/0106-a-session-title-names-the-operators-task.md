# ADR 0106: A Session's Title Names The Operator's Task, Not The Preset's Instruction

Status: accepted

Relates to ADR 0028 (the title's derivation and its log-only rule) and [ADR 0105](0105-the-durable-log-is-projected-into-the-consoles-vocabulary.md),
which first made that title visible in the console's sidebar.

## Context

The console lists a session by the title in its log. This host derives a fallback title
from the first words of the run's input (ADR 0028), and the plan-execute preset feeds its
plan stage a *copy* of the input with its own instruction appended:

```go
planInput := task.Input + "\n\n" + planner.PlanPrompt
```

The plan stage then runs as its own harness loop, and that loop derives and publishes the
title again from the state it was given — the augmented text. So a session started with
`h` recorded two titles, and the second one won:

```
seq 2  session.title  {"title": "h", "source": "fallback"}
seq 3  session.title  {"title": "h Create a concise todo plan for the", "source": "fallback"}
```

The second sentence is `planner.PlanPrompt` — "Create a concise todo plan for the user's
request. You must call todo_write with the todo list before giving any final answer." —
and it is the harness's own instruction, not anything the operator said. The sidebar of
the console the operator was reading showed it verbatim, which is how this was noticed:
the first conversation they opened in the console was listed as
"h Create a concise todo plan for".

The run's own records were always correct — `run.started`, and therefore the projected
`user/message` in the console's transcript, carries the operator's `h` — and so was the
first title event. Only the *derivation input* of the later stage, and the state metadata
frozen from it (`zenforge.session_title`), carried the instruction.

## Decision

A session's title is derived from the operator's own task text, on every stage of a run.
`sessionTitleInput` reads the run state's recorded task (`planning.input`, the field the
preset writes when it augments its stage input) and falls back to the state's input only
when the run has no recorded task. Both derivation sites use it: the metadata freeze in
`applyRunContext` and the `session.title` publication that follows `run.started`.

The preset's augmentation remains what the *model* sees for the plan stage; only the
title's input changes.

## Consequences

- A planning session's sidebar entry reads what the operator typed. Verified against the
  live host's log: the sequence above was served by the built binary before the fix, and
  the test reproduces both titles' derivation exactly.
- Both stages derive the same title, so the two `session.title` records a planning run
  emits now agree instead of the second contradicting the first. The duplicate remains:
  a run may legitimately publish a title per stage, and the console reads the latest
  (ADR 0105 serves every durable record). Collapsing it into one record is not worth a
  second log rule for a field whose readers take the last value.
- A resumed plan stage keeps its recorded task, so a resumed run's title is stable.
- The explicit `--title` / `agent.sessionTitle` override is untouched: it is derived
  before any preset augmentation and never consulted the stage input.

## Alternatives Rejected

### Do not publish a title from the plan stage at all

The plan stage's title publication exists because the stage is a harness loop that
publishes one per `run.started` it sees; suppressing it whenever the metadata is already
set would also suppress the *first* title in a react-mode run, where the metadata is
frozen by the same loop before the event is published, and the console would lose titles.

### Strip the appended instruction from the title text

The title would then be a string that never appeared anywhere in the run, and the
truncation-to-words rule (`sessiontitle.Fallback`) would decide how much of the
instruction to remove. Naming the operator's task is a matter of *which* input is read,
not of cleaning up a text.

### Keep the instruction out of the plan stage's input

The plan stage needs the instruction: it is what makes the model plan with `todo_write`
before acting. The model surface and the title's input are separate readers of the same
run, which is exactly ADR 0099's layer rule.

## Verification

`go test -run TestAgentPlanExecuteTitleNamesTheOperatorsTask .` — a plan-execute run
started with `h` records `h` as its title on every `session.title` event. The test fails
against the pre-fix derivation with the exact title the operator saw
(`h Create a concise todo plan for the`).
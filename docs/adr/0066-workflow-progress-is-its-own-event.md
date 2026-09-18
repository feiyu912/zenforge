# ADR 0066: Workflow Progress Is Its Own Event Family

Status: accepted

## Context

A workflow's progress had no event types of its own. `phase()` and `log()` and
the script's view of its children were emitted as `subtask.event` payloads with
`type` set to `workflow.phase`, `workflow.log`, `workflow.agent.start` or
`workflow.agent.end`, wrapped one level down in a `payload` object. The parity
plan recorded this as the last C9 gap: a dedicated workflow event type.

The carrier was chosen for one shape rather than for a reason — the sub-agent
runtime already had `subtask.*` events, and reusing them meant one shape for a
reader. But the two things are not the same thing. A `subtask.event` says "a
child run streamed this"; a `phase()` says "the script narrated this". A reader
that wanted a script's narration had to subscribe to every child's stream and
filter payload types to find it, and a reader that wanted child events had to
ignore a payload convention it did not know about.

## Decision

Four first-class event types:

| type | fields (besides the shared identity) |
| --- | --- |
| `workflow.phase` | `phase` |
| `workflow.log` | `message` |
| `workflow.agent.started` | `seq`, `label`, `phase` |
| `workflow.agent.done` | `seq`, `label`, `phase`, `outcome` |

Every one of them carries `workflow`, `parentRunId`, and `toolCallId`, and its
own fields **flat** — the event type already says what the event is, so there is
no `type`/`payload` pair to unwrap.

The child's run lifecycle and its streamed events stay on `subtask.*`. That is
not a leftover: those events carry the child's `childRunId`, its status, its
error, and its own stream payloads, and they are produced by the sub-agent
runtime for the task tool too. A workflow child *is* a sub-agent run, so
`subtask.started`/`subtask.event`/`subtask.done` are the correct names for what
happened inside it.

Two views of one child therefore both exist, and both are kept:

- `workflow.agent.started`/`.done` is the script's bookkeeping: the call's
  sequence in the script, its `label` and `phase`, and its outcome
  (`completed`, `failed`, `cancelled`). It is reported by the engine, so it
  includes a child that never started — a fatal `AGENT_START` still ends with a
  `.done` for the call the script made.
- `subtask.*` is the run: identity, status, error, and the child's own stream.

## Consequences

- The last recorded C9 gap is closed: a reader subscribes to
  `workflow.*` for a script's narration and `subtask.*` for child runs, with no
  payload convention to learn and no filtering.
- This is a wire change. A consumer that filtered `subtask.event` for a
  payload `type` beginning `workflow.` must now listen for the four types, and
  one that read `payload.phase` must read `phase`. The change is recorded in
  `docs/limitations.md`; the events are additive otherwise, and nothing else in
  this repository consumed the old shape (the events CLI, the JSONL stream, and
  the HTTP/MCP surfaces render whatever type they are handed).
- `workflow.agent.started`/`.done` still overlap with `subtask.started`/`.done`
  for a child that did start. That overlap is deliberate and documented: one is
  the script's view, the other the run's, and only the script's covers the call
  whose start failed.
- Event volume is unchanged in aggregate: a phase or log event is still one
  event, and a child still produces its subtask events plus the engine's two
  bookkeeping events, exactly as before. What changed is which type carries
  them.

## Alternatives Rejected

### One `workflow.event` type with a `kind` field

That is the old shape with a different carrier: every reader would still
filter, and a subscriber to one kind would receive all of them. Four types cost
four constants and let a filter be a subscription.

### Move child lifecycle to `workflow.agent.*` as well

Then the child's streamed events, its status, its error, and its run id would
have to be re-plumbed into the engine's observer, or the child's run identity
would be lost. `subtask.*` is a faithful description of a child run and is
shared with the task tool; duplicating it under a second name would leave two
event types meaning the same thing.

### Keep the nested `payload` object for compatibility

The nested level exists only because `subtask.event` needed one generic field
to describe the child's own event type. A dedicated event type makes it
redundant, and keeping it would have preserved the confusing part of the old
shape while changing the part readers actually filtered on.

### Emit both shapes during a deprecation window

Two events per phase and per log, plus a documented end date, for a surface
whose only in-repository consumers are generic. The honest choice is one shape
and a recorded change.

## Verification

`go test .` — `TestAgentRunsWorkflowTool` collects the stream and asserts the
four types arrive, each carrying `workflow`/`parentRunId`/`toolCallId`, that
`phase`/`message`/`seq`/`outcome` are flat fields of the right events, that both
children are reported by two `workflow.agent.started` and two
`workflow.agent.done` events, and that a child's own `child.note` still arrives
on `subtask.event` with no `workflow.` payload type left on that carrier. Fails
when the observer is put back on the subtask carrier: no `workflow.phase` event
appears at all. Plus `go test ./... -count=1`, `go test -race .`, `go vet ./...`,
`gofmt -l`, and `go test ./docs/...`.
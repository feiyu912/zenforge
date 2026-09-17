# ADR 0047: Durable Memories As Readable Files

Status: accepted

## Context

Every run starts from zero. The agent re-discovers that this repository runs
its tests with `make check`, that a particular command needs a flag, that the
user prefers a certain style, and forgets all of it when the process exits.
The reference solves this with a two-phase memory pipeline: phase 1 extracts
a structured memory from each finished rollout (bounded batches, leased jobs
in a state database, retry backoff, secret redaction), and phase 2
consolidates those records into a memory folder with a dedicated
consolidation sub-agent, git-baselined so the workspace diff drives what the
agent sees.

That design is a large amount of machinery, and most of it exists to make
memory *cheap at scale*: claim rules, leases, watermarks, retention windows.
This project has one agent, one workspace, and a user who has to be able to
read and delete what the agent has learned about them.

## Decision

### Memory is a plain file the user can read

`memory.FileStore` keeps two markdown files: `raw_memories.md` (append-only
entries in a small block format) and `memory_summary.md` (the consolidated
form that is actually injected). Both are greppable and hand-editable. The
summary is not a cache of the raw file — it is generated *from* it, so
deleting a line in the raw file and re-running removes the memory.

### Identity is the content, not the run

`memory.ID(scope, project, text)` hashes the normalized text. Discovering
the same fact twice produces one memory; the scope and project are part of
the identity because the same sentence can be true for one repository and
false for another.

### Injection is frozen, like every other prompt input

The summary is read once at run start and stored in durable
`RunState.Meta` under `zenforge.memory_context`, rendered as a system
section (`prompt.OrderMemoryContext`) next to the hook context. A resume
replays the memories the run began with. Memories written after a run
started do not silently change the instructions of a run already in flight.

### Distillation is one optional model call

`memory.ModelDistiller` sends the run's task, result, commands, failures,
files, and event trail (all bounded) with a prompt adapted from the
reference's stage-one extraction: record only what will still be true in a
later session, cite it, and prefer saying nothing over guessing. Distillation
is opt-in (`--memory-distill`) because it costs a model call per run, and a
distiller error never fails the run — it is reported as a `memory.recorded`
event with the error attached, and the user still gets their answer.

### The read path fails the run, the write path does not

A summary that cannot be read fails the run *before the model is called*: a
run whose instructions are unreadable must not half-run on different
instructions. A distillation or store-write failure at run end is reported
and ignored, because memory is an optimization.

### Deterministic, budgeted consolidation instead of a second agent

Consolidation renders the newest entries within a byte budget and an entry
cap. It is deterministic for a given raw file, which means a run's injected
instructions can be reproduced, and it cannot grow without bound: an
unbounded memory file would silently consume the model's context.

## Consequences

Benefits:

- learnings survive the session, and the user can read, edit, or delete
  them without a database tool;
- injection is reproducible and frozen, so a resume and the original run see
  the same instructions;
- a wrong or hostile memory is visible in the prompt and removable;
- the store, the distiller, and the agent are separately testable: the
  store and consolidation are pure, the distiller takes an injected model,
  and the agent takes a `Provider` interface;
- memory failures never cost the user their answer, and an unreadable store
  never produces a run with silently different instructions.

Costs and limits:

- no rollout batches, leases, retries, or usage telemetry: distillation runs
  inline at run end under a timeout, so a slow distiller delays the run's
  exit by up to that timeout;
- one memory file per store, not per-thread records; there is no
  `last_used` ranking, so consolidation is recency-ordered, not
  usefulness-ordered;
- citations are recorded but not verified or parsed back out of the model's
  later output;
- secrets are not separately scrubbed: distillation sends the task, result,
  commands, failures, and files to the model, which is the same data the run
  already sent;
- extension/resource pruning and git-baselined workspace diffs from the
  reference are not ported.

## Alternatives Rejected

### Port The Two-Phase Pipeline Verbatim

A state database, claim rules, leases, and a consolidation sub-agent exist
for concurrent multi-thread extraction at scale. Porting them would add a
large amount of machinery whose failure modes (stale leases, watermark
regressions) outweigh the benefit for one workspace, and it would make the
memories invisible to the user.

### Inject The Raw File

The raw file grows without bound; injecting it would consume the context in
proportion to how long the project has existed. The summary is the injected
artifact precisely so it has a hard budget.

### A Model Call For Consolidation Too

A second model call per run to rewrite the summary adds cost and
non-determinism for a job that a stable recency-ordered render does
adequately at this scale.

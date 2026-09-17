# ADR 0028: Deliverables, Session Titles, And The Completed Write Observation Policy

Status: accepted

## Context

The DSH reference exposes three session-level behaviors ZenForge was
missing or only partly implementing (parity plan items C18 and the
observation half of the workspace-tool row).

First, DSH's `present` tool is the explicit hand-off point for
deliverables: a model that produced or updated a file the user asked to
receive must *declare* it, and the session log records the validated
file references (turn, call id, paths, descriptions). DSH caps one call
at eight files, rejects empty paths, missing files, and non-regular
files with recoverable errors, and appends a `deliverables/presented`
event only after a successful call.

Second, DSH derives a session title from the first eligible human
message: escape sequences (OSC/CSI/ESC), C0/C1 controls, and
bidirectional marks are stripped, whitespace collapses, the result is
capped by words and bytes on rune boundaries, and the title is log-only
— it never enters the model surface or derived history. An explicit
rename whose text normalizes to empty is rejected as invalid input.

Third, DSH's filesystem observation policy distinguishes three states:
*unseen*, *absent*, and *present-version*. ZenForge implemented only
present-version CAS: `workspace_read` records metadata and
`workspace_write` checks it before overwriting. But `Stat` never
returned its documented not-found sentinel — `Workspace.resolve` passed
the raw `lstat` error through — so the new-file branch in
`workspace_write` was dead code and every "create a new file" attempt
under `RequireReadBeforeWrite` failed with a raw filesystem error. The
CLI test that asserted a blind write is blocked passed only because of
that bug.

## Decision

### `present` records validated deliverables

`tools/present` exposes `present` with DSH's argument shape
(`files: [{path, description?}]`), a default eight-file cap, and
validation through the workspace adapter: empty paths and excess files
are invalid arguments, a missing file returns a recoverable error that
wraps `workspace.ErrPathNotFound` and says to create the file and retry,
and a directory is "not a regular file". Validation resolves each path,
so the recorded reference is the canonical workspace-relative path.

The tool returns the files plus DSH's `Presented <path>` lines, and the
agent appends a durable `deliverables.presented` event — tool-call id
and files — only for successful calls. Deliverables are references, not
copies: nothing is snapshotted, moved, or preserved.

### Titles are sanitized, deterministic, and log-only

The new `sessiontitle` package ports DSH's sanitizers exactly: OSC, CSI,
and ESC sequences, C0/C1 controls, and the bidi/directional set are
removed (the OSC pattern drops DSH's negative lookahead, which Go
regexp lacks, and terminates on BEL/ST instead), whitespace collapses,
and truncation keeps whole runes. `Fallback` keeps the first eight words
within 64 bytes; `Normalize` accepts an explicit title up to 200 bytes.

`applyRunContext` derives the title once per fresh run (explicit
`SessionTitle` wins and must survive normalization, otherwise the run
fails with `session title is empty after normalization`) and freezes it
into run-state meta so resume replays it. The `session.title` event is
published immediately *after* `run.started`, keeping `run.started` the
log's first record — a broken event store must not fail with a title
error before the run has started. Titles never enter the model prompt.

### Writes must observe the path first

`SnapshotStore` gains absence observations (`RecordAbsentForRun`,
`AbsentObservedForRun`) alongside its present-version snapshots, and
`workspace_read` records an absence when a read reports not-found.
`workspace_write` under `RequireReadBeforeWrite` then splits cleanly:
an existing file needs a fresh same-run snapshot, a missing file needs a
same-run observed absence, and anything else is surfaced as-is. A blind
create fails with `ErrSnapshotRequired` wrapped in a message that tells
the model to read the path first.

`Workspace.resolve` now maps a not-found `EvalSymlinks` failure to
`workspace.ErrPathNotFound` when existence was required, so the sentinel
is real; and `ErrPathNotFound` wraps `fs.ErrNotExist`, so hosts can
classify a missing path with `errors.Is(err, fs.ErrNotExist)` as well as
with the workspace sentinel.

## Consequences

Benefits:

- deliverables are explicit, validated events host UIs can render,
  instead of paths the model merely mentions in prose;
- every run carries a stable, human-readable title derived from its
  input, sanitized against terminal escape injection, with an explicit
  override for hosts that know better, and with zero prompt surface;
- the observation policy is now symmetrical — the model must look at a
  path before either overwriting or creating it — closing the blind
  create/clobber hole the raw-error bug had been masking;
- `errors.Is(err, fs.ErrNotExist)` now works for every workspace
  consumer, including host code that never learned the ZenForge
  sentinel.

Costs:

- creating a file costs one extra failed `workspace_read` round trip;
  hosts that want to pre-authorize creation can still record absence
  directly through the store;
- an absence observation is keyed by normalized path and run id, so a
  create after the window reset or under a different path spelling
  requires a fresh observation (fail-closed by design);
- the title event shifts durable event sequences by one record, which is
  a log-format change for downstream consumers that compare exact type
  sequences rather than filtering.

## Alternatives Rejected

### Recording Deliverables Inside The Tool

The tool cannot emit agent events, and delivery must be suppressed for
failed calls; the agent owns both, so it inspects the successful
structured result, exactly as the todo and workspace-change paths do.

### Making Titles Part Of The System Prompt

DSH deliberately keeps titles log-only so a title cannot perturb model
behavior or be replayed into derived history. Injecting it would also
break the frozen-prompt resume guarantee of ADR 0024.

### Failing The Run When The Title Event Cannot Be Persisted

The title is metadata. Persisting it before `run.started` made a broken
event store report a title error instead of the real first failure, so
the event is published after `run.started` and any later failure is
reported in its own right.

### Allowing Blind Creates (Keeping The Old Dead Branch)

The old code explicitly permitted writing a file that `Stat` reported
missing. That is the hole the CLI test was written to catch; the raw
error merely hid it. Requiring an observed absence keeps the documented
"existing files need a snapshot" rule while making the new-file path
work deliberately rather than by accident.

### Treating A Failed Read As Authorization For Any Run

Absence observations are run-scoped like present-version snapshots
(ADR 0026 semantics): a read in one run never authorizes a write in
another.
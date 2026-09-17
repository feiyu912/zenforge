# ADR 0033: The `apply_patch` Envelope Tool

Status: accepted

## Context

Codex edits files through a single text tool, `apply_patch`, whose
envelope can add, update, move, and delete files in one call:

```
*** Begin Patch
*** Add File: path/new.txt
+line
*** Update File: path/existing.txt
@@ context
-old
+new
*** Update File: path/old.txt
*** Move to: path/new.txt
*** Delete File: path/gone.txt
*** End Patch
```

ZenForge had only `workspace_write` (whole-file writes) and
`workspace_edit` (exact-string replacement). Both are useful, but neither
expresses a multi-file change, and neither can rename or delete. A model
working on a refactor therefore needed many round trips, each of which
re-validated and re-approved separately.

The reference implementation's value is not only the format but its
matching behavior: `seek_sequence` locates context lines through four
progressively looser passes, and `*** End of File` anchors a chunk at the
end of the file, so patches authored by a model that mis-transcribed
whitespace or typographic punctuation still apply.

## Decision

### Port the parser and applier semantics, not a new dialect

`applypatch.Parse` accepts the reference grammar with the reference's
leniency: markers tolerate surrounding whitespace, a `<<'EOF' … EOF`
heredoc wrapper is unwrapped (the reference does this because gpt-4.1
sometimes wraps the tool argument), a chunk may omit its leading `@@`
header, and diagnostics carry the reference's line-numbered shape
(`invalid hunk at line N, …` / `invalid patch: …`).

`applypatch.Apply` uses the reference's line matching:
exact, trailing-whitespace-insensitive, surrounding-whitespace-
insensitive, then Unicode-punctuation-normalized (typographic dashes,
quotes, and spaces map to ASCII). The search may start at the end of the
file for an `*** End of File` chunk. Line endings normalize to LF and the
result always ends with a newline, matching the reference default mode.

Two reference behaviors are preserved even though they surprise:
`*** Add File` **overwrites** an existing file rather than failing, and a
matched region is rewritten from the patch text, so a loose match also
normalizes the whitespace or punctuation it ignored. Both are documented
in the tool description and covered by tests.

### The tool composes existing policy, observation, and diff machinery

`tools/patch` wraps the engine and reuses ZenForge's existing contracts
instead of inventing parallel ones:

- **File policy.** Every affected path (update sources, move
  destinations, adds, deletes) is planned through
  `policy.PlanFileAccess`. A path outside the write roots is denied with
  `policy.ErrFileAccessDenied` and a structured payload; a path that
  merely needs approval raises **one** approval request for the whole
  patch, whose fingerprint covers the patch text, so an approval cannot
  be replayed against a different patch.
- **Observation policy.** Under `RequireReadBeforeWrite`, an update needs
  a same-run read of the current version (`SnapshotStore.CheckForRun`)
  and a creation needs an observed absence
  (`AbsentObservedForRun`), exactly like `workspace_write`. A delete
  needs neither: the applier already fails when the path is missing.
- **Turn diffs.** Original and updated content are captured per path, so
  the change appears in the turn diff like any other mutation.
- **Deletion.** `workspace.Deleter` is a new optional interface
  (`Delete(ctx, path)`), implemented by the local workspace with the same
  root-confinement and regular-file checks as `Write`. Workspaces that do
  not implement it fail with a clear message instead of silently
  ignoring a `*** Delete File` hunk.

### `apply_patch` is always registered

Unlike `tool_search` (which is pointless without deferred tools), the
patch tool is useful in every write-enabled session, so the CLI registers
it alongside the workspace tools. Hosts that do not want it simply do not
add it to `Config.Tools`.

## Consequences

Benefits:

- a multi-file refactor is one tool call, one approval, and one turn
  diff, instead of a sequence of per-file round trips;
- renames and deletions become expressible at all;
- loose matching makes model-authored patches robust to whitespace and
  typographic drift, which is the reference's most practically valuable
  behavior;
- the engine is a pure function over an injected `FS`, so it is tested
  without a filesystem and can be reused by any host.

Costs:

- a whole-patch approval is coarser than per-file approval; a host that
  wants per-path granularity must deny with roots instead (the denial
  path is per-path and reports the offending path);
- applying hunks is sequential, not transactional: a failure part-way
  leaves earlier hunks applied, matching the reference tool, so the
  documented recovery is to re-read the files and re-apply;
- the LF-normalizing mode can rewrite a file's line endings; the
  reference's `PreserveLineEndings` mode is not ported, and CRLF files
  therefore normalize to LF;
- deletion requires the optional `Deleter` interface, so a third-party
  workspace that only implements `Workspace` cannot delete.

## Alternatives Rejected

### A JSON Array Of Per-File Operations

Structured JSON is friendlier to schema validation but loses the
reference's line-based diff semantics, and the model has to escape
content. The envelope is what the models are trained on, and the whole
point is that the model writes diffs naturally.

### Staged Writes For All-Or-Nothing Atomicity

The parity sketch suggested staged writes. The reference tool does not do
this — it writes each hunk in order and returns an error on failure — and
staging would either double disk usage or require a journal. Matching the
reference keeps behavior predictable, and the failure message names the
path that failed.

### Reimplementing `workspace_edit` On Top Of `apply_patch`

The exact-string edit contract (single match, `replaceAll`, CAS guard) is
a different and useful tool: a model that knows the literal text should
not have to construct a diff. Both stay.

### Deferring The Patch Tool Like A Remote Catalog

`tool_search`'s deferral exists for large optional catalogs. The patch
tool is small, always relevant to a write-enabled session, and hiding it
would force a search round trip for the most common mutation.
# 0134. The `@` picker is served from the directory the host serves

- Status: Accepted
- Date: 2026-09-22
- Related: 0089 (the console's file face and its one workspace), 0099 (the adapter
  tier), 0131 (the skill catalog, the seam pattern this follows), 0133 (the chain
  before it)

## Context

`fileReferences/list` is the console's `@` menu. Its wire is not like the other
namespaces this adapter serves (`@deepseek-ai/dsh-api-session-controller`,
`packages/api/session-controller/src/file-references.ts:33`):

- scope is `{context: "agent", wire: "agentId"}` -- the same scope the goals
  namespace is served under, so the session arrives as `agentId`;
- the parameters are `agentId` and `query` (the path text following `@`), with no
  `request` object around them;
- the result is a **bare array** of `{path, kind: "file" | "directory"}` -- no
  `{ok, value}` union, no items wrapper;
- the console's own call site is
  `ctx.remote.fileReferences.list(session.sessionId, query, signal).then((result) =>
  result.ok ? result.value : [])` (`ui-reference/client.js:164`), and that
  `Promise.all` has no catch, which is why this row being unserved broke the whole
  `@` menu rather than only its file section.

The reference host resolves candidates from a per-workspace provider,
`@deepseek-ai/dsh-file-reference-local`. Its behavior is fully specified, and this
host can reproduce it over the one directory it serves: a query with no slash
searches a tree, a query with a slash (or an empty one) lists one directory, both
are ranked and bounded.

## Decision

**The candidates come from the directory `zenforge serve` was started with, and the
ranking is the reference provider's.** The seam is `dshapi.FileReferenceSource`
(interface + `SetFileReferences`), installed by the serve command through
`dshmount.Config.FileReferences`; the walk itself lives in `cli/filereferences.go`,
because only the CLI knows which directory this host serves -- the same split ADR
0131 used for the skill catalog.

Served semantics, each taken from the reference provider:

- **A query with a slash lists its directory.** The text before the last slash names
  the directory and the text after it is the fragment, ranked against that
  directory's immediate entries. The empty query is the root's own listing.
- **A query with no slash searches the tree** and ranks matches by the same five
  bands: exact name (1000), name prefix (900), name substring (700), path substring
  (500), subsequence (300 + the reference's gap score), with a 25-point bonus for a
  directory and ties broken by kind, then shorter path, then lexicographic order.
  The answer is at most 20 rows.
- **Hidden entries are opt-in.** A dot-leading entry is skipped unless the fragment
  (directory listing) or the query (tree search) itself names a dot, which is how a
  person reaches `.github` by typing `.`.
- **The reference provider's skip list is skipped**: `.git`, `node_modules`, `dist`,
  `build`, `out`, `coverage`, `target`, the common framework caches, and so on.
- **Symlinks are never listed and never traversed.** A directory path is resolved
  one segment at a time with `lstat`, and any component that is a symlink or not a
  directory answers no candidates, as does any path that escapes the root -- the
  reference's `resolveDisplayDirectory` refuses both, and a picker that offered a
  path outside the workspace would promise a reference the host cannot read.
- **Both bounds are the reference's own defaults**: at most 50000 entries enter the
  walk and at most 20 rows leave it. This host adds a depth cap (64) so a
  pathological tree cannot make a picker request linger.
- **A read failure answers no candidates** (the reference's `readDirectory` does the
  same), while an *unopenable workspace* leaves the seam nil and the namespace
  answers `unimplemented` naming `FileReferenceSource`.
- **The scope is validated**: an `agentId` this host does not serve is refused with
  `session/not-found` and `{sessionId}`; a missing or non-string `query` and an
  unknown argument are `gateway/arguments-invalid`; a source failure is
  `gateway/internal` with the reason, because "no files" is not an honest answer to
  "the tree could not be read".

## Deviations, recorded

1. **Every call walks; there is no index.** The reference keeps a per-workspace
   index, rebuilds it in the background after an invalidation, and answers a bare
   query from the stale copy immediately. This host has no such cache: each
   `fileReferences/list` walks the tree under the entry budget. The answer is the
   same ranking over the same entries and is never stale; what is missing is the
   reference's "stale but instant" answer while a rebuild runs, and the
   invalidation-on-tool-result hook has nothing to invalidate.
2. **One root for every session.** The reference composes one provider per workspace
   root, so a session in workspace B sees B's tree. This host runs every session in
   the directory it serves (ADR 0089/0094), so every session's candidates come from
   that directory -- the same limit the workspace file browser already documents.
   The `agentId` is still validated rather than ignored.
3. **The unknown-scope refusal is this host's sentence.** Upstream resolves the
   agent in the remote binder, which names no business code for a miss; this host
   uses the `session/not-found` sentence and details every other session-scoped
   namespace uses, rather than inventing a code.

## Consequences

- `fileReferences/list` is served, taking the ledger to 56 served, 4 streams, 17
  refused, 32 unserved of 109. The `@` menu works end to end: its file section now
  answers, so the `Promise.all` that used to reject whole no longer does.
- Live evidence, from a `zenforge serve` over a crafted workspace (a nested
  `internal/dshapi/`, a hidden `.github/workflows/`, a `node_modules/` tree and a
  symlinked `linkdocs`), against a session this host created:
  - the empty query listed the root's own entries, directories first
    (`docs`, `internal`, `README.md`, `main.go`) with `node_modules` and `linkdocs`
    absent;
  - `internal/dsh` answered the one directory `internal/dshapi`;
    `internal/dshapi/` answered its two files; `internal/dshapi/feedback` ranked
    `feedback.go` first;
  - `main` searched the tree (`main.go` before `docs/main-notes.md`), `dshapi` put
    the matching directory above the files under it, and the fuzzy `fg` still found
    `internal/dshapi/feedback.go`;
  - `.github/` returned the hidden directory named on purpose, and a bare `.` saw
    hidden entries that a plain query hides;
  - `../`, `linkdocs/` and `nope/nothing` all answered `[]`;
  - an unknown scope answered `session/not-found: session "run-nobody" not found`,
    a missing query answered `gateway/arguments-invalid: argument "query" is
    required`, and an extra `path` argument was refused by name.
- `docs/limitations.md` records the two limits an operator can meet: every call
  walks the tree rather than reading an index, and every session sees the one
  directory the host serves.
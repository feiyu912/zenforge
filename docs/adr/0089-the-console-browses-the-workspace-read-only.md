# ADR 0089: The Console Browses the Workspace Read-Only

Status: accepted

## Context

The console's file sidebar and document preview read files through the
`workspaceFiles` namespace: the sidebar calls `list`
(`client/ui-sidebar-files/src/client/face.ts:55`) and the preview calls `read`
and `readAll` (`client/ui-sidebar-documentpreview/src/client/rpc.ts:81`,
`src/client/index.ts:99`). No shipped panel calls `readRelated` or `changes`.

The page semantics are upstream's and are specific: `offset` and `limit` count
**lines**, `offset` is 1-based, a page over the byte cap is **refused rather than
shortened** ("a silently cut page reads as the whole page",
`api/workspace-files/src/index.ts:106-112`), a page that stops early reports
`eof: false`, and a file is binary when it contains a NUL byte
(`src/index.ts:241-243`). Directory entries are capped at 2000 with the cut
reported (`src/index.ts:349-350`).

## Decision

### The read-only half is served; the rest is refused by name

`list`, `stat`, `read`, `readAll` and `readBytes` are implemented. `changes`
(a streaming watch) and `readRelated` answer `unimplemented` with the capability
named. Neither is called by a shipped panel, so refusing them costs a console
nothing and keeps this host from claiming a watch it does not run.

### Containment is the workspace's own, and it is stricter than upstream's

Every path resolves through the host's workspace (`workspace/local`, whose
`resolve` refuses an escape), so the sidebar can never list or read past the
directory the server was started with, and a request for `../secrets` answers
`workspace-file/outside-workspace`. Upstream allows paths outside the workspace
for the byte window; this host does not, and the code says which rule applied.

### Upstream's codes, classified where the information exists

The six `workspace-file/*` codes are answered with their details
(`types.d.ts:36-58`): `not-found`, `outside-workspace`, `too-large` (with the
limit), `not-text`, `not-regular-file` and `not-directory` (with the kind). The
adapter classifies, because only it can tell a missing path from one outside the
root; an error it cannot classify becomes `gateway/internal` whose message names
no path, since a raw filesystem error can carry a host path the caller never
asked about.

### The limits are upstream's numbers

2 MiB per page, 32 MiB for a complete read, 5000 lines as the default and largest
page, 2000 directory entries (`src/index.ts:186-189`). Matching them means a
console written against upstream gets the answers it was written for. A page over
the byte cap is refused with the cap named; the directory cap drops the rest and
sets `truncated: true`, which is the one short answer that is not a lie.

### A file request is scoped to a session this host serves

`workspaceFileScopeId` must name a session the host knows -- a live or durable
run, any turn of one, or a session the manager still lists -- and anything else
answers `session/not-found`. This host serves one workspace root per process, so
the scope does not select a different tree; refusing an unknown scope keeps the
host from answering with a file tree the caller never asked for.

## Consequences

- The file sidebar and the document preview work, with the page and binary rules
  the console was built against.
- Reading a page goes through the workspace's whole-file read, so a file above the
  32 MiB cap answers `too-large` even for its first line. Upstream pages through a
  file of any size; this host reads it into memory, and the limit is named in the
  error rather than hidden behind a truncated page.
- The namespace is read-only: nothing in the console can write to the workspace
  through it.

## Alternatives Rejected

### Reimplement path containment in this package

The workspace already resolves, refuses escapes, denies blocked device paths and
caps reads, and its rules are the ones the agent's own tools use. A second
implementation would be a second set of rules to keep in step.

### Allow paths outside the workspace because upstream does

This host's workspace is the boundary an operator gave the server, and a console
that can read `/etc` because a byte-window call allows it would be a surprise, not
a feature.

### Shorten an oversized page instead of refusing it

The console cannot tell a shortened page from a complete one, so it would render
a partial file as the file.

### Hide a file above the read cap behind an empty page

An empty page reads as an empty file. The error names the cap.

## Verification

`go test ./internal/dshapi/ -run TestWorkspaceFile` -- the listing with its
requested path, the entry cap setting `truncated`, the line-range rules (offset
below one, limit above the page cap, zero limit, a range that is not an object,
all with the host proven untouched) and an empty range taking the defaults, the
refusal codes with their details, an unclassified error staying internal without
naming a path, an unknown scope, every surface answering `unimplemented` with
`WorkspaceFiles` named when no face is installed, and the two named capability
gaps. `go test ./cli/ -run 'TestConsoleWorkspaceFiles|TestCutTextPage'` -- the
real tree: listing types and sizes, a directory's absent size, a content-hash
version that differs for different content, the page table including an empty
file, a file with no trailing newline and a page past the last line, `readAll`,
byte windows including one clamped at the end, and every classified refusal with
its kind -- plus the page cap checked while collecting, so one enormous line
cannot grow a page past it.

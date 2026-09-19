# 0101. The console's workspace registry is process-local, and sessions run in the host's one directory

- Status: Accepted
- Date: 2026-09-19
- Supersedes: nothing
- Related: [0094](0094-the-host-holds-the-consoles-own-settings-namespaces.md),
  [0099](0099-the-framework-core-and-the-console-adapter-are-separate-layers.md),
  [0086](0086-a-session-outlives-its-runs.md), [0100](0100-the-directory-picker-serves-the-browse-half-and-refuses-the-native-half.md)

## Context

The console's "choose workspace" flow is a chain of four host calls. The
operator picks a directory through `directoryPicker/list` (served, ADR 0100),
the panel confirms it with `workspace/create`, the adopted row is read back from
the `workspace/follow` stream, and the session that opens in it is created with
`session/create {workspaceId}`. Before this decision the host answered only the
first step: adopting a directory failed with `HTTP 404` and the console showed
its "couldn't open folder" dialog, so the workspace control was unusable even
though the directory browser worked.

The framework side of the question is narrow. `Agent` carries exactly one
workspace (`config.go` `Config.Workspace`, a `workspace.Workspace`), and `Task`
has no workspace field at all, so one agent process runs every session in the
directory it was built around. There is no per-run working directory to
override, and the harness API has none to pass (`harnesshttp` run requests carry
no workspace or cwd).

## Decision

**1. The workspace namespace and its stream are served from a process-local
registry.** `workspace/create`, `workspace/rename`, `workspace/delete`,
`workspace/archiveSession` and `workspace/unarchiveSession` are unary methods;
`workspace/follow` is a logical stream on the WebSocket mux that publishes one
`baseline` frame and then `upsert`/`remove`/`order`/`archived` increments. The
registry lives in the serve command, beside the console's settings, and is
therefore process-local for the same reason: the repository has no durable home
for console preferences, and inventing a file for one would be a second source
of truth (ADR 0094).

**2. The host's own directory is registered at construction and cannot be
deleted.** The row for the directory the process runs in is always present, so
the console's workspace list is never empty and a session can always be grouped.
`workspace/delete` on that row is refused by name (`workspace/root-immutable`),
because this host keeps running sessions there and a removed row would hide the
directory they still open in.

**3. A workspace id resolves for `session/create` only when it names the
directory this host runs in.** Any other registered row is refused with
`unimplemented`, naming both paths and the `--workspace` remedy. Every session
this host creates — with or without a requested workspace — is attached to the
host's own workspace, so the sidebar can show it under the row it actually runs
in.

**4. Directories are canonicalized before they are compared.** Registration and
lookup run the path through `filepath.Abs` and `filepath.EvalSymlinks`, so
`/tmp/ws` and `/private/tmp/ws` are one row, a trailing separator is not a
second row, and a directory reached through a symlink is the directory the link
names. Row ids are derived from the canonical path (a truncated SHA-256), so the
same directory keeps one id across a restart even though the table does not.
Titles stay distinct: a second directory with the same base name is registered
as `name (2)` rather than refused.

## Consequences

- The console's workspace control works end to end for the directory the host
  runs in: pick it, adopt it, and the session opens in it. This is the case the
  page needs before anything else, because every session and every file the
  console shows belongs to that directory.
- A directory registered from somewhere else is honest but inert. It appears in
  the list, it can be renamed and removed, and opening a session in it is
  refused with a message that names the host's directory and the flag that would
  make it the host's directory. Refusing beats the alternative — grouping a
  session under a path whose tools would still edit the host's directory.
- Per-session workspaces are a framework change, not an adapter one: it needs a
  workspace override on the run request (or a `Task`-level workspace) before any
  host can honor a second directory. Until then this limit is recorded in
  `docs/limitations.md` and the console ledger rather than papered over.
- `workspace/insertBefore` and `workspace/insertSessionBefore` remain unserved:
  they reorder rows, and a host that lists rows in registration order has no
  honest order to accept. The ledger records them as the only remaining members
  of the namespace.
- The registry is lost on restart, like every other console-written value
  (ADR 0094). A restart re-registers the host's directory and nothing else.

## Alternatives rejected

- **Deriving workspaces from sessions.** The console's row carries a path, a
  title and a manual order. A session list has none of those, so the derived
  rows would be a rendering invention rather than a registration, and the "add
  workspace" gesture would still have nothing to create.
- **Accepting any `workspaceId` and running the session in the host directory
  anyway.** The console would then show a session under one directory while the
  file sidebar, the tools and the durable log all describe another. That is the
  class of lie ADR 0082 exists to prevent.
- **Persisting the registry to disk now.** It would invent a storage format,
  a migration story and a recovery rule for a preference the framework does not
  model, and it would make the console's state outlive the process that can
  still honor it.
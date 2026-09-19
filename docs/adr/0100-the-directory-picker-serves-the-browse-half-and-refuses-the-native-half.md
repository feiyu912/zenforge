# ADR 0100: The Directory Picker Serves The Browse Half And Refuses The Native Half

Status: accepted

## Context

Selecting a workspace is the console's first control an operator cannot use at
all. The picking seam upstream is deliberately *not* one method set: it is a
capability union (`packages/host/directory-picker/src/index.ts:1-60`). A
`native` backend opens one OS chooser on the **host's** display; a `browse`
backend serves listing and creation primitives that an in-app browser drives one
level at a time, "and thereby works for remote clients no OS dialog can reach".

The console consumes it through `ui-workspace`'s service
(`packages/client/ui-workspace/src/client/navigation.ts:207-222`):
`directoryPicker/list`, `directoryPicker/createDirectory`, and
`directoryPicker/pick`. The browser half is rendered by
`ui-directory-picker-browse`, which fills `ui-workspace`'s two directory-flow
slots and reads `directoryPicker/list` through `ctx.uiWorkspace`; the native half
is `ui-directory-picker-native`, whose sibling dialog cannot render on a server.

This host is a server with no operator display. It answered none of the three
methods, so even a console that showed the browser would have had nothing to
drive it.

## Decision

**This host implements the browse capability and refuses the native one by
name.**

- `directoryPicker/list` answers one level: the listed absolute path, the host
  account's home directory, the crumb chain from the filesystem root to the
  listed directory inclusive (the root crumb carries its full path as its name,
  every crumb visible), and the name-sorted child **directories** with their
  absolute paths and hidden flag. A symlink is decided by what it points at, so
  a symlinked directory is a row; a file never is.
- A path that is not fully qualified is refused as `directory-unreadable`,
  because a wire value must never resolve against the host's working directory.
  So is a path that does not exist and one that is not a directory.
- The level is bounded at `DirectoryPickerMaxEntries` (2000, upstream's own
  posture), and a cut level reports `truncated: true` instead of silently
  dropping its name-sorted tail.
- `directoryPicker/createDirectory` creates one child under an existing
  absolute parent with mode 0755. The name must be a single non-blank path
  segment, and an existing child answers `directory-exists` — its own code, so
  the browser can say "already exists" rather than a generic failure. Every
  other failure is `directory-create-failed`.
- `directoryPicker/pick` answers `unimplemented` and names the missing
  capability: a native chooser needs a display this host does not have. Naming
  it is what keeps the in-app browser the whole interaction instead of leaving a
  caller waiting on a dialog that will never open.

## Consequences

- The picker's host half exists and is measured: the console coverage ledger
  moves the two browse methods to *served* and `pick` to *refused*, and the
  ledger's check against the routing table keeps them there.
- The console's workspace picker still shows no add action until
  `ui-directory-picker-browse` is in the served graph. It sits in the roster's
  `blocked` list because a running host once reported it — and only it — failing
  activation, and the reason was never established; a plugin present in the graph
  that cannot activate is a fatal boot page instead of the designed slot
  fallback. Re-testing that activation on a scratch port is the next step, and
  the ledger records it as such. This ADR covers the host half only.
- Browsing is the host filesystem, which is the point of a picker. The console's
  existing fence already keeps every request on loopback unless the operator
  passed `--allow-remote`, so what a picker exposes is exactly what that operator
  already reaches from the host account.
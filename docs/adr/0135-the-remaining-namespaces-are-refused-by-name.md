# 0135. The remaining console namespaces are refused by name

- Status: Accepted
- Date: 2026-09-22
- Related: 0082 (a refusal is not an empty answer), 0128 (the first refusal sweep),
  0130 (the queue that stays process state), 0134 (the chain before it)

## Context

With `fileReferences/list` served, the ledger's unserved set was 29 methods across
six families: `terminal/*` (10), `dynamicCordisRunner/*` (12), `subagents/*` (3),
`officeToPdf/*` (2), `fileUploads/upload` (1), and
`sessionReferenceResolver/candidates` (1). None of them is a capability this host
has, and reconnaissance established what each would need:

- **`terminal/*`** -- the host does run commands under a PTY (`jobs.Manager` ->
  `pty.StartWithSize`), but there is no attachment layer, no runtime resize (the
  PTY master is not exposed and nothing calls `pty.Setsize`), no screen model for
  `follow` (go.mod carries only `creack/pty`; there is no VT emulator, and the job
  buffer is lossy where a gapless screen sequence is required), and no shell
  discovery. The panel that would call these (`client/ui-sidebar-terminal`) is also
  dropped from this build.
- **`subagents/*`** -- no child-session plane: children are one-shot runs inside the
  parent run (`<parentRunID>_sub_<taskID>`), streamed off the agent rather than
  registered, absent from `RunManager` and from `session/list`, and both
  `session/page` and `session/follow` already refuse the `subagent` address arm on
  purpose.
- **`sessionReferenceResolver/candidates`** -- nothing in Go parses or expands an
  `@`-mention, so a picked conversation reference would reach the model as literal
  text.
- **`fileUploads/upload`** -- the same missing attachment store that
  `session/attachment` and `session/prompt`'s image arms already refuse with.
- **`officeToPdf/*`** -- no Office converter and no PDF renderer, and
  `client/ui-sidebar-documentpreview` is dropped from the build.
- **`dynamicCordisRunner/*`** -- no dynamic plugin runtime; this host's plugins are
  compiled in, which is the same reason the plugin-manager namespace refuses.

The ledger also carried three rows the *served* client does not declare at all:
`agentTeams/createTask`, `agentTeams/updateTask` and `agentTeams/view`. They came
from a different upstream revision; the experimental agent-team bundle is dropped
from this build, and the vendored descriptor table (`webui/dsh/plugins/api/remotes/client.js`)
declares 106 methods, not 109.

## Decision

**Every one of the 29 is routed and refused by name, and the three undeclared rows
are removed from the ledger.** The surfaces were previously `unserved`, which the
console reads as a 404 -- a transport failure an operator cannot act on. A refusal
names the capability and, where one exists, the substitute:

| Family | Capability named | Substitute named |
| --- | --- | --- |
| `terminal/*` (10) | an embedded terminal | the agent's own shell and job tools |
| `subagents/*` (3) | a child-session plane | -- (the follow stream refuses the same address arm) |
| `dynamicCordisRunner/*` (12) | a dynamic plugin runtime | -- (plugins are compiled in) |
| `officeToPdf/*` (2) | an Office document converter | `workspaceFiles/readAll`, the preview's byte arm |
| `sessionReferenceResolver/candidates` (1) | a session reference resolver | `fileReferences/list`, the other half of the same `@` menu |
| `fileUploads/upload` (1) | an attachment store | put the file in the workspace and ask the agent to read it |

- One handler per family, routed per declared method name -- the shape
  `pluginManagerUnsupported` already uses. Per-method handlers carrying one sentence
  would let the sentences drift; but the *routing* stays per method, so a method the
  client does not declare (`terminal/detach`) is still a 404 rather than a
  catch-all.
- `fileUploads/upload` reuses the attachment sentence **verbatim**
  (`attachmentRefusal`), because it and `session/attachment` are the same missing
  store: a caller that meets one must not meet a different story at the other.
- `subagents/*`'s sentence matches the spirit of the follow stream's own subagent
  arm, so the two halves cannot disagree about why no child session exists.
- `terminal/follow` is the sweep's only *stream*: the mux gained an explicit arm so
  it answers `unimplemented` with the reason and the capability, instead of the
  generic "stream endpoint not found" the default arm would give.
- The ledger's row set is now checked against the client's own descriptor table in
  **both** directions
  (`TestConsoleCoverageLedgerListsExactlyWhatTheClientDeclares`): a row for a method
  no client declares fails, and a declared method with no row fails. That is the
  check that would have caught the three `agentTeams/*` rows.

## Consequences

- The ledger reads **56 served / 4 streams / 46 refused / 0 unserved** of **106**
  declared methods: no row is left doing nothing. Its header prose (which had said
  47 served of 109) is corrected with it.
- Live evidence, from a `zenforge serve` on this commit: one refused method per
  family answered `unimplemented` with its own sentence and capability --
  `terminal/create` and `terminal/resize`, `subagents/list`,
  `sessionReferenceResolver/candidates`, `fileUploads/upload` (with the attachment
  sentence), `officeToPdf/render`, `dynamicCordisRunner/runHostHalf` -- while
  `terminal/detach`, a method the client does not declare, answered **404**; and the
  `terminal/follow` stream, opened on `/api/remote.mux` the way the console opens
  it, answered `{"type":"error", "code":"unimplemented", "capability":"an embedded
  terminal"}`.
- What remains is host work, not console work, and `## Next up` in the ledger says
  so: the durable inbox fold (ADR 0130) first, then whichever of these capabilities
  is ever built. Each refusal already names what it would take.
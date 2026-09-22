# ADR 0126: A Desktop Capability Is Answered, Not Built

Status: accepted

## Context

Two declared `session/*` methods are about the **host desktop**, and the ledger's
next-up prose had read them as something else -- "opening a workspace into a
session, the other half of selection". The reference declares them as a question
and an operation over the *machine serving the console*
(`api/session-controller/lib/types.d.ts:334-343`, `lib/index.d.ts:95-119`):

- `session/canOpenWorkspacePath()` -- "Report whether this deployment can hand a
  Session workspace path to a native desktop. Returns true when the matching open
  operation is available." No parameters; the declared result is `boolean()`.
- `session/openWorkspacePath({path, action?})` -- "Open one path prepared by a
  Session-aware caller **on the Host desktop**", where `action: "reveal"` asks
  for file-manager navigation and its omission asks for the default application.
  The declared result is `{opened: true}`: a receipt, not a report, so the call
  either succeeds or fails.

Neither has a caller in the pinned bundle. The caller upstream is the desktop
carrier (`dshDesktopBoot`, `apps/web/src/main.ts:4-34`), and this host serves the
browser carrier: the protocol recon already declared "Desktop/worker carrier
(`dshDesktopBoot`, `__DSH_TRANSPORT__.rpc/fetch/loadBundle`) -- not needed"
(`docs/dsh-console-protocol-recon.md`). Leaving them unrouted is the third option
and the worst one: they are part of the surface the console's gateway validates,
so an unserved method is a protocol failure rather than an answer, and the ledger
cannot say why.

## Decision

- **The question is answered.** `session/canOpenWorkspacePath` returns `false`, as
  a bare JSON boolean. The declared schema is `boolean()`, the transport passes
  `result.value` through untouched, and a caller branches on it -- so an error
  would be a contract violation that reads as "this host is broken" rather than
  "this host has no desktop". That difference is a disabled affordance versus a
  failed page, which is exactly the distinction the ledger's `refused` tier exists
  to keep. The zero-argument method keeps the upstream exact-arguments rule.
- **The operation is refused by name.** `session/openWorkspacePath` answers
  `unimplemented` with the missing capability in its details and one sentence
  naming the reason *and* the substitute: this host serves the console in a
  browser and has no desktop carrier to open a path on, while
  `workspaceFiles/list` and `workspaceFiles/read` show a file inside the session.
  The request's own declared fields (`path`, `action`) are validated *before* the
  refusal, so a typo is still reported as the typo it is; a **valid** request is
  refused too, because what is missing is the host's desktop and not an argument.
  The probe's `false` and the refusal's sentence are one decision, so the sentence
  is written once and both halves read it.
- **No opener is built.** Answering `false` is not a placeholder for an
  implementation that was too much work: `openWorkspacePath` would spawn a native
  file manager (`open -R`, `explorer /select,`, `xdg-open`) from the host process
  on behalf of a request that any page reaching the loopback API can send. That is
  a side effect on the operator's own machine, with no caller in the pinned bundle
  and no desktop carrier in the deployment. If a desktop carrier is ever shipped,
  the probe flips to `true` and the operation is implemented in the same change --
  the two are one decision, and the ADR is where that is written down.
- **The ledger's misread is corrected rather than carried.** Item 1 of "Next up"
  was wrong about what the family does; both methods are now routed (one served,
  one refused), so the item is removed and the rest renumbered. The reference
  declares a third member of this family, `session/workspaceDesktop()` (the
  serving desktop's name, availability and file-manager kind), which our pinned
  client does not declare, so it is not part of the surface the ledger measures;
  when the bundle declares it, it belongs to this same answer.

## Consequences

- The envelope is pinned against the vendored bytes by
  `TestWorkspaceDesktopEnvelopesMatchTheVendoredConsole`: the probe's result is a
  bare `boolean()` with no object keys, the operation declares exactly one
  `request` object whose keys are `action`/`path` and a `{opened: true}` receipt,
  and this host's handler accepts exactly the flattened names (`path`, `action`)
  the client sends. `TestCanOpenWorkspacePathAnswersABoolean` asserts the served
  value is the raw JSON `false`, `TestOpenWorkspacePathNamesTheMissingCapability`
  refuses a flat request, a request splice and an explicit `action: "reveal"`
  alike, and `TestWorkspaceDesktopRejectsUnknownArguments` keeps the
  exact-arguments rule on both halves.
- The bundle-truth readers became a reusable `vendoredBundle` (source, package,
  namespace) in `goals_test.go`, so this family and the goal family read the same
  generated remote map instead of two hand-copied parsers. What the goal tests
  assert is unchanged.
- The ledger moves to **45 served, 4 streams, 16 refused, 44 unserved** of 109,
  and its next-up list now begins with `session/search`, `session/fork`,
  `session/attachment`, `session/updateQueue`. The refused row quotes the handler's
  sentence verbatim, like every other refused row.
- Live on a scratch host (`--addr 127.0.0.1:8799`, throwaway `--checkpoint-dir`
  and `--settings-file`): `session/canOpenWorkspacePath` answered
  `{"type":"server-response",…,"result":{"ok":true,"value":false}}`;
  `session/openWorkspacePath` with `{"path":"/tmp"}`, with
  `{"request":{"path":"/tmp","action":"reveal"}}` and with a flat
  `{"path":"/tmp","action":"reveal"}` answered the same
  `"code":"unimplemented"` naming `"capability":"a desktop carrier to open a path
  on"`; and `{"path":"/tmp","reveal":true}` answered
  `"code":"gateway/arguments-invalid"`.
- Nothing in the shipped page changes, because no bundle calls either method. What
  changes is that the declared surface answers instead of 404-ing, and that the
  console's desktop question has a truthful `false` behind it instead of silence.
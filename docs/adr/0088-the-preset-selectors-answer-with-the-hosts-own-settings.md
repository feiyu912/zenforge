# ADR 0088: The Preset Selectors Answer With the Host's Own Settings

Status: accepted

## Context

The console asks for `permissionPresets/catalog` and `agentPresets/list` while it
boots -- the composer's permission and preset selectors read them -- and the host
now records every endpoint it does not serve, so this is observed rather than
assumed (`INFO console rpc endpoint is not served by this host
namespace=permissionPresets method=catalog`, then `namespace=agentPresets
method=list` twice, on a single page load).

Upstream, a permission preset *is* a sandbox and approval combination
(`packages/interaction/permission-presets/src/index.ts:185-194`):
`workspace-write` is a confined sandbox that asks for approval, and
`danger-full-access` is an unconfined sandbox that never asks. This host has
exactly those two knobs as startup flags (`--sandbox`, `--approve`) and a third
for its execution preset (`--mode`), and none of them can be changed once the
process is up.

## Decision

### The catalog reports the combination the host is running

`permissionPresets/catalog` answers one option, derived from the host's
`--sandbox` and `--approve`:

- unconfined and never asking is upstream's `danger-full-access`;
- a confined sandbox that asks is upstream's `workspace-write`;
- anything else is upstream's own reserved `custom`, whose description states the
  raw combination (`--sandbox none --approve prompt`).

Every description names the flags it was derived from, so an operator reading the
panel can see why a named preset did or did not match instead of trusting a
plausible-looking wrong one. One option is the honest length: this host cannot
switch policy for a session, so a list of choices would be a selector that
silently does nothing.

### The roster is a truthful list, not a selector

`agentPresets/list` answers one row per execution preset the host implements
(`react`, `oneshot`, `plan_execute`), with ids taken from the agent's own mode
constants so a renamed mode cannot drift away from the roster. Every row is
`trust: system` because every preset here is compiled in, and the row the host
actually runs is marked `isDefault` -- an explicit `--mode`, otherwise the
planning preset when planning is on, otherwise the plain model/tool loop.

`authorable` and `modeSelectionEnabled` are both false, and that is the point of
the answer: the host has no preset directory to author into and no way to change
a session's preset, so the console finds out here rather than through a write
that appears to work. `broken` is never sent -- a compiled-in preset cannot be
unreadable.

### Reads explain; writes refuse with upstream's own codes

`agentPresets/read` returns the mode's own wording plus a plain statement that
this host has no preset file behind it. An unknown id is upstream's
`agent-preset/not-found` with `details.available`, the authoring writes (`copy`,
`deletePreset`) are upstream's `agent-preset/read-only` with `details.reason`,
and per-session selection is `unimplemented` with `capability:
"per-session agent preset selection"`. A console that already handles upstream's
answers handles these; a console that does not still gets a code and a reason
instead of a silent success.

## Consequences

- Two failures the console hit on every page load are now answered surfaces, and
  the composer's selectors have something true to render.
- The permission policy is still fixed at startup. That is now visible in the
  panel's own description text rather than only in the host's flags.
- Authoring and selecting an agent preset remain gaps, named at the moment they
  are attempted. Supporting them needs a preset directory and a session-level
  preset event with its projection, neither of which this host has.

## Alternatives Rejected

### Report upstream's three presets as selectable

Two of them would appear to be choices and change nothing, which is the failure
mode this repository keeps refusing: a configuration surface that shows a state
the run path is not in.

### Report no options, or keep the namespace unserved

The console renders an empty selector and an operator cannot see the policy the
host is enforcing -- for a host that gates tool calls on approvals, that is the
one setting an operator most needs to see.

### Make the catalog settable

It would need session-level permission events and the permission projection the
console reads back, and this host fixes its policy per process. Settable presets
are their own decision, not a side effect of answering a catalog request.

### Name a preset the host is not running

Mapping "confined sandbox but never asking" onto `danger-full-access` (or the
reverse) would hide exactly the combination an operator needs to notice.

## Verification

`go test ./internal/dshapi/ -run 'TestPermissionPresets|TestAgentPreset|TestPresetSurfaces'`
-- the catalog mapping table across unconfined/confined and asking/never-asking
including the unset-sandbox default, each description proven to name its flags;
the roster's one default, system trust, and the absence of `broken`; the read
document stating there is no preset file; the unknown id answering
`agent-preset/not-found` with the available ids; the authoring and selection
refusals carrying their codes and reasons; and every surface answering
`unimplemented` with `PresetSource` named when no source is installed -- plus the
package runs `go test ./internal/dshapi/ ./internal/dshmount/ ./cli/`.

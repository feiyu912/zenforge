# ADR 0091: The Plugin Inventory Describes What This Host Publishes

Status: accepted

## Context

The console's Plugins settings tab reads `pluginInventory/list`
(`client/ui-settings-plugin-inventory/src/client/index.ts:38`), and the packages
manager reads it **before anything else**: when `managementAvailable !== true`
that panel reports itself unavailable and never calls `pluginManager` at all
(`client/ui-plugin-manager/src/client/manager-store.ts:461-470`).

Upstream's `PluginInventoryEntry` is `{entryId, moduleName, enabled, fiberPhase}`
and the snapshot also carries `managementAvailable`
(`host/plugin-inventory/lib/types/types.d.ts:4-32`). This host serves a fixed set
of prebuilt console bundles from an embedded tree; it has no loader, and the mount
already holds the complete statement of what it publishes and what it withholds
(`roster.json`, `ADR 0078`/`0080`).

## Decision

### The inventory is the mount's own roster

One row per roster module: `entryId` is the staged directory the bundle is served
from, `moduleName` is the module specifier, and `enabled` says whether this host
publishes it. The upstream siblings the host deliberately withholds are listed as
disabled rather than omitted, so the listing is complete rather than convenient --
an operator looking for one of them finds it and sees that it is not served.

The wire type has no field for the roster's reason, so the reason is not carried.
That is a stated limitation rather than a hidden one: the roster and
`PROVENANCE.md` hold it.

### `fiberPhase` is always null

A fiber's phase is a client-side fact. This host serves static assets and never
observes the console's runtime, so it has nothing to report and says so instead of
claiming `active` for a module it cannot see running.

### `managementAvailable` is false, and the manager degrades on its own

There is no loader behind the tree, so no enable, disable, install or remove could
take effect. Answering false is what makes the packages panel report itself
unavailable instead of offering buttons that fail.

### Every `pluginManager` method is refused with the same reason

`listBundles`, `listPlugins`, `inspect`, `installBundle`, `removeBundle`,
`setBundleEnabled`, `setPluginEnabled` and `cancelInstall` all answer
`unimplemented` with `capability: "host plugin management"` and
`managementAvailable: false` in the details, so a caller that skipped the inventory
still learns why the write cannot land.

### `agentPresets` is omitted, not empty

It would describe presets as compositions of plugin rows. This host's presets are
compiled in (`ADR 0088`), so there are no rows to report; an empty array would
claim presets exist that are simply composed of nothing.

## Consequences

- The Plugins tab lists what this host publishes and what it withholds, and the
  packages panel degrades to "unavailable" without a single failing call.
- Nothing in the console can change the served bundle set at runtime. That is the
  host's design: the tree is generated from a pinned upstream revision and
  validated at boot (`ADR 0080`).
- The withheld entries' reasons remain outside the wire type.

## Alternatives Rejected

### List only the published modules

The three withheld ones exist upstream and an operator may be looking for them.
Silence there is indistinguishable from an oversight.

### Claim `fiberPhase: 'active'` for published modules

The host cannot see the console's fibers. Reporting `active` would be a guess
presented as an observation.

### Serve an empty inventory

The tab would render nothing, which reads as "this host has no plugins" rather
than "this host publishes a fixed set".

### Implement `pluginManager` by writing into the embedded tree

It is generated data validated against a pinned revision at boot. Runtime edits
would either be discarded at the next build or break the boot graph -- and the
consistency check between the roster and the staged tree is exactly what makes the
mount trustworthy.

## Verification

`go test ./internal/dshapi/ -run TestPlugin` -- the snapshot's
`managementAvailable: false`, the published and withheld rows, `fiberPhase` sent
as `null`, `agentPresets` absent rather than empty, an empty inventory answered as
`[]`, `unimplemented` with `PluginInventorySource` named when no source is
installed, and all eight `pluginManager` methods refused with their capability and
`managementAvailable` detail. `go test ./internal/dshmount/ -run
TestPluginInventory` -- the real roster: one row per module, both ids non-empty,
`fiberPhase` null throughout, the enabled and withheld counts, and the withheld
native directory picker present and disabled. That last assertion caught a real
bug: the entries table lists withheld modules too, so the first version reported
the one module an operator most needs to see as withheld **twice**, once enabled.

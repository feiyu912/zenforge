# ADR 0080: The Module Graph Is Validated, Not Trusted

Status: accepted

## Context

ADR 0079 decided to serve the upstream console from a pinned, rebranded build.
The first half of the host side is the boot path: a module graph the console
loads its plugins from, the injections that must reach the page before the
shell's own script, and the `/plugins` route that serves the bundles the graph
advertises.

The console's own failure mode decides how careful this has to be. A plugin that
is **absent from the graph** degrades: its slot renders a fallback. A plugin that
is **in the graph but whose bundle cannot be fetched** is fatal — the console
shows a boot page that says the plugins failed to load. So an invalid graph is
not a cosmetic problem; it is a console that never starts, reported as a generic
plugin failure that names no cause.

## Decision

### Validate where the graph is built, and name the offender

The graph is built and validated here, at startup, and an invalid one fails with
an error that names the offending id or URL — duplicate entry id, an entry that
belongs to two batches or none, a duplicate batch URL, a batch naming an unknown
entry, a missing revision, a module-graph cycle, an entry that lists itself as
external, or a combination URL over the client's 3072-byte limit. The upstream
client validates the same shape at boot; a host that skipped it would move the
failure to a place where an operator sees "failed to load plugins" and has
nothing to act on.

### `/plugins` is an exact-match table, not a path prefix

The route answers the URLs the graph advertised and nothing else. Lookup is a
map keyed by path plus raw query — which also preserves upstream's unusual
`??` combination spelling — so an unadvertised combination, a stale revision,
`/plugins/../x`, and `/plugins/` are all 404s by construction rather than by
filtering a path. A directory walk here would mean a request could reach bytes
the graph never promised, and a stale revision would silently serve a mixed
release: the revision is part of the key, so it cannot.

### Revisions are derived, and the console treats them as opaque

The graph and batch revisions are stable hashes over the ordered entry
revisions and the graph, rather than upstream's per-process nonce. The console
uses `rev` only as an opaque cache key, and a stable derivation is what makes
the served bytes cacheable, the staged artifacts reproducible, and a rebuilt
tree diffable — which is the whole point of committing them with a recipe
(ADR 0079).

### Stricter than upstream in three places, on purpose

An empty revision is rejected, chunk names are validated when the graph is
built, and duplicate chunk names are refused. Upstream can afford to be laxer
because it owns both halves in one process and can check at request time; a host
serving bytes pays for a late discovery with a boot failure, and the assertion
is cheaper here than the failure is there.

### The queue script is copied, not rewritten

The inline script that installs the module loader's queue facade is
byte-identical to upstream's (extracted programmatically from the sources and
diffed). It is the one piece of the boot path where an approximation would fail
in a way that looks like a missing bundle. Injections are idempotent, and the
graph JSON escapes `<`, so an id or revision containing `</script>` cannot break
out of its element.

### Source maps are not served

The console does not need them to boot or run, the recipe drops `.map` files
from the staged tree because they dwarf the artifacts (ADR 0079), and a request
for one is a 404. Half-serving maps — some present, some not — would be worse
than not serving them, because a developer would trust whichever one loaded.

## Consequences

- A graph bug is a startup error with a name in it, instead of a console that
  reports a plugin failure and no cause.
- The route cannot leak a file the graph did not advertise, and a stale cache
  cannot mix two releases.
- Debugging the console in a browser is map-less: stack traces point into
  minified bundles. Rebuilding with maps is a deliberate, local choice, not
  something the committed artifacts carry.
- The remaining host work — the `/api` RPC surface, the WebSocket streams, and
  the approval bridge — has a validated boot path to build on.

## Alternatives Rejected

### Trust the graph and let the console validate

The client does validate, and its failure is the fatal generic boot page. The
information needed to fix the graph exists at the moment it is built; discarding
it and reconstructing it from a browser error is strictly worse.

### Serve `/plugins/` as a directory tree

Simpler to write, and it would serve exactly the same bytes for every valid
request while also serving files the graph never advertised — including, after a
rebuild, a mixture of two releases. The map is the same amount of code and has a
smaller set of possible answers.

### Reuse upstream's nonce revisions

Then the same sources produce different bytes on every build, the artifacts stop
being diffable, and caching becomes pointless. The console does not care what
the revision is, only that it is consistent within a graph.

### Validate chunk names at request time, as upstream does

By then the developer's cost is a failed boot with a 404 in a network panel,
and the host's cost is the same check moved later. Validating where the graph is
built also means the check can name which entry wanted the missing chunk.

### Ship source maps

They are the single largest part of the build output (megabytes per bundle), the
console runs without them, and staging a subset invites debugging against maps
that do not match what was served.

## Verification

`go test ./internal/dshboot/` — `TestBuildGraphProducesGraphTheConsoleValidates`,
`TestBuildGraphRejectsDuplicateEntryID`,
`TestBuildGraphOrdersModuleGraphDependencies`,
`TestBuildGraphRejectsModuleGraphCycle`,
`TestBuildGraphRejectsOversizedComboURL`,
`TestValidateGraphRejectsEntryInTwoBatches`,
`TestValidateGraphRejectsEntryInNoBatch`,
`TestValidateGraphRejectsDuplicateBatchURL`,
`TestValidateGraphRejectsUnknownBatchEntry`,
`TestValidateGraphRejectsBadPhase`,
`TestBuildGraphRejectsMalformedChunkName`, `TestComboURLShape`,
`TestComboURLAlwaysCarriesRevision`, `TestChunkURLShape`,
`TestChunkURLAlwaysCarriesRevision`, `TestBuiltURLsAlwaysCarryRevision`,
`TestRenderInjectionsHeadOrder`, `TestRenderInjectionsAddsBodyTailAndGlobals`,
`TestRenderInjectionsIsIdempotent`, `TestRenderInjectionsEscapesScriptClose`,
`TestRenderInjectionsHandlesDocumentsWithoutHeadOrBody`,
`TestHandlerServesCombinationURLInOrder`, `TestHandlerServesEntryURL`,
`TestHandlerServesChunkURL`, `TestHandlerServesHeadWithoutBody`,
`TestHandlerNotFoundForUnadvertisedPaths`, `TestHandlerRefusesStaleRevision`,
`TestHandlerRejectsUnsupportedMethod`, `TestNewRejectsEntryWithoutBundleSource`,
`TestNewRejectsMissingChunkFile`, `TestNewReadsClientBundleFromEntryFS`.
Plus `go test -race ./internal/dshboot/ -count=1`, `go test ./... -count=1`,
`go vet ./...`, `gofmt -l`, and `go test ./docs/...`.

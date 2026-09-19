# ADR 0079: A Console Under Our Own Name

Status: accepted

Amended by ADR 0092: the browser-visible identity is spelled `zenforge`.

## Context

The reference harness ships a browser console that is genuinely good at the
thing an operator needs: a run is a stream of events with tool calls inside it,
and a terminal scrollback is a poor way to watch several of them at once. Its
sources are public and MIT (`deepseek-ai/deepseek-harness`), so the console can
be served by another host — ADR 0078 built the Go host skeleton for that.

The protocol reconnaissance settled two facts that decide this ADR. First, the
console is not a bundled application in the sense one would hope: the shell is a
static Vite build, but the interface itself arrives as client plugins that the
host must list in a module graph and serve as individual scripts. That is
workable — the host implements a graph route, not a bundler. Second, and
decisively, the console **cannot be renamed at runtime**: the shell's title, the
boot page's wordmark, the layout's `productTitle`, the sidebar's name and mark
fallbacks, and the empty-state hero are all compiled into the shell and into
three client packages, and the published bundles have the official-brand gate
compiled out.

This repository serves one product and its name is zenforge. A console that
announces another product's name to the operator — and whose logo is another
product's mark — is not the console this project ships.

## Decision

### Build the console, rather than serving someone else's build

ZenForge builds the console from the upstream sources at a pinned release, with
the upstream build's own local profile (`DSH_CLIENT_BUILD_PROFILE=local`, which
is the profile that skips the official brand slots) and its title set to
`ZenForge`, then applies a small, reviewed patch for the strings the profile
does not reach: the boot page's wordmark, the layout title, the sidebar's name
and mark fallbacks, and the empty-state hero.

The upstream build must be used rather than the published dist because the
published artifacts are the *official* profile: their brand gate is compiled
out and the strings are baked in, so no host-side injection can replace them.
Rebuilding is not an optimization here; it is the only way to present this
product's name.

### Build once, commit the artifacts, keep the recipe

The built console is a small set of static files. It is committed, together with
the pinned upstream revision, the exact commands, and the patch, as a script in
`scripts/`. The main CI stays Go-only: a Go build remains the whole toolchain
for the product, and the console is an asset the release carries rather than a
step every build performs. A separate manually triggered workflow rebuilds the
console for anyone who needs to reproduce or bump it, because a committed
artifact with no recipe is a binary nobody can audit, and a recipe that runs on
every commit would put a Node toolchain and a several-hundred-package install on
the critical path of a Go repository.

The pinned revision and the upstream packages' versions move together: the
shell and its plugins are one release. Bumping means rebuilding, diffing the
result, and updating the pin in the same commit.

### Attribution travels with the artifact

The console is MIT-licensed upstream work, modified. `THIRD_PARTY_NOTICES.md`
records the source, the license, the copyright line, and the nature of the
modification — a downstream rebrand, permitted by the license, that implies no
endorsement by the upstream project. The rebranding removes upstream's name from
what the operator sees; it does not remove upstream's notice from the
repository.

### The host protocol is ours to implement, and the roster grows by tiers

Serving the console means implementing the host side of its protocol in Go: the
boot injections and module graph, the unary `/api/<namespace>/<method>` RPC
envelope, the WebSocket mux and its streams (`session/follow`,
`session/control`, `$events`), and the capability set the console calls. The
reconnaissance recorded the exact shapes; the implementation proceeds in tiers,
starting with the minimum chat roster (about 23 plugins) and adding panels only
when the host namespace each panel needs actually answers. A plugin whose
service has no provider is a fatal boot failure, so a tier is enabled when its
server side exists, never before.

The console's approvals ride the waterfall event and answer with
`allowed-once`/`rejected`, mapped onto the approval broker this repository
already has (ADR 0056/0075): a console answer is the same decision a CLI answer
is, and a failure to answer stays a refusal rather than a silence that hangs.

## Consequences

- The operator gets the console they asked for, under this project's name, with
  no Node toolchain in the normal build.
- The repository carries a built artifact whose provenance is a pinned upstream
  revision and a reviewable patch; the cost is that bumping the console is a
  deliberate, manual step, which is the correct cost for a dependency that
  cannot be renamed at runtime.
- The embedded first-party console from ADR 0078 remains as the fallback and as
  the testing surface: it needs no plugin graph, so it still works on a host
  where the plugin route is unavailable or a build has no console artifacts.
- Until the roster tiers land, some console panels will be absent rather than
  broken: an omitted plugin renders its slot's fallback, which is the failure
  mode this ADR prefers over a fatal boot page.

## Alternatives Rejected

### Serve the published console and accept its branding

Free and immediate, but it shows another product's name and logo to the
operator of this project. The requirement that decided this work was the name.

### Inject a rename at runtime

Not possible for the decisive strings: they are compiled into the shell and the
brand/layout/sidebar/conversation packages, and the published bundles have the
official-brand gate compiled out. An injection that changed the title would
leave the wordmark, the logo, and the hero unchanged — a half-rename is worse
than either honest option.

### Build the console in the main CI on every commit

A several-hundred-package install and a Vite build for a Go repository's every
commit, to produce artifacts that change only when the pin changes. The manual
workflow keeps the recipe executable without charging every build for it.

### Fork the whole monorepo into this repository

Then the console's dependencies' sources live here, and this repository's
history becomes that repository's history. Only the built artifact, the patch,
and the pin are needed to reproduce the console; a fork would also invite local
divergence in sources that are not ours to maintain.

### Write a second, first-party console with the reference's interaction model

ADR 0078 did exactly this as the first slice, and it remains the fallback. It is
not the answer to this decision because the operator asked for the reference
console specifically, and its plugin roster is the part that is expensive to
reimplement — not the layout.

## Verification

The build recipe lives in `scripts/` and is expected to be runnable from a clean
checkout of the pinned revision; a manually triggered workflow runs it. The
committed artifacts are checked by the host's tests: the shell and each asset
are served with their media types, every URL the page references resolves
through the host's routes, the module graph validates (unique ids, every entry
in exactly one batch, URLs matching the advertised revision), and the served
page contains the ZenForge title, the ZenForge wordmark, and no upstream product
name. `go test ./...`, `go vet ./...`, `gofmt -l`, and `go test ./docs/...` cover
the rest, and `THIRD_PARTY_NOTICES.md` carries the upstream license.
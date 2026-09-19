# ADR 0099: The Framework Core And The Console Adapter Are Separate Layers

Status: accepted

## Context

ZenForge is a Go-native agent harness meant to be used as a framework: the deep
API (`Agent`, `Task`, `Config`) for a caller who wants a working agent in a few
lines, and the harness core (`harness/`, `tool/`, `model/`, `workspace/`,
`sandbox/`, `eventlog/`) for one who wants to replace runtime pieces. Products
built on it — ZenMind's app server, and now the DSH console host in this repo —
are consumers of that framework, not parts of it.

Twenty of the 99 ADRs are the console adapter's own history
([0078](0078-a-console-on-localhost.md), [0079](0079-a-console-under-our-own-name.md),
and [0081](0081-the-console-answers-in-envelopes.md) through
[0098](0098-a-declared-profile-is-edited-field-by-field.md)). By the end of that
run, the repository front door no longer said what the project *is*: the
architecture document did not mention the console layer at all, the ADR index
read as one continuous console log from 0078 on, and the CLI's visible entry
point (`zenforge serve`) is a console host. Someone opening the repository
could reasonably conclude it is a console application with a library attached.

The code boundary was, and is, clean:

- No Go package outside the adapter tier imports it. `internal/dshapi`,
  `internal/dshboot`, `internal/dshmount`, `internal/dshsession`,
  `internal/dshstream`, `cli/`, and `webui/dsh/` depend on the core; the core
  depends on none of them.
- The adapter lives under `internal/`, so a framework user cannot import it
  even by accident.
- Console-only vocabulary (`llm-pi-ai`, `ui-onboarding`, `remote.mux`,
  `session/selectModel`, the DSH method names) appears nowhere outside those
  paths.

What was missing was a stated rule and something that keeps it stated.

## Decision

**ZenForge has three layers, and the dependency arrow points one way.**

```text
deep API      easy default agent for most users
harness core  replaceable runtime pieces for advanced users
adapters      optional bridges a particular product or UI needs
```

1. The first two layers are *the framework*. The third is consumed by them.
   An adapter may import the core; the core may never import an adapter, and
   no adapter's vocabulary may appear in a core package.
2. The DSH console host is one adapter, confined to `internal/dsh*`, `cli/`,
   and `webui/dsh/`. It is optional in the strongest sense: the framework
   builds, tests, and serves its own examples without it.
3. `docs/architecture.md` states the three layers at its top and lists the
   adapter tier in its package layout. A test fails if that statement
   disappears.
4. The console series is grouped under one heading in `docs/adr/index.md`, so
   the framework's own decisions remain the main line of the record.
5. The adapter's capability ledger is
   [docs/dsh-console-coverage.md](../dsh-console-coverage.md): every remote
   method the console's client declares, marked served, refused by name, or
   unserved, with the reason. A test keeps that ledger consistent with the
   host's routing table, so a capability cannot be added or removed in code
   without the ledger moving with it.

## Consequences

- A framework user reads `architecture.md` and sees a harness with an optional
  console, not a console product. The console ADRs stay as the honest record of
  how the adapter was built, but they are visibly one tier's history.
- The boundary is enforced by `docs/console_boundary_test.go`: a new console
  import in a core package, or a DSH identifier in core source, fails the
  build instead of quietly becoming the project's shape.
- Partial support is now legible. The console's client declares far more
  methods than this host answers; the ledger says which, and names what the
  gaps are (`directoryPicker/*` for workspace selection, `goal*`, `terminal/*`,
  `subagents/*`, `workspace/*`, and more). "The panel cannot select anything"
  becomes a line item instead of an investigation.
- Extracting the adapter later is a mechanical move: it already has no
  inbound dependencies from the framework, so it can become
  `adapters/dshconsole/` or a separate module without touching the core.
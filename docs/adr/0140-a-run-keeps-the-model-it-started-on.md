# 0140. A run keeps the model it started on: the task names its own route, the run resolves it once, and the console stops installing a host-wide adapter

- Status: Accepted
- Date: 2026-09-24
- Related: 0096 (superseded in part: a selection is no longer an adapter the host
  installs before a run), 0102 (the settings document), 0095 (declared provider
  profiles), 0099 (the core carries no console vocabulary), 0103 (a chosen model
  persists in the settings document), 0084 (one configured credential)

## Context

ADR 0096 made the console's model picker per session in what it recorded, and
left its effect host-wide. `session/prompt` called
`ApplyModelSelection(sessionID)`, which built the session's adapter and
installed it into the serve command's single `swappableModel`
(`cli/serve.go`, `settingsStore.setModelAdapter`). Every model call re-read that
one global, because `swappableModel.Stream` reads its inner adapter per call:
what a run answered on was whatever the most recent prompt had put there, not
what that run had been started on.

The limitation ADR 0096 recorded as a consequence was therefore the mechanism
itself, not a missing nicety. Two sessions prompted concurrently under different
selections overwrote each other, and a run already answering could change models
between its own first step and its second. It could not be repaired inside the
console, because the harness's `zenforge.Task` carried no model at all: there
was nowhere for a run to keep one, so every call had to go back to the host's
single adapter. `RunState` did carry a `Model` field with `Provider` and `Name`,
but no code ever wrote it, so it was persisted shape rather than a record of
anything a run used.

## Decision

### 1. The task may name its own model

`Task` (repo root `task.go`) gained `Model model.Model`, `ModelProvider string`
and `ModelName string`. The two halves do different work. `Model` is an adapter a
caller has already built, so it is live and not serializable. `ModelProvider`
and `ModelName` are the route, which is two strings, and that is what can be
written into durable state and read back after a restart. `subagent.TaskSpec`
already had the first half (`Model`); the route half is new, and it is what makes
a run resumable on its model rather than on the host's.

The core's vocabulary stays console-free (ADR 0099). What the harness learns is
the harness sentence "a task may name its own model"; the route is a provider and
a model name, which is what a model client already calls its own configuration
(ADR 0095). No console word enters the core: not selection, not session, not
catalog, not picker. `docs/console_boundary_test.go` enforces that, and it is why
the field is named `Model` on a task rather than after the console act that fills
it in.

### 2. A run resolves its model exactly once and keeps it

`Agent.Stream` fixes the selection before anything else about the run is decided
(`openRunModel`), registers the resulting adapter in a registry that belongs to
the `Agent` (`modelsMu sync.RWMutex`, `models map[string]model.Model`, keyed by
run id), and drops the entry from the goroutine that opened the run, so an entry
lives exactly as long as its run. Every model call reads it through
`modelForRun(state.RunID)` rather than reading `a.config.Model` directly. Two
call sites were re-pointed, `callModel` and `callModelAttemptDurable`, because
those were the two places the old code reached for the configured adapter.

A task that named nothing keeps the agent's configured adapter, so a CLI run is
unchanged: it runs on what the operator configured, and no route is involved. The
registry is keyed by run rather than a single field on the agent because two runs
of one agent are ordinary here, and neither may observe the other's model. A
route that cannot be resolved refuses the run at `Stream`, naming the provider and
the model it could not build, before any turn is admitted.

### 3. The route is frozen into the run's durable Meta

`metaModelProvider = "zenforge.model_provider"` and
`metaModelName = "zenforge.model_name"`, written by `freezeModelRoute`, and only
when a route was actually named: a run that named none records none rather than a
false one. A resume (`Agent.Resume`) and a fork (`Agent.Fork`) re-resolve the
route from that Meta through the same resolver, so the model a run continues on
is the model its checkpoint says it started on.

`RunState.Model.Provider/Name` was deliberately left alone. It is persisted, but
nothing has ever written it, and giving it a writer now would make two durable
fields mean the same thing with different lifetimes, for any reader that already
treats it as "the model that ran". The Meta pair is additive, and it is what a
resume reads. A route the host can no longer build -- a credential removed, a
profile deleted, a model no longer offered -- is a refused resume or fork, never a
silent run on another adapter: a resume that quietly switched models would
continue a conversation under a model the operator never chose, and the
transcript would not show it.

### 4. The console hands over both, and the store's shape changed

`dshapi.ModelSelectionStore.ApplyModelSelection(sessionID) error` is replaced by
`ModelRoute(sessionID string) (ModelRoute, bool, error)` and
`MarkModelUsed(sessionID string)`, with `ModelRoute{Provider, Model string;
Adapter model.Model}`. The three results are three different states. `ok=false`
means the session chose nothing, so the task names no route and the run uses the
host's configured adapter: the picker's "no choice yet" is a real state and not an
error. A non-nil error is a recorded choice this host can no longer build (a
missing credential, a model no longer offered), and it makes `session/prompt`
refuse synchronously, as ADR 0096 already did, because the operator looking at
the composer is the one who can fix it.

`session/prompt` puts `route.Adapter`, `route.Provider` and `route.Model` on the
Task on both the first-turn and the continuation path, so a later turn of a
conversation does not fall back to the operator's default. It calls
`MarkModelUsed` only after the run really started, and only for a session that
named a route. The record itself still lives in the settings document and
survives a restart (ADR 0102, ADR 0103); what changed is only where its effect
lands -- on the run, not on the host.

### 5. `MarkModelUsed` is separate from the read on purpose

The console's projected-transcript provenance read
(`dshapi.sessionModelIdentity.SessionModelIdentity`) answers which model a
session's messages ran on. It stays optional and unchanged, and it has to stay
cheap and side-effect free: it is a map read that must not build an adapter and
must not consume the "last used" mark. If reading the route marked it used, a page
that was merely opened would report a model no run had ever used, which is the
opposite of provenance. The split puts the write where the fact is -- the prompt
that started a run -- and the read where the question is asked.

### 6. The host's configured adapter stays the operator's

`swappableModel` is retained, and only the settings page (the gear icon) swaps it.
The operator's save is a statement about the host, and it remains one. A
session's selection never installs anything globally again. The setter
`settingsStore.setModelAdapter` was deleted, and
`settingsStore.adapterForModel` was added so that building the configured route
yields a fresh adapter from the settings as they stand rather than the live
swappable; `consoleModels.Resolve` uses it, and `adapterFromSettings` is the same
call with the settings' own model name.

The difference is visible rather than notional, and it is narrower than "the
host's adapter is frozen": a session that chose the configured route holds the
adapter the settings had when its run was admitted, so a save made afterwards
cannot reach a run that is already answering. A session that chose **nothing**
holds no route at all -- that is what "no choice yet" means -- and follows the
host's live adapter, because following the operator's own statement about the
host is exactly what such a session asked for.

### 7. A resume rebuilds a route by name, so serve installs a resolver

A resumed run holds only the frozen route's two strings, so something has to turn
them back into an adapter. `consoleModels.Resolve` is the `zenforge.ModelResolver`
seam: it builds the adapter a provider and model name, from the settings as they
stand for the configured route and from its own fields for a declared profile, so
the adapter a resume rebuilds is the adapter its original run used.
`AdapterFor` is `Resolve` plus the catalog's membership check, which is the
relationship the two need: the picker asks whether a route may be offered and
chosen, while a caller that names a route and a model explicitly -- a checkpoint's
frozen route, a workflow script's `agent()` option -- is not the picker and must
not be gated by its list. That is also the behaviour the seam had before per-run
models, when it passed a named model straight to the provider.

The resolver the serve command already installed answers for workflow scripts, not
for the console's declared profiles, so the two had to be reconciled instead of one
replacing the other. `consoleRouteResolver` asks the console first for any provider
it declares, and falls back to the CLI's own resolver for one it does not declare at
all -- including a caller that names only a model, since an empty provider is not a
route name. That fallback is how a workflow script's `agent()` still names a
provider the operator never added to the console, which is the behaviour that
existed before routes. A provider the console does declare never falls back: its
own refusal -- a credential that is gone -- is the answer, because resolving it
somewhere else would run the caller on a different endpoint than the one it named.
The pre-existing reader in `workflow_runtime.go` (`Config.ModelResolver`) is what
the seam feeds.

### 8. A child run inherits its parent run's model

`runChildSubAgent` now prefers the parent run's own adapter
(`a.runModel(req.RunID)`) over the agent's configured one, so a session's
subagents do not regress to the host default. A child of a run that had a route
answers on the same model its parent is answering on, which is what the operator
who chose that model asked for; a child of a route-less run still uses the
configured adapter.

### 9. The deviations this decision records rather than hides

- `RunState.Meta` was chosen over `RunState.Model.Provider/Name` for the frozen
  route. The latter exists and is still checkpointed, but nothing writes it, and
  repurposing it would silently change the meaning of a field that has always been
  empty.
- `MarkModelUsed` is an extra interface method beyond the minimum. The minimum is
  the read; the write exists for the provenance projection (5), and folding it
  into the read would have made opening a page a mutation.
- `Task.Model` (a live adapter) exists beside the route, because a caller that has
  already built an adapter must not have it built twice. The honest consequence is
  that a task handing over an adapter only is **not** resumable on its model: the
  adapter cannot be frozen, so a resume of such a run falls back to the agent's
  configured adapter. A caller that needs a resume to keep the model names the
  route as well; the console names both.
- The console-aware `ModelResolver` with its CLI fallback (7) exists because the
  resolver serve already installed answers for workflow scripts, not for the
  console's declared profiles. Removing the fallback would break workflow scripts
  that name a provider the console never declared; putting the fallback ahead of
  the console's catalog would send a declared provider's request to the CLI's
  route instead of its own.
- The resolver does not apply the catalog's membership rule to a caller that names
  its route and model, and that is a deliberate departure from `AdapterFor`. The
  rule is the picker's: it decides what the model list offers and what a selection
  may record. A caller that names a model outright -- and the seam that rebuilds a
  checkpoint's frozen route -- used to reach the provider unchecked, and gating it
  on the configured route's own one-model list would narrow behaviour that has
  nothing to do with this decision. What is still enforced is that the route is one
  this host knows and that its credential resolves.
- The full suite still carries one environment-dependent failure:
  `go test ./tools/jobs/`'s PTY (`--pty`) test fails on this machine for the reason
  ADR 0137 names. It is not part of this decision, and none of the runs below
  includes it.
- ADR 0096's doc comment in `internal/dshapi/modelselection.go` described the
  host-global effect -- the adapter applied for the session being prompted. It was
  rewritten to describe the per-run read, because a comment that keeps stating a
  limitation this ADR removes is a deviation of its own.

## Consequences

- Two sessions prompted concurrently under different selections keep their own
  models, and a selection made while a run is answering cannot reach that run. The
  limitation ADR 0096 recorded is removed, and `docs/limitations.md` now carries
  the per-run statement instead: the selection is the run's own model, resolved
  once as the prompt is admitted.
- A route-less run is unchanged. A CLI run, and a console session that chose
  nothing, still run on the agent's configured adapter, and a host with that
  adapter missing still makes a no-op run rather than an error.
- A resume or fork rebuilds the model from the run's own durable Meta, so what a
  run continues on is what it started on, and a route this host can no longer
  build refuses the resume by name.
- One exception is recorded rather than smoothed over: a run started from a task
  that handed over an adapter and named no route cannot resume on that model,
  because there is nothing durable to resolve (9).
- The console keeps projecting exactly what it projected before -- the catalog,
  the `modelSelection` fold and the provenance read are unchanged in shape. What
  changed is where a run's model comes from, not what the console is told about
  it.
- The settings page is now the only writer of the host's live adapter, so "the
  host runs on this" has one author again (ADR 0102, ADR 0084).

## Verification

- `go test .` (the root package, including `modelroute_test.go`) pins the
  harness half: a route resolved once and used for every step of the run; two
  concurrent runs keeping their own adapters, with neither seeing the other's
  conversation; a host-adapter swap mid-run not reaching the run; a task that
  hands over an adapter using it without asking the resolver; an adapter-only task
  freezing no route; a fresh run freezing its route into durable Meta and a
  route-less run recording none; a resume resolving the route it was frozen on; a
  resume refused when the frozen route cannot be resolved; and `Stream` refusing a
  named route when no resolver is configured.
- `go test ./internal/dshapi/` pins the prompt path: the first turn and a
  continuation each carry the session's own adapter and route on the task; a
  session with no choice carries none and is not marked used; a recorded choice
  that cannot be built refuses the prompt and starts no run; and
  `session/selectModel` is unchanged.
- `go test ./cli/` pins the store's new shape: `ModelRoute` builds the session's
  own adapter and leaves the host's live adapter exactly as the settings page put
  it; a registered session with no choice reports no route; `MarkModelUsed`
  records the run and leaves a choice-less session alone; and a route whose
  credential is gone is refused with the credential named.
- `go vet ./...` is clean, and `gofmt` reports nothing: the two are enforced by
  `docs/format_test.go` rather than by a workflow step, which is why they are named
  here. `mkdocs build --strict` is clean with the new page, its index row and its
  navigation entry.
- Live on a real host (a scripted OpenAI-compatible endpoint on its own port,
  logging every request body at receive time; two sessions created through the
  console RPC surface, both selecting a different model, then prompted in
  `"mode":"queue"`; the host run with `--approve never` for one session selected
  `openai/gpt-4.1` and the other `acme/acme-large`; each run makes two model
  steps, a tool call then the answer, the first held open by the endpoint so the
  second session's prompt lands while the first run is mid-step):
  - BEFORE (binary sha256
    `6e18d0cf26f670424ae8ecf480d6670610e4f55a034acb46cf6707532b0c13de`, built
    from `HEAD 9268d55` with a clean tree) the request log read: seq 1 S1
    `gpt-4.1` step 1, seq 2 S2 `acme-large` step 1, seq 3 S2 `acme-large` step
    2, seq 4 **S1 `acme-large` step 2** -- the first run's own second request had
    been moved onto the other session's model while it was answering.
  - AFTER (binary sha256
    `7cd968cec640426c226c53db9c5f01533e5fa1e2e7305eaf6a38e0cb09a62150`, built
    from the tree this ADR commits with) the same harness printed the same four
    requests with seq 4 S1 `gpt-4.1` and the verdict `NO WRONG-MODEL ATTRIBUTION
    FOR S1 (models seen: ['gpt-4.1'])`: each session's two requests carried its
    own selection, and every request carried `Authorization: Bearer sk-live` to
    the same endpoint.
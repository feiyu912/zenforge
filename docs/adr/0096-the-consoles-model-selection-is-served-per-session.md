# ADR 0096: The Console's Model Selection Is Served, Per Session

Status: accepted

Superseded in part by ADR 0140: the run's model is now its own -- resolved once
per run and frozen in the run's Meta -- instead of an adapter installed on the
host's single model before each run, so the concurrent-sessions limitation this
ADR stated no longer holds.

## Context

ADR 0095 made a custom provider declarable: the Models page's "Add a custom
provider" entry point appears, the profile is stored, and the host reports whether
it can serve it. Declaring a provider is not using one. Two more links were
missing, and both are visible from the composer:

- The model picker submits the complete selection through `session/selectModel`
  (`client/ui-model-selection/src/client/directory.ts:87-99`), a method this host
  did not serve at all.
- The picker renders `current = projected.next ?? catalog.default`
  (`directory.ts:144-172`), where `projected` is the session's durable
  `modelSelection` projection. That projection arrives on the **control** stream —
  the opening baseline's `projections.<session>.values` and every later value as
  `{type:'projection', sessionId, key, value, seq}`
  (`session-controller/src/control.ts:25-31`, `:78-90`) — and this host's control
  baseline carried `projections: {}` (ADR 0083, which sent the legal minimal
  baseline because there was no projection provider). With no `modelSelection`
  key, `projected === undefined` and the picker rendered neither a chosen nor a
  configured model.
- The catalog the picker lists came only from the host's configured route, so the
  models of a declared provider could not appear in it.

## Decision

### One resolution path answers both the catalog and a selection

`session/modelCatalog` merges the route the operator configured with every
declared profile: one group per route (a declared profile's models come from the
profile, falling back to the model id for a name) and a `default`. Membership and
adapter building are answered separately, because upstream separates them too: a
provider is registered, and a request that cannot authenticate fails when it is
made. So `routableProviders` lists the routes this host has registered -- the
configured route when the settings name a model, and every declared profile whose
protocol and endpoint this host can build for -- while `failures` carries the
reason a registered route cannot actually serve (a missing credential, a profile
that will not build), beside the reason `llm/listConfigurableProviders` already
reports. Marking a credential-less route unselectable would grey out the composer
of a host that has simply not been given a key yet, which is a worse lie than
listing the route and naming what is missing. When nothing is configured,
`default` falls back to the first declared model. `session/selectModel` uses this
membership rule, and applying a selection builds the adapter through the same
function, so what the picker offers and what a run can use cannot drift.

### A selection is validated, recorded, and reported as a projection

`session/selectModel` takes `{sessionId, provider, model, reasoningEffort?}`
(`session-controller/lib/types/types.d.ts:270-276`), refuses a malformed session
id, a route it cannot route to, a model that route does not offer, and any
reasoning effort (this host exposes no effort choices for any model), and answers
`{selected}`. The picker does not read that response for the current value: it
reads the projection, so the host publishes one — `modelSelection`, the client's
`{lastUsed, next}` fold (`types.d.ts:97-104`) — in the control baseline, in a
follow snapshot for the session being followed, and live on the control stream as
each value changes. Live updates coalesce per session in the stream, so the
goroutine answering an RPC never blocks on a slow socket, and the local sequence
that orders them is honestly this host's own counter rather than a session-log
watermark: the harness records no `model/selection` event, so there is no log
position to cite.

### The selection is applied before each of that session's runs

This host has one model adapter (ADR 0084's single credential, the serve command's
one provider) and `zenforge.Task` carries no model field, so the selection is
per-session in what it records while its effect is an adapter swap. Every prompt
applies the adapter for the session being prompted — its own selection, or the
operator's configured adapter rebuilt when it has none — on the first turn and on
every continuation. A fresh session therefore never inherits the previous
session's model, and a later turn of a conversation never falls back to the
operator's default. A selection that cannot be applied (its credential is gone,
its profile was removed) refuses the prompt, and a refused prompt keeps the
pending allocation so the operator can fix the cause and retry.

### A declared profile's credential is borrowed only for the reference the card derives

The console's card stores the key the operator typed with
`credentials.set(deriveKeyRef(route), value)` (`CustomProviderCard.tsx:144-190`),
which on this host is the single model credential (ADR 0084). A declared profile
whose `apiKeyEnv` is exactly the reference that card derives for its route
therefore resolves from the host's stored credential — that is where the key
entered for this provider went. A profile naming anything else is resolved from
that environment variable alone: silently sending the operator's own provider key
to an arbitrary declared endpoint is a credential leak, not a convenience.

## Consequences

- The operator can add a provider and run on it: the picker lists the declared
  models, the selection is accepted, the projection shows it, and the run's model
  call goes to the declared endpoint with the credential the console stored.
- The picker now shows a model at all. Before this batch the missing projection
  left the composer with no current selection even for the configured route.
- An unconfigured host stays usable: the configured route is registered and
  selectable, `failures` names the missing credential, and the run that follows
  refuses the prompt with that reason rather than the composer being inert.
- A declared provider's models are listed even when it cannot be served, beside
  the reason, so the operator sees what to repair rather than an empty page.
- Two sessions running concurrently under different selections share whichever
  adapter was applied last: per-run adapters need a model field on the harness's
  task, which it does not have. `docs/limitations.md` states this plainly.
- The selection is process-local, like every other console-written setting here
  (ADR 0094), and no model-selection event is recorded in the session's log.
- `llm/discoverModels` (probing a draft endpoint for its model list) was still a
  named capability gap when this decision was made; ADR 0097 serves it.

## Alternatives Rejected

### Accept any provider and model name and record it

That is the "looks like it saved" failure ADR 0095 rejected one layer down: the
operator would learn the truth when the run failed, with no way to tell a typo
from an unsupported route.

### Store the selection without applying it, and document that runs ignore it

It would make the picker appear to work while every run used the host's adapter.
The point of the objective is a provider that is actually used.

### Apply the selection only on the first turn

Multi-turn conversations would silently drift back to the operator's default
mid-conversation, which is exactly the state the console's own projection
(`next`, falling back to `lastUsed`) exists to prevent.

### Publish a fabricated `lastUsed` or a log watermark

The fold is what tells a client whether the next request will use a new selection
or the previous one. Guessing either would misreport a state the operator is
looking at.

### Borrow the host's credential for any declared endpoint

One line shorter, and it would send a key entered for one provider to whichever
endpoint a profile names. The derived-reference rule keeps the card's flow working
without that.

## Verification

`go test ./internal/dshapi/ -run TestSessionSelectModel|TestSessionPromptApplies|TestSessionPromptRefusesWhen`
— the response shape, the effort forwarded, a host refusal surfacing the reason,
the required arguments and the exact-argument rule, the named gap without a store,
and the selection applied before the first run and on a continuation (with a
refused prompt keeping its allocation).

`go test ./internal/dshstream/ -run TestSessionControl|TestSessionFollowSnapshot`
— the control baseline carrying a session's projection with its sequence, a live
`{type:'projection', sessionId, key, value, seq}` frame, the follow snapshot
carrying the followed session's value at the snapshot cursor, and an unselected
session carrying none.

`go test ./cli/ -run TestCatalog|TestOffers|TestSelectModel|TestApply|TestDeclaredProfileCredentials`
— the catalog's groups, routable set, failures and default (including the declared
fallback and the empty host explained by name), membership refusals and successes,
a credential-less registered route being selectable while its adapter refuses to
build, the recorded selection with its live update and unsubscribe, the applied
adapter with the configured one restored for a session that chose nothing, a gone
credential refusing the apply, and the credential rule for derived versus foreign
references.

Live, against `zenforge serve` with a stand-in OpenAI-compatible endpoint on
`127.0.0.1:8899`: declaring `acme` through the card's payload reports
`"ACME_API_KEY is not set"` before the key and nothing after it;
`session/modelCatalog` lists `openai[gpt-4.1] acme[acme-large]` with both routable;
`session/selectModel` answers `{"selected":{"provider":"acme","model":"acme-large"}}`
and refuses `nope` with `model "nope" is not one provider "acme" offers`; the
control baseline reports
`{"lastUsed":null,"next":{"provider":"acme","model":"acme-large"}}` for the session
and a later selection arrives as a live projection frame; a prompt then completes —
`run.done` with output `hello from acme` — and the stand-in endpoint records
`{"model": "acme-large", "authorization": "Bearer sk-local-acme"}`.
## Amendment (2026-09-19): the key has to be registered before anyone chooses

The projection design above is right, and it was still not enough for the state
every operator starts in. The control baseline published `modelSelection` only for
sessions the selection store already had a record for, and a record appeared only
when `session/selectModel` recorded one. A session nobody had chosen a model in --
every fresh session, and the draft the composer is bound to on first load --
therefore carried no key at all.

The console reads an absent key as "capability absent": `manager.ts`
`replaceControlBaseline` seeds its per-session projection store from this
baseline, and `ui-model-selection/src/client/directory.ts:146-160` holds
`current: null, groups: [], status: 'loading'` while the key is missing. The
composer showed "Loading models…" with an empty menu **even though the catalog had
answered with every model**, which is why this hid behind the argument-shape work
for so long: the catalog was never the problem.

Three changes, all that the client's own read paths require:

- The control baseline registers `modelSelection` for **every session the manager
  lists**, unselected ones with an empty projection (`{lastUsed: null, next:
  null}`) at cut 0. An empty projection means "no choice yet", so the selector
  falls back to `projected.next ?? catalog.default` and shows the host's
  configured model instead of nothing.
- The selection store gains `RegisterSession`, and `session/create` calls it
  through the optional `sessionRegistrar` interface. A session created after the
  control stream opened gets a live projection frame the moment it exists, which
  is what seeds its store: the baseline alone cannot, because it was sent before
  the session was.
- A record that exists but holds no choice reports `next: null` rather than an
  empty provider and model, so "nothing selected" and "selected the empty string"
  cannot be confused.

The follow snapshot registers the key the same way. It is the control stream that
seeds the console's store, but upstream's follow snapshot carries a projections
block too (`history.ts:197-200`), and sending an incomplete one there would be a
second, quieter way to say the capability is absent.

Verified against a real browser, not by inspection: with the host on a scratch
port and the console loaded in headless Chrome, the composer's control reads
`Select model, current qwen-plus` (it read `Loading models…` before), and the
`session/modelCatalog` response the page receives carries both provider groups.

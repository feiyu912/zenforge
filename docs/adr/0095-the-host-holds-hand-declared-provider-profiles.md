# ADR 0095: The Host Holds Hand-Declared Provider Profiles

Status: accepted

## Context

An operator reported that the console's Models page would not let them add a
provider: "我怎么 add provider 不行呢". The page has two entry points, and both
depend on a namespace this host did not serve
(`client/ui-settings-models/src/client/ModelsSection.tsx:510-547`):

- **"Add"** adopts a route the adapter already knows. It is rendered only when the
  configurable directory is non-empty, and it is enabled only when a row is
  addable, which for this host means a shipped route.
- **"Add a custom provider"** declares a route the adapter does not know. It is
  rendered **only** when `state.namespaces.has('llm-pi-ai')`, and it is enabled
  **only** when that namespace's schema yields protocols:
  `protocolChoices(state.namespaces.get('llm-pi-ai'), schema)` navigates the
  rehydrated envelope to `['providers', '\u0000probe', 'api']` and reads a `union`
  (`client/ui-settings-models/src/client/store.ts:112-135`). The comment above the
  call states the intent: those choices are a schema read so the protocols the page
  offers cannot drift from the ones the adapter accepts.

The host served only `llm-openai` and `llm-anthropic` (ADR 0087), so the custom
entry point was hidden entirely, and the card's write -- one `set` at
`providers.<route>` in namespace `llm-pi-ai`
(`client/CustomProviderCard.tsx:144-170`) -- had no namespace to land in. Upstream's
family is the same address: `llm-pi-ai`, path `['providers', provider]`
(`packages/llm/llm-pi-ai/src/index.ts:93`, `:126-135`).

Three things were missing, and only the first is a button:

1. a namespace whose schema names the protocols this host speaks;
2. a store that holds the declared profiles;
3. an answer to "can this host actually serve this profile?", which is the
   difference between a saved row and a working route.

## Decision

### The namespace is served with a real envelope whose union is this host's protocols

`llm-pi-ai` is reported in `settings/describe` (with `writable: true`) and answered
by `settings/mutate`, `settings/update` and `settings/replace`. Its schema is a
generated schemastery envelope, like the provider and onboarding namespaces
(ADR 0087, ADR 0094), in which `providers` is a `dict` of profile objects so the
client's probe path resolves through it, and `providers.<route>.api` is a `union`
whose members are exactly the wire protocols this host's adapter factory can
build: `openai-completions` and `anthropic-messages`. The choices the page offers
are therefore the choices this host accepts, by construction rather than by
agreement.

### Shape refusals name what this host accepts

A route id must be usable both as a settings dict key and as the stem of a
credential reference (`client/store.ts:107-125` uppercases it and replaces
non-alphanumerics with `_`), so it must start with a letter and hold only letters,
digits, underscore or dash. A profile must name a protocol from that union, an
absolute http(s) `baseURL`, at least one model -- a route with no model cannot
serve a request -- and, when present, a credential reference in the wire's own
grammar. Each refusal names the value and the accepted set.

### Serviceability is decided by building the adapter, and answered on read

A declared profile is not accepted on shape alone: the store builds the adapter the
run would build (`provider.FromEnv`) and reports the reason when it cannot. The
answer is **not cached**. The card writes the profile first and the credential it
names second (`CustomProviderCard.tsx:144-190`, `deriveKeyRef` -> `credentials/set`),
so a diagnostic captured at write time would keep calling a route unconfigured
after the key arrived -- and the credential a profile names resolves to the
operator's configured key or the environment variable of that name (ADR 0084: this
host has one model credential).

### A profile this host cannot serve yet is stored with its reason

It is not refused. The order above makes a refusal wrong, and upstream's directory
already has the field for the honest answer: the entry's `error`, "a configuration
diagnostic for repair; unaffected models may remain serviceable"
(`packages/llm/llm/lib/types/types.d.ts:232-235`).

### The directory reports declared routes as declared

`llm/listConfigurableProviders` gains one entry per declared profile: its display
name (falling back to the route id), `settingsNs: llm-pi-ai`,
`settingsPath: ['providers', <route>]`, `declared: true`, and the diagnostic when
there is one. Shipped routes keep `declared` absent, and live routes are unchanged.
This supersedes in part ADR 0085, which left the field absent because no route was
declared by configuration.

### A host with no profile store does not advertise the namespace

When the serve command installs no store, `llm-pi-ai` is absent from `describe`, so
the custom entry point stays hidden and the write is refused as an unserved
namespace. Offering a namespace whose write could not be held would be the cosmetic
failure this repository refuses.

## Consequences

- The custom-provider entry point appears and is enabled, the card's save lands, and
  the profile reads back: the operator's report is answered.
- A declared route appears in the directory with its own diagnostic, and that
  diagnostic follows the credential instead of the write.
- **A declared route is not yet selectable.** `session/modelCatalog` does not list a
  profile's models and `session/selectModel` is still unserved, so the model picker
  cannot yet choose a declared provider for a run. Declaring a provider and *using*
  it are two batches; this one makes the declaration real and honestly reports how
  far it goes.
- Profiles are process-local, like every other console-written setting here
  (ADR 0094): a restart forgets them.
- Model discovery from a draft endpoint stays a named capability gap (ADR 0085).

## Alternatives Rejected

### Serve the namespace with a schema offering protocols this host cannot build

That would enable the button and store a route no run could use. The schema union is
the one place where the page's choices and the host's abilities can be made the same
list, and spending it on a cosmetic answer would make the console lie at the exact
moment an operator is asking what works.

### Refuse the write by name and document the limitation

This is the honest fallback when a capability is not there, and it was tempting
because the run path cannot select the model yet. But the declaration is genuinely
holdable, checkable and reportable, and refusing it would hide the entry point and
leave the operator with no way to see which endpoint their host would accept.

### Cache serviceability at write time

Cheaper, and wrong in the exact sequence the card performs: profile, then
credential. The diagnostic would be stale the moment the card finished.

### Refuse a profile whose credential is missing

It is the same mistake from the other side: the card's two calls are one flow, and
the first one carries no key yet.

## Verification

`go test ./internal/dshapi/ -run 'TestPiAi|TestDescribeCarriesTheProvider|TestTheProfileNamespace|TestMutateStores|TestMutateUnsetsAndUpdates|TestAnUnserviceable|TestProfileWrites|TestProfileModel'`
-- the schema walk the client itself performs (`providers.<probe>.api` is a union
whose members are exactly `hostProtocols`, with the model fields declared), the
namespace's presence and absence, the card's exact op stored and read back, unset /
update / replace, a stored unserviceable profile, the shape refusals, and numeric
model windows -- plus `go test ./cli/ -run 'TestDeclaredProfile|TestConsoleProtocols|TestProfileStoreLogs|TestDiagnosticFollows'`
for the store: declaration order, replacement in place, removal, the directory entry
it renders, the protocol mapping, and the diagnostic following a credential that
arrives later.

Live, against `zenforge serve` on `127.0.0.1:8787`: `settings/describe` reports
`llm-openai, llm-anthropic, ui-onboarding, llm-pi-ai` with `writable: true`; the
client's own `protocolChoices` walk of the returned envelope yields
`["openai-completions","anthropic-messages"]`; the card's exact `settings/mutate`
payload is accepted; `llm/listConfigurableProviders` reports
`{provider: "acme", settingsNs: "llm-pi-ai", settingsPath: ["providers","acme"], declared: true, error: "ACME_API_KEY is not set"}`;
after `credentials/set` that error is empty; the profile reads back with its model
windows; `gemini-generate` and a relative `baseURL` are refused with their reasons.
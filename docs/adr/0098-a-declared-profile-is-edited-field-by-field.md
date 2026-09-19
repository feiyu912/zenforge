# ADR 0098: A Declared Profile Is Edited Field By Field, And Nothing Is Dropped Silently

Status: accepted

## Context

ADR 0095 accepted one write path into `llm-pi-ai`: `providers.<route>` carrying a
whole profile, which is what the "Add a custom provider" card sends
(`CustomProviderCard.tsx:163`). An operator could therefore **add** a provider but
not **correct** one.

The Models page renders every non-declared directory entry through
`ProviderEditor`, whose save diffs the keys of the profile's subtree and commits
each changed key as its own op:

```ts
ops.push({ op: 'set', path: [...base, key], value: value as JsonValue })
if (!(key in after)) ops.push({ op: 'unset', path: [...base, key] })
```
(`client/ui-settings-models/src/client/ProviderEditor.tsx:123-126`, with
`base = settingsPath`, which is `['providers', '<route>']` for a declared profile.)

So saving an edit to a provider that already exists arrived as
`providers.acme.baseURL`, and this host refused it:
`settings write: path "providers.acme.baseURL" is not stored by this host;
namespace "llm-pi-ai" holds provider profiles at providers.<route>`. The refusal
was honest and the write was not lost silently, but the feature was unusable past
the first save: an endpoint typo, a display name, a corrected credential
reference, a protocol change or a model window could not be fixed.

## Decision

### Two paths are held, and a field write merges

`providers.<route>` carries a whole profile (the Add card) and
`providers.<route>.<field>` carries one field of an existing profile (the
editor). `unset` removes a route at the first path and one field at the second. A
path deeper than one field is refused by name rather than guessed at.

A field write re-encodes the **stored** profile, applies the one field, and runs
the merged result through the same decode and validation a whole-profile write
uses. The stored profile is the merge of the fields the editor did not touch, and
a write that would leave a profile this host would have refused to create -- an
unsupported protocol, an unset required field, an empty model list -- is refused
**and changes nothing**. There is no half-merged state to read back.

### The accepted field sets are the schema's own

The profile's fields are `displayName`, `apiKeyEnv`, `api`, `baseURL`, `models`
and a model row's are `id`, `name`, `contextWindow`, `maxTokens` -- exactly the
envelope this host advertises, which is also what the console rehydrates before it
renders the editor. A write naming anything else is refused by name instead of
being dropped by the typed decode, because a dropped field is a write that looks
saved. That includes a **model row** carrying a field this host does not store: a
row adopted from an endpoint that disclosed something beyond these four (an input
modality list, for instance) is refused, naming the field, rather than trimmed
quietly. `TestProviderProfileFieldsMatchTheSchema` holds both sets to the embedded
schema, so a field the console can render is never refused and one it cannot be
sent is never stored.

### A field of a route that is not there is refused, not created

Editing a field presupposes a stored profile. A field write for a route this host
holds nothing for names the path to write first (`write providers.<route> first`)
instead of creating a profile from one field, which could only be a profile that
fails validation a moment later.

## Consequences

- An operator can now add a provider **and** correct it: endpoint, display name,
  credential reference, protocol and model list each save on their own, keeping
  every field they did not touch.
- The credential is still written through `credentials/set` with the reference the
  card derives, not through the profile write, so the split ADR 0095 described is
  unchanged.
- Honest limits, recorded in `docs/limitations.md`: only one field deep is
  accepted (which is what the editor addresses), a field write cannot create a
  profile, and a model field this host does not store is refused rather than
  stored verbatim -- so an endpoint-disclosed modality list cannot be adopted into
  a profile until this host holds it.
- The refused-write invariant is now stronger than the whole-profile path: the
  model-row field check applies to **every** write into this namespace, including
  the Add card's.

## Alternatives Rejected

### Keep the whole-profile path only

It made the console's own editor permanently unable to save, and told the operator
to remove and re-add a provider to fix a typo. That is a capability gap where the
capability was assumed.

### Replace the profile with the field's value

A dict-shaped write would have wiped every other field, which is the opposite lie:
a save that succeeds and deletes the rest.

### Accept the field but store nothing for it

Silently dropping an unknown field is exactly what this repository's honesty rule
forbids: the operator would see a save, and the field would be gone on read-back
with no refusal to explain it.

### Create a profile from the first field write

A profile needs a protocol, an endpoint and at least one model; one field cannot
supply them. Refusing by name is the only honest answer.

## Verification

`go test ./internal/dshapi/ -run TestProviderProfileFieldsMatchTheSchema|TestProviderProfileFieldWritesMerge|TestProviderProfileFieldWriteRefusals`
-- the accepted field sets against the embedded envelope; a field write merging
into the stored profile (a new endpoint keeping the name, protocol, reference and
models; a replaced model list keeping a corrected window; cleared optional fields
absent from the read path the card renders); and the refusal table (a field this
host does not store, a route that is not there, a protocol it does not speak,
clearing a required field, a path deeper than one field, a model field it does not
store) with every case asserting that the stored profile is byte-for-byte what it
was.

Live, against `zenforge serve` and a stand-in OpenAI-compatible endpoint: the
whole-profile declare succeeds; the exact path that used to be refused
(`providers.acme.baseURL`) and then `displayName`, `apiKeyEnv`, `api` and
`models` each answer `ok`; the read-back shows the merged profile
(`displayName: "Acme Gateway (edited)"`, `contextWindow: 200000`); an unknown
field is refused with `field "apiVersion" is not one this host stores for provider
"acme" (it stores displayName, apiKeyEnv, api, baseURL, models)` and the read-back
is unchanged; and the merged profile still serves a run -- `run.done` with output
`hello from acme` after the endpoint records `model: acme-large` with the stored
credential.
# ADR 0084: One Credential, And It Stays Write-Only

Status: accepted

Scope: this record was written when both namespaces were planned. The batch that
landed implements the **`credentials`** namespace (`describe`, `set`, `unset`)
and its wiring, so the Models page can read credential state and store the value
an operator types. The **`settings`** document namespace (`describe`, `update`,
`replace`, `mutate`) is deliberately still unimplemented: those paths answer the
console's 404 degradation, the host's diagnostic log names them, and a card write
therefore reports a refused feature rather than appearing to save. Everything
below about the settings namespace is the decision for the batch that implements
it -- naming a model from the console still needs that batch, while setting the
endpoint's key works now.

## Context

The mounted console's settings panel does not talk to a REST form. It reads and
writes a host-owned configuration document through a **`settings`** namespace
(`describe`, `update`, `replace`, `mutate`, plus the native-open helpers) and it
carries API keys through a separate **`credentials`** namespace
(`describe({refs})`, `set({ref,value})`, `unset({ref})`), registered beside it in
upstream's `packages/api/settings-controller`.

Two properties of that contract are load-bearing upstream and must survive here:
`settings.describe` is called with `redactSecrets: true` — secrets are redacted
**at the source**, not by the caller — and no `credentials` method ever returns a
value. A secret crosses in one direction only.

This harness has no configuration document and no credential provider. It has one
model credential, held in memory by `serve`'s settings store: write-only, never
echoed, never logged (ADR 0078/0082). The panel needs an honest answer anyway.

## Decision

### Implement both namespaces beside the session methods, with an injected store

`settings` and `credentials` are unary RPCs on the surface that already exists,
so they live in `internal/dshapi` next to `session` and follow its envelope,
fence and error conventions. What backs them is injected through a setter, the
same shape as the model catalog: this package must not import `cli` (a cycle),
and with no store injected every one of these methods answers `unimplemented`
naming what is missing, rather than inventing a configuration.

### One credential backs every reference, and that is documented

The store holds one key. `credentials/describe` therefore answers
`configured`/`writable` for whichever references the panel asks about, and
`credentials/set` stores the value as that single credential regardless of the
reference it was named with — so pasting a Qwen or OpenAI key works without this
host having to model provider-scoped credentials it does not have. This is a
deliberate simplification, recorded in a code comment and in the handover, not
an accident to be discovered later.

### The value crosses one way

No method's response contains the key, and the diagnostic log added in ADR 0082
records only a namespace and a method — never arguments or a body. A test asserts
the secret appears in no response body and in no captured log line, because the
natural way to make a settings panel "work" is to echo back what it saved, and
that is exactly the mistake this ADR exists to prevent.

### Writes apply what this host can hold, and refuse the rest

`settings/update|replace|mutate` apply the provider, model and endpoint fields the
store can hold, and answer a clear method error for anything else. This host does
not persist an arbitrary settings document, so accepting a write that claims to
have stored one would be a lie; the panel showing an error is the honest failure.

### The native-open helpers answer an honest error

`settings/openSettingsDocument` and `settings/openAgentPresetDirectory` exist to
launch an editor on the host. There is no such editor here, so they answer a
method error naming that. Faking success would leave an operator waiting for a
window that will never open.

### Unimplemented namespaces still 404

A namespace this host does not serve remains a 404, so the console degrades one
panel instead of failing at boot, and the log line says which panel asked.

## Consequences

- An operator can name a provider and model and paste a key in the console's own
  panel; the credential stays in the host process, as every other part of this
  host keeps it.
- Multi-provider credential management, per-provider profiles, and a persisted
  settings document are **not** available; the panel may surface that as a
  refused write rather than a missing feature.
- The settings panel's state lives only as long as the process. Persisting it
  would mean designing a document format and a redaction guarantee first, which
  is a separate decision.

## Alternatives Rejected

### Return the stored key so the panel can display it

A secret that travels back can be captured by a proxy, a devtools session or a
log. `describe` answers whether a credential is configured; that is all a panel
needs to render its state.

### Accept every settings write and store it in a map

Then the panel would read back what it wrote and appear to work, while the run
path ignored it — a configuration that lies. Refusing unsupported fields makes
the gap visible at the moment it is created.

### Model provider-scoped credentials properly

That needs a credential provider, a reference grammar with provider metadata and
a place to persist them; this host has one key, and pretending otherwise would
produce a credential store that holds nothing.

### Keep the settings panel unsupported

Then the operator cannot configure the console from the console — the panel that
exists for that purpose would stay a dead end, and the model catalog would remain
the only configuration surface.

## Verification

`go test ./internal/dshapi/` — the `settings` and `credentials` tests (describe
reports configuration without the key, writes apply what the store can hold and
refuse the rest, the reference grammar and batch bound are enforced, `set`/`unset`
change what `describe` reports, with no store every method answers
`unimplemented`, and the key value appears in no response body and no log line),
plus the pre-existing envelope, fence, session and model-catalog tests. Also
`go test ./internal/dshmount/`, `go test ./cli/`, `go test ./... -count=1`,
`go vet ./...`, `gofmt -l`, `go test ./docs/...`.

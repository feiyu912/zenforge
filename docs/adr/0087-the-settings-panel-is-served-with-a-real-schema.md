# ADR 0087: The Settings Panel Is Served With a Real Schema

Status: accepted

## Context

The Models page does not render a form from a description it invents. It asks the
host for `settings/describe`, takes each namespace's **serialized schemastery
envelope** (`schema.toJSON()`, rehydrated client-side with `new Schema(json)` --
`packages/settings/settings/src/types.ts:36`), and resolves the probe path
`nodeAtPath(schema, ['providers', '\u0000probe', 'api'])`
(`client/ui-settings-models/src/client/store.ts:131`). Without that namespace the
page loads its directory and then has nothing to render a card from.

Hand-writing that envelope would be an imitation of a library's output, which is
how a panel ends up rendering the wrong thing. The library is available: this
repository's upstream checkout vendors `schemastery` 3.18.2 -- the same library
and version upstream builds the envelope with.

## Decision

### The envelope is generated, embedded, and pinned by a test

`settingsSchemaJSON` is the verbatim output of
`Schema.object({providers: Schema.dict(Schema.object({api: Schema.object({baseURL,
apiKey (role secret), model})}))}).toJSON()`, generated with the vendored library
and embedded as a constant. The extra `api` level is not decoration: the page
resolves it, so a profile without it renders no card at all. A test walks the
envelope (`uid` -> `refs[uid].dict.providers` -> `inner` -> `api` -> the three
fields) so an edit that breaks the probe path fails here rather than in a
browser.

### One namespace per adapter route the host can build

`llm-openai` and `llm-anthropic` are described, matching the `settingsNs` the
provider directory advertises (`ADR 0085`). `describe` reports `writable: true`
and `hasDocument: false`: this host holds its configuration in the running
process, so it has no document to point a "open the file" action at, and saying
so is what keeps that action from appearing.

### The value carries the configuration, never the credential

Each view's `value` has `providers.<route>.api.baseURL` and `.model`, and a
`secrets` entry `{path: ['providers', route, 'api', 'apiKey'], set: <bool>}`.
The credential itself is never in `value`, `user` or a reply -- the same rule the
credentials namespace follows (`ADR 0084`) -- and a store error string is redacted
before it is reported, because the error is built outside this package and is not
trusted to be free of the value it was just handed.

### Writes are flattened to the three fields this host holds, and the rest is refused

`mutate` (path ops) and `update`/`replace` (a section) both flatten into writes of
`baseURL`, `model` and `apiKey`. Anything else -- another provider's profile, a
namespace this host does not serve, a path it does not store -- is refused by
name as `unimplemented` rather than dropped, because a settings write that
appears to succeed and is discarded is a configuration that lies. A non-string
value is `gateway/bad-request` (the schema declares strings), an unknown
namespace is `gateway/bad-request`, and a mismatched `expectedRevision` is
`session/conflict`.

The host reports a constant `revision` because it has no versioned document to
number: a client that echoes what it read never conflicts, and one that sends
something else is refused rather than silently overwriting state it did not read.

### Clearing follows what the host can actually do

Clearing `apiKey` removes the stored credential and leaves an environment-supplied
one to be reported honestly (`ADR 0084`). Clearing `model` is refused: this host
always has a model, and pretending to remove it would leave the run path with a
configuration the panel believes is empty. Clearing `baseURL` restores the
provider default, which is what the store's own semantics already mean by an
empty endpoint.

### Native open answers an honest gap

`canOpenAgentPresetDirectory` answers false. `openSettingsDocument` and
`openAgentPresetDirectory` answer a named `unimplemented` capability: this host
has no native editor, and faking success would leave an operator waiting for a
window that will never appear.

## Consequences

- An operator can name a provider's endpoint, model and key in the console's own
  Models page, and the host applies them to the running process.
- The configuration lives only as long as the process: there is still no
  settings document on disk, which `hasDocument: false` states rather than
  implies. Persisting it needs a document format and a redaction guarantee, which
  is its own decision.
- Only one profile per route is modelled; a section naming a second profile is
  refused rather than partially stored.

## Alternatives Rejected

### Hand-write the schemastery envelope

It would work until the library's format or the page's probe path changed, and a
subtly wrong envelope renders a wrong form -- the failure mode this repository has
been avoiding by refusing to fake what it cannot produce.

### Persist a settings document so `hasDocument` can be true

That means choosing a file format, a merge order, a revision scheme and a
redaction guarantee for secrets at rest. Those are real decisions and none of
them is required to let an operator configure this host.

### Accept every path and keep it in a map

The page would read back what it wrote and look correct while the run path
ignored it. Refusing an unsupported path makes the boundary visible at the moment
it is crossed.

### Keep the Models page unsupported

That was the operator's original request -- configure the endpoint and the key in
the console -- and the credential half already shipped (`ADR 0084`). Stopping
there would leave the page loading a directory it can render nothing from.

## Verification

`go test ./internal/dshapi/ -run TestSettings` -- the describe shape with the
secret slot and no credential in the value, the schema's probe path walked
structurally, mutate applying endpoint/model/key, the validation table (unknown
namespace, mismatched revision, unknown op, unstoreable path, another provider's
profile, a non-string value, clearing the model, a missing ops array) with the
store proven untouched, unset clearing the credential, update and replace
applying a section, every method answering `unimplemented` with its dependency
named when no store is installed, the native-open gap, and a refused write
redacting the secret it was handed -- plus the whole-package run
`go test ./internal/dshapi/`.

# ADR 0085: The Provider Directory Is Served, Discovery Is Not

Status: accepted

Superseded in part by ADR 0095: a route configuration declares is now held and
reported with its `declared` flag and its diagnostic.

## Context

The console's Models page loads its provider directory before it renders any
card. Without it the page does not degrade to a smaller feature: it reports
"Loading the provider directory failed: client api: llm/listProviders failed:
transport failure for /api/llm/listProviders: HTTP 404" and shows nothing at
all. An operator watching that page concluded, reasonably, that the console was
broken -- the message names the endpoint, which is exactly the evidence this
repository's unserved-endpoint log was added to produce.

Upstream declares three methods on the namespace
(`packages/llm/llm/lib/typert.remote-client.d.ts:10-12`) and describes the two
directory halves as complementary: `listProviders()` reports routes that are
registered, `listConfigurableProviders()` reports every route an adapter can
activate through configuration, and a configuration surface merges them so a
provider appears alongside its live or dormant state.

## Decision

### Serve both directory halves from an injected source

`llm/listProviders` and `llm/listConfigurableProviders` are answered from an
`LlmDirectorySource`, injected exactly like the model catalog and the credential
face, because this package cannot import the serve command. The live half is the
route the host is configured to serve; the configurable half is every route the
host's own adapter factory accepts, which is the honest set: this host can serve
`openai` and `anthropic`, and naming a route it cannot build would be a promise
the run path cannot keep.

### Never answer null for an array

Both wire types are readonly arrays. A nil slice marshals to `null`, which fails
the page's decode where an empty array renders an empty directory, so both
answers normalize to `[]`.

### A route the host does not recognise keeps its own key as its name

The display name is a nicety for a selector, so the two known routes get their
proper names and anything else keeps its key. Inventing a label for an unknown
route would be prettier and less true.

### `discoverModels` is registered and answers a named gap

The method interrogates a provider endpoint a card is still editing, sending the
draft endpoint and credential directly. This host can reach an OpenAI-compatible
endpoint and does not do it yet. Registering the method and answering an
`unimplemented` error that names "model discovery" gives the page a capability
message, while a 404 would read as a broken install. Implementing it means
deciding how a draft credential is handled and bounded (network policy, timeouts,
what is logged), which is its own decision and is not smuggled in here.

### `declared` stays absent

Upstream's optional `declared` flag means "the adapter knows this route only
because configuration declared it". Only the adapter can answer that, and this
host did not write the adapters, so the field is omitted rather than guessed. (A
route the operator declares themselves is a different question, and ADR 0095
answers it: that route is declared by construction, and the field says so.)

## Consequences

- The Models page renders its directory and can show the host's configured route
  as live.
- The card write path still needs the `settings` document namespace, whose page
  reads a serialized schema (`schema.rehydrate(namespace.schema)` and a lookup at
  `['providers', route, 'api']`) -- so a card save is still refused, and the
  provider directory alone does not make a model nameable from the console.
- Model discovery from the console is a named gap rather than a silent one.

## Alternatives Rejected

### Answer the namespace with 404 until all three methods exist

That is what produced the operator's report. A partial host should degrade one
feature, and the directory is a feature the host can serve truthfully.

### Implement discovery now by probing the draft endpoint

It is the natural next step, but it introduces outbound requests driven by a
request body's URL and a credential the console supplies, so it needs its own
decision about policy, bounds and what may be logged. Doing it inside this batch
would have hidden those choices in a feature commit.

### Report a fabricated `declared`

The page uses it to tell a user-added route from a shipped one. Guessing would
mislabel exactly the routes an operator is trying to diagnose.

## Verification

`go test ./internal/dshapi/ -run TestLlm` -- the live directory shape, both
arrays never null, the configurable shape with an untouched declared path, the
exact-arguments rule, the missing source answering `unimplemented` with its
dependency named, and discovery answering its named capability gap -- plus the
pre-existing envelope, fence, session, catalog and credential tests, and the
whole-package run `go test ./internal/dshapi/`.

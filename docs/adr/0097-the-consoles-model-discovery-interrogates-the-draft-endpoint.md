# ADR 0097: The Console's Model Discovery Interrogates The Endpoint An Operator Is Editing

Status: accepted

## Context

The Models page's editor calls `llm/discoverModels` twice
(`client/ui-settings-models/src/client/ModelListEditor.tsx:153` and `:230`). On
mount for a card that is **not** declared it inherits the adapter's own model
catalog with no endpoint named
(`ProviderEditor.tsx:469` passes `catalogProvider` only when `declared !== true`),
and its fetch button sends the draft a user is still editing -- endpoint,
protocol and credential -- because a provider being added has no route to name.

ADR 0085 registered the method and answered a named `unimplemented` capability
gap, on the grounds that serving it means deciding how a draft credential is
handled and bounded. That decision was not made, so the fetch button could only
report a capability error and the editor could not help an operator fill a model
list at all.

## Decision

### The method is served with the upstream signature and the upstream error

`llm/discoverModels(settingsNs, request)` takes the settings namespace whose draft
is being interrogated plus `{provider?, baseURL?, api?, apiKey?}`
(`packages/llm/llm/lib/types/types.d.ts:243-261`). The namespace is required and
is used to know which protocol a draft speaks when it does not name one itself
(`llm-pi-ai` and `llm-openai` are OpenAI-compatible, `llm-anthropic` is
Anthropic Messages; an unknown namespace without `request.api` refuses by name).
An unknown or misspelled request field is refused rather than ignored, because a
draft that said `baseUrl` would otherwise be interrogated with no endpoint and
refuse for the wrong reason. Every outcome of an interrogation itself is the one
error upstream declares for this method, `llm/model-discovery-rejected`, carrying
`{settingsNs, baseURL?}` (`types.d.ts:263-270`) -- and never the credential.

### No endpoint named means the host answers from its own registry

The model catalog this host already publishes is its registry, and it is the
better answer because it costs no network call: a route that appears in it is
answered with the models the console shows for it, which is exactly what
`catalogProvider` inheritance wants. A route the host can build an adapter for but
publishes no model list for -- an unconfigured shipped route -- answers an **empty
catalog**, which is the truth and lets the operator add models by hand; an
unknown route refuses by name, telling the operator to pass `baseURL` or declare
the provider first. A failure never masquerades as an empty catalog.

### A named endpoint is interrogated once, bounded, with the credential in a header

An interrogation is `GET {baseURL}/models`, which is where both protocols this
host speaks publish a listing under the API root the operator configured. The
draft credential is used for that one request in the header its protocol uses
(`Authorization: Bearer` for OpenAI-compatible, `x-api-key` plus
`anthropic-version: 2023-06-01` for Anthropic), and it is never stored, never
logged, and redacted from any diagnostic that quotes a transport error. The
request is bounded four ways: only `http`/`https` is accepted, a URL carrying
credentials is refused (the key has a place to go), the response is read to at
most 1 MiB, and the whole call is bounded by a 20 second timeout on top of the
request's own context, so a slow endpoint cannot outlive the operator's patience
or their browser.

### Two listing shapes, and only fields an endpoint actually discloses

OpenAI-compatible endpoints publish `{"data": [...]}` and Ollama publishes
`{"models": [...]}`; both are read, and a response with neither refuses by name
rather than reporting an empty catalog for an endpoint that answered something
else. An id comes from `id`, then the tag a gateway repeats as `model`, then
Ollama's `name`; a display name comes from `name` or `display_name` and is omitted
when it merely repeats the id; capacities come from `context_window` /
`context_length` and `max_output_tokens` / `max_tokens` **when disclosed**, as
pointers, so an undisclosed capacity is omitted rather than reported as zero. An
entry with an explicitly empty id is skipped and duplicates are dropped, because a
card cannot adopt an entry it cannot name.

## Consequences

- The fetch button works: an operator adding a custom provider points it at an
  endpoint and gets the model list, with capacities where the endpoint discloses
  them, and adopts them into the draft.
- A card for the route this host is configured with inherits its model, so the
  editor opens populated rather than empty.
- The refusals are actionable: a wrong scheme, a URL with credentials in it, a
  protocol this host does not speak, a namespace that names no protocol, a
  non-2xx answer (with its status), an answer that is not JSON, a JSON object with
  no list, and an unknown route each say which part failed.
- Honest limits: only the two protocols this host speaks are interrogated; there
  is no pagination, so an endpoint with more models than 1 MiB of listing is
  reported as far as it was read rather than followed; capacities appear only
  under the spellings listed above, and nothing is inferred from an unknown
  field; and this host still ships no built-in model registry for its own
  provider routes, which is why an unconfigured route answers an empty catalog.
- A discovered model is not a validated one: discovery reports what the endpoint
  says about itself, and the card decides what to adopt.

## Alternatives Rejected

### Keep the named capability gap

ADR 0085 left it open because the credential decision had not been made. The
decision is above, it is bounded, and leaving a console button permanently
inoperative is not honesty -- it is an unfinished feature that reports itself as
a limitation.

### Fabricate a model registry for the shipped routes

Listing a plausible set of OpenAI or Anthropic models would be this repository's
kind of lie: a model the host never verified would appear selectable. An empty
catalog plus a working interrogation is the truthful pair.

### Guess capacities from unknown fields, or default them to zero

A zero context window is a wrong fact, not a missing one. Undisclosed stays
undisclosed (pointers, omitted on the wire).

### Store the draft credential while discovering

The credential belongs to one interrogation of an endpoint the operator has not
saved yet. Storing it would make "I typed a key to test" a configuration write;
the card stores it separately when the profile is saved.

### Reach any scheme or follow redirects to one

`http`/`https` only, and credentials in the URL are refused. A console on
loopback is the operator's own, but a URL is data the page supplied, and the
narrower rule costs nothing.

## Verification

`go test ./internal/dshapi/ -run TestLlm` -- the host-registry answers (a declared
route's models with a name omitted when it repeats the id, an empty catalog for a
registered route with no list, a refusal naming an unknown route and a request
with nothing to interrogate), the draft interrogation (path under the configured
base URL, `Authorization: Bearer` header, `accept`, disclosed context window and
output limit, an entry with an explicitly empty id skipped, a duplicate dropped),
the other shapes (`x-api-key` and `anthropic-version` for Anthropic, `display_name`
as a name, Ollama's `models` with `name` and `model` as ids), every named failure
(HTTP 401, an HTML answer, an object with no list, a non-http scheme, credentials
in the URL, an unsupported protocol, a namespace naming no protocol -- with the
credential absent from every message), and the argument rules (a missing
namespace, a missing or non-object request, a misspelled request field, a
non-string request field, an unexpected top-level argument).

Live, against `zenforge serve` with a stand-in OpenAI-compatible endpoint whose
listing discloses a context window and an output limit: the fetch call
`{baseURL: http://127.0.0.1:8899/v1, api: openai-completions, apiKey: sk-draft}`
answers `[{"id":"acme-large","name":"Acme Large","contextWindow":128000},{"id":"acme-mini","name":"Acme Mini","maxTokens":4096}]`
and the endpoint records `Bearer sk-draft`; the configured route inherits
`[{"id":"qwen-plus"}]` with no network call; a declared profile is answered
`[{"id":"acme-large","name":"Acme Large"}]` with none either; and the four
refusals come back as `llm/model-discovery-rejected` with their reasons
(`knows no route "nope"`, nothing to interrogate, `protocol "gemini-generate" is
not one this host speaks`, and `the endpoint answered HTTP 404`).
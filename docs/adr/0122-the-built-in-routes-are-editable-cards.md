# ADR 0122: The Built-in Routes Are Editable Cards

Status: accepted

## Context

The Models page could not edit the routes this host is built to serve. The console
chooses a provider's editor by the **name** of the settings namespace the directory
entry points at: it knows `llm-deepseek` and `llm-pi-ai`, and reports anything else
as `unknown`, which renders a card with no fields and a disabled save
(`ui-settings-models/src/client.js`, the namespace-to-editor mapping). This host
advertised OpenAI and Anthropic under namespaces of its own invention
(`llm-openai`, `llm-anthropic`), so both rendered as uneditable hints — while a
hand-declared route, which already pointed at `llm-pi-ai`, edited correctly.

The audit also found a second consequence of the same directory: the models the
catalog offered came from the host's own configuration, so an edit that the page
*had* accepted would not have appeared in the picker.

## Decision

- **Every configurable route is a pi-ai card.** The two built-in routes are
  advertised with `settingsNs: "llm-pi-ai"` and
  `settingsPath: ["providers", <route>]`, the same address a hand-declared route
  carries, so the page renders the real editor for them.
- **They are seeded with the host's own configuration.** A store starts with one
  profile per built-in route: the display name, the wire protocol from the pi-ai
  schema's own union, and the live route's configured base URL and model. A card
  therefore opens on the endpoint the operator actually runs, not on an empty form.
- **A route the host is not running on names its own credential variable**
  (`OPENAI_API_KEY` / `ANTHROPIC_API_KEY`), while the live route keeps an empty
  reference, which is how a profile says "the host's own credential". An empty
  reference on a foreign route would let it borrow a key for a different service —
  the rule the console's own reference derivation exists to enforce.
- **The live route is one group in the catalog.** Since the live route is also a
  seeded profile, the picker takes that profile's models as the live group's models
  and never adds a second group for the same route.

## Consequences

- Pinned by `TestBuiltinRoutesAreEditablePiAiCards`: every listed route carries a
  pi-ai settings address at `providers.<route>`; the live route's card holds the
  configured base URL and model with no credential reference; the other built-in
  names its own variable and the `anthropic-messages` protocol.
- The settings document now names `apiKeyEnv` for a built-in route the host is not
  running on. That is a reference, not a secret: the document still never carries
  the credential value, which `TestConsoleSettingsDocumentLeavesAFieldItNeverMovedAlone`
  now checks by looking for the value field rather than the substring `apiKey`.
- An edit to a built-in card is stored through the same profile path as a declared
  route, and its models are what the picker lists for that route.
- Still missing, and named as such: a built-in profile is advertised as `declared`,
  which upstream reserves for a route the operator wrote; and the deepseek
  namespace (`llm-deepseek`) is not served at all, so a console built around that
  route would still show it as unknown.
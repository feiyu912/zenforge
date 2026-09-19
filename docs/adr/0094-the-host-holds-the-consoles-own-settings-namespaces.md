# ADR 0094: The Host Holds the Console's Own Settings Namespaces

Status: accepted

## Context

The console opens on an "Internal Testing Notice" whose Continue button writes the
acknowledged notice version into settings namespace `ui-onboarding`, field
`welcomeNoticeVersion`
(`client/ui-settings-models/src/onboarding-copy.ts:1-14`,
`client/welcome-store.ts:84`). The settings scope decides where that write goes
from the transport: a loopback page persists on the host, a remote page keeps it in
process memory (`client/ui-settings/src/client/index.ts:58`).

This host served only the provider namespaces (`llm-openai`, `llm-anthropic`), so
a browser on `127.0.0.1` -- correctly told it was on loopback -- sent
`settings/mutate` for `ui-onboarding` and had it refused by name. The notice could
therefore only fail, and it said so: "The acknowledgement could not be saved."

## Decision

### The host declares the console-owned namespaces it holds

`ui-onboarding` is reported in `settings/describe` beside the provider namespaces
and answered by `settings/mutate`, `settings/update` and `settings/replace`. It
holds one declared, optional string field, `welcomeNoticeVersion`.

### The schema is a real schemastery envelope

The console validates a namespace's section against the envelope the host sends and
treats one it cannot rehydrate as vouching for no section
(`client/ui-settings/src/client/settings-scope.ts:204-213`). The envelope is
therefore generated with the pinned schemastery the console rehydrates with, like
the provider schema, and the field is optional so an unacknowledged namespace
validates as an empty section.

### A write this host cannot hold is refused by name

An undeclared path, an undeclared section key, a non-string value, an unknown op
and a namespace the host does not serve are all refused with the reason. A write
that appears to land and is dropped is worse than one that reports it was not
accepted -- and it is the failure mode this ADR exists to remove, not to
reintroduce one namespace over.

### It lives where the rest of the console-written settings live

The store holds it in this process, which is what this host's
`hasDocument: false` already means for the endpoint, model and key the Models page
writes. The acknowledgement therefore survives reloads within a running host but
not a host restart, after which the notice appears again.

## Consequences

- Continue works: the notice's write lands, and the read path it decides from sees
  the same value, so the notice does not reappear on the next reload.
- A host restart shows the notice again. Making it durable means giving this host a
  durable user settings document, which would also change what the Models page
  writes into -- a larger decision this ADR does not take.
- Console-owned facts never mix with host configuration: the provider schema is
  unchanged, and the onboarding namespace carries no secret slot.

## Alternatives Rejected

### Answer the write without storing it

Continue would appear to work and the notice would return on the next reload. That
is the same broken promise with a slower feedback loop.

### Put `welcomeNoticeVersion` in the provider profile

It is a fact about the GUI, not about the endpoint. Mixing it in would show it to
the Models page and make the provider schema depend on the console's versioning.

### Tell the client it is not on loopback so it keeps settings in memory

It is on loopback, and saying otherwise would also take the host persistence away
from the Models page, which does work.

## Verification

`go test ./internal/dshapi/ -run TestSettings` -- the namespace present in
`describe` with its envelope and an empty section, the acknowledgement stored and
returned by `mutate`, the stored value visible on the read path the notice decides
from, `unset`, `update` and `replace`, the refusal table (undeclared path, nested
path, non-string value, unknown op, undeclared section key, unserved namespace)
with the store proven untouched, and the provider write path still landing.
`go test ./cli/ -run TestSettingsStore|TestConsoleSettings` -- the store's
round-trip, the copy handed out so a caller cannot reach into the store, an empty
section read back like one never written, an unrelated namespace staying empty, and
the adapter passing the namespace through.

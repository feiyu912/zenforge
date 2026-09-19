# ADR 0103: The User Layer Is What The Console Wrote, And A Chosen Model Persists

Status: accepted

Amends [ADR 0102](0102-the-consoles-settings-and-credential-persist-in-a-host-owned-file.md)
(its naming rule was "the live state moved off the startup seed"; the rule here is "the
console wrote it"). It does not add a remote method: `settings/describe` and
`session/selectModel` were already the console's own calls.

## Context

ADR 0102 gave the console a durable document, and the first host to run it showed what
the document was still getting wrong. The operator at `127.0.0.1:8787` -- a host started
with `--base-url https://dashscope.aliyuncs.com/compatible-mode/v1 --model qwen-plus`,
whose real setup is the declared `qwen` profile pointing at `deepseek-v4.1-flash` --
reported three things in one round:

1. The model they picked in the composer was gone after a restart, and the composer fell
   back to `qwen-plus`.
2. The Models page's OpenAI card showed `qwen-plus` and the dashscope endpoint as a saved
   configuration, which they had never entered on that card.
3. The document on disk did not record their endpoint or their model at all.

Each has a different cause, and they share one: the host could not tell a value it had
been *started with* from a value the console had *written*, and the model selection was
not durable at all.

- Complaint 2 came from the read path. `settingsViewFor` built the namespace's `user`
  layer out of the live `SettingsProfile`, so the flags `serve` was launched with were
  reported as the operator's saved settings. The page then showed a configuration -- and
  offered to save over one -- that nobody had entered. The write path the same day had
  made this worse in the other direction: a page whose user layer is empty after an
  accepted write looks like a lost write
  (`client/ui-settings-models/src/client/ProviderEditor.tsx` reads `namespace.user` and
  then `written.view.user`), so the two halves of the console disagreed about what "saved"
  means.
- Complaint 3 came from the write path. ADR 0102 recorded a field only where the live
  value *differed from the startup seed*, a rule introduced to stop a model-only write
  from recording an empty credential and wiping a key supplied with `--api-key`. It is the
  right instinct -- silence is not a statement -- but it tested the wrong thing. An
  operator who typed the endpoint and the model the host was already using had written
  them, and the file recorded neither, because the values happened to match.
- Complaint 1 had no cause in the document at all: `consoleModelSelection` held each
  session's choice in memory, so a restart lost it while the document claimed the
  console's state was durable.

## Decision

### The user layer is the console's own writes, and it is absent when there are none

`SettingsDocumentStore` gains an optional face, `SettingsUserLayer`, whose
`SettingsUserSection(route)` reports the section the console wrote for one provider. A
view reports that section as the namespace's `user` layer, and reports *no* layer when the
store says nothing was written. The resolved `value` keeps reporting the live
configuration, flags included, because that is what a run would use.

A store that does not implement the face keeps the older behaviour of restating the live
configuration as the user layer. That is not a compatibility shim for its own sake: the
face is the serve command's document, and an injected store that has no way to tell the
two apart should not pretend it does.

### A field belongs to the card it was written through

The endpoint and model fields are written per provider namespace (`llm-openai`,
`llm-anthropic`), and the route travels with the write: `SetSettingsEndpoint(route, ...)`
and `SetSettingsModel(route, ...)`. The store records the route beside the field
(`spokenRoutes`), the document carries it (`consoleRoutes`), and the user layer reports a
field on that route's card only. A field with no recorded route -- a document written
before routes were recorded, or a caller with no namespace -- is attributed to the
provider the host is configured with.

Without this, "the console wrote the model" would put the value on whichever card the host
happens to be configured with, which is complaint 2 in a new costume.

### The document records what the console wrote, not what moved off the seed

A settings field is written to the document when the console wrote it, and left out when
nobody did. The test is the write, not the comparison: `markSpoken` is called by the
mutator that commits the field, next to setting it, so the endpoint and the model an
operator typed are recorded even when they equal the flags the host was started with.

The rule that ADR 0102 protected still holds, and is now enforced more directly: a field
no request wrote is never named, so a write that touched only the model still cannot
record an empty credential over a `--api-key` seed. A loaded document marks the fields it
names as spoken, so a restored field stays recorded on the next write of the document
rather than being mistaken for a flag delta and dropped.

Naming is also what puts a field back on the page: a value written in the console is
reported as `user` from then on, which is what makes the editor's read-back after an
accepted write show the accepted value.

### A session's chosen model is console-written state

`session/selectModel` was already durable in intent and process-local in fact. The choice
now goes into the same document (`modelSelections`, keyed by session id) and is adopted at
startup, before the document is read, so the store that restores it exists when the file
is loaded. The write follows the document's commit rule: the record is committed, the
document is written, and a document that will not take it puts the record back and reports
the failure -- a choice that is live but not durable is exactly the disagreement the
document exists to remove.

Two bounds keep the file small and honest. Only sessions whose model an operator actually
chose are recorded, so a session that never chose one is absent rather than stored as an
empty choice; and at most the 64 most recent choices are kept, because a map keyed by
every session the host has ever served would grow the file without limit. The runtime
last-used hint is not recorded: it changes on every run, and rewriting a credential-bearing
file for a label is not a trade worth making.

### The added fields do not bump the document version

`consoleRoutes` and `modelSelections` are new optional fields in the version-1 document. A
document written by this checkout before them -- including the one the operator's host has
on disk -- loads unchanged, and its settings fields are attributed to the configured
provider. Additive fields that a reader either uses or ignores do not change the meaning
of what was already there; a reader that refuses unknown fields refuses the newer file
rather than misreading it, which is the direction the format already prefers.

## Consequences

- Complaint 2 is gone: a host started with `--model qwen-plus` reports no user layer on any
  provider card until the console writes one, and the card shows only what was written
  there. The resolved value still reports `qwen-plus`, so the composer's default and the
  catalog are unchanged.
- Complaint 3 is gone: the endpoint and the model an operator saves are in the file, and
  the fields the file names are the fields the page shows back. The file is smaller than
  a whole-configuration dump and larger than a seed delta by exactly the console's own
  writes.
- Complaint 1 is gone for the sessions that chose a model. A session that never chose one
  still falls back to the configured model, which is what the composer expects
  (`projected.next ?? catalog.default`).
- `docs/limitations.md` no longer says a per-session model selection is process-local; it
  says instead that the selection is bounded to the most recent 64 sessions and that the
  last-used hint is not persisted.
- A document edited by hand while the host runs is still overwritten by the next commit
  (ADR 0102), and the hand-added `consoleRoutes`/`modelSelections` fields are ordinary
  parts of that snapshot.
- The credential keeps every rule ADR 0084 gave it. It is not part of the user layer -- it
  travels as a secret -- so it is marked with no route and never appears in a provider
  card's section.

## Alternatives Rejected

### Fix the seed comparison instead of tracking the write

Comparing against the seed could be narrowed (say, "record when it differs, unless a
loaded document already named it"), but it cannot express the operator's actual gesture.
Typing the value a flag already carries is a write, and a rule that reads the value cannot
see it. The write is where the fact exists.

### Report the console's fields under every route, or under the configured one

Reporting under the configured route is the wrong card for the operator who edited the
other one, and reporting under every route repeats the same value on cards it was never
saved on. The namespace a write arrived through is the only correct attribution, and it is
already in the request.

### Derive the user layer from the document without a store face

The view could read the document's fields directly, but the document belongs to the serve
command (ADR 0099), and `internal/dshapi` cannot know what a host chose to persist. The
optional face keeps the boundary where it is: the handler asks, the store answers.

### Persist the model selection as a run/event

The event log is durable state for runs and is readable through the session routes; the
selection is console state about a session, it belongs with the rest of the console's
settings, and it has to be restored before the first projection is served.

### Record every session the host has served

The composer's projection baseline is built from the sessions the console asks about, and
`RegisterSession` already exists for that. Persisting a record for a session that never
chose a model would grow the document with rows that mean "nothing" -- the same silence-is-
not-a-statement distinction the settings fields keep.

## Verification

`go test ./cli/ -run TestConsoleUserLayer` -- a host started with an endpoint and a model
reports no user layer on any card; writing only the model reports and records only the
model, with the endpoint absent from the file; writing the values the flags already carry
records them and they survive a restart; and a model written through the anthropic card is
reported on that card, on `llm-anthropic` only, before and after a restart.

`go test ./cli/ -run TestConsoleModelSelection` -- a chosen model is in the document, is
projected for that session after a restart, a session that never chose one is not written,
and a document that cannot be written puts the record back and reports the failure.

`go test ./cli/ -run TestConsoleSettingsDocument` -- the ADR 0102 round trip still holds
under the new naming rule: the credential survives, a field nobody wrote stays out so a
`--api-key` seed keeps its key, a cleared override stays cleared, and the key appears in
exactly one file.

`go test ./internal/dshapi/ -run TestSettings` -- a store that implements the user-layer
face is believed: its section is the `user` layer, `value` still carries the live
configuration, a route it says nothing about has no layer at all, and a store without the
face keeps restating the live configuration.

Live, against the operator's host, once it is restarted onto this checkout: the Models
page's OpenAI card is empty until something is written there, a model picked in the
composer is still the projection after a restart, and
`~/.config/zenforge/console-settings.json` names the endpoint and model that were saved,
with `consoleRoutes` recording the card they were saved on. The host serves the repository
itself as its workspace, so this is also the check that the document stays outside the
directory the page can browse (ADR 0102).
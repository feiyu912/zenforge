# ADR 0123: The Prompt Cards the Console Renders

Status: accepted

## Context

The console's chat flow shows two cards above an answer: the **system prompt** the
request was built with, and the **request header** (provider, model, sampling) that
opened the attempt. Neither was served.

- `system/message` is one of the four surface-eligible types this host already
  declares, and it was never emitted. The console reads the loaded `system/message`
  nodes in surface order and renders the last non-empty one
  (`ui-conversation/src/client/contract/system-prompt.ts`, `inspectSystemPrompt`),
  which is why the prompt card stayed empty.
- `request/header` was never emitted, so the request card had no anchor: the
  console's `requestPromptDefinition` matches it by type and expects
  `data.header` to be an object carrying the request's configuration.

The assembled prompt was not on the log at all. It exists as **messages** in the
run's checkpoint -- and the checkpoint is written precisely so a resume replays the
same prompting -- but a projection reads events, so the text was unreachable from
the console's side of the wire. Guessing it (or rendering a card from the flags the
host happens to hold) would show the operator a prompt that may not be the one the
model received.

## Decision

- **The assembled prompt becomes a durable event.** At the step that first sends it,
  a run writes `system.prompt` with the step number and the assembled sections. It is
  this host's own durable record: the wire type it projects to is the console's
  `system/message`, which is the pair's real vocabulary.
- **One `system/message` per section**, appended at the step the harness actually
  assembled them for, with `surfaceOp: "append"` and the section text as a single
  text block. The console's own update rule (an append after a non-empty node is an
  update) then holds without this host inventing a flag.
- **`request/header` at the attempt that opens the request**, carrying
  `{header: {config: {provider, model}}}`, the reason, and the step. The payload obeys
  the console's validator: it **omits `header.system`** (the console rejects a header
  that carries it -- "must omit header.system; use system/message") and omits `tools`
  and `adapterDefaults` when empty. The provider and model are the run's identity,
  which is the route the host actually ran on.
- **One header per route change, not one per step.** Upstream logs a header only when
  the model-visible request differs from the previous one (or a new series starts), so
  a second step on the same route mints no second card.

## Consequences

- Pinned by `TestProjectionServesTheSystemPromptAndTheRequestHeader`: one
  `system/message` per section in order, `surfaceOp: "append"`, turn and step
  stamped, and a header whose payload has no `system` key, no empty `tools`, and the
  run's provider/model with reason `initial`.
- Counted: a prompted turn is now twelve records (the eleven the console's transcript
  is made of, plus the request header its attempt opens). The session tests were
  updated with that number, and `startedAfterSeq` for an open attempt is now the
  header's sequence.
- Live check: one real qwen-plus turn served
  `turn/start, user/message, session/title, session/title, step/start, system/message,
  system/message, request/header, assistant/message, step/end, turn/end`, and the
  console bundle's own `validateSessionEventData` accepted all eleven records.
- Still missing, and named as such: `request/context` (provider/model/context window
  when they change) is **not** emitted. The shipped console has no consumer for it --
  it appears only in the bundle's event vocabulary -- and this host does not record a
  per-request context window, so emitting it would either be a no-op or an invention.
  When the context meter is wanted, that is the place to start: the durable data has
  to exist first.
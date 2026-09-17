# ADR 0029: Ordered Prompt Sections With Strict Variable Interpolation

Status: accepted

## Context

ZenForge assembled its system-message prefix with a hand-written if-chain
(`systemPrefixMessages`): configured instructions, then the frozen
environment snapshot, then discovered project instructions, then the
skill catalog. DSH instead exposes a system-prompt registry: uniquely
named sections with allocated order slots, a `complete` section that
suppresses all others, runtime *contexts* rendered as a separate
superseding snapshot, and strict `{{variable}}` interpolation of section
text where a malformed or unknown reference throws during assembly
rather than rendering empty.

The ad-hoc chain has two problems the reference design fixes. First,
there is no way for a host to place deployment-specific guidance (a
persona, an operator policy) at a defined position relative to
first-party guidance, and no stable slot table to reason about. Second,
there is no way to template that guidance safely: a hand-rolled
substitution would either leave an unknown `{{var}}` in the prompt or
silently delete it, both of which produce a prompt the host did not
author.

## Decision

`prompt/` implements the DSH registry contract: `Section` and `Context`
records with names, orders, optional interpolation, and the
`complete` rule; duplicate names rejected at registration; sections
sorted by order with a name tiebreak; empty sections dropped; and
`Render` joining with blank lines. The DSH `SECTION_ORDERS` slots are
ported as constants (`OrderHarnessIdentity` -1000 through
`OrderPersonaSuffix` 10200), along with the context slots and the
`Current runtime context. This snapshot supersedes earlier
runtime-context snapshots.` preamble.

Interpolation is a direct port of DSH's `interpolate` step:
`{{name}}` groups are matched at the position (`^\{\{([^{}]*)\}\}`),
names must match `^[a-z][a-z0-9_]*$`, unknown names fail and list what
is registered, a lone `{{` with no later `}}` is literal prose, and
substituted values are never rescanned. A Go map cannot distinguish a
registered empty value from an absent key, so both substitute-empty and
unknown-value behavior collapse into: registered keys substitute
(possibly empty), absent keys fail.

The agent assembles six sections in DSH order: the persona prefix, the
configured instructions, the frozen environment snapshot, the frozen
project instructions, the skill catalog, and the persona suffix.
Persona sections interpolate strictly; discovered content does not,
because a user-authored `AGENTS.md` containing `{{` must not fail a
run. Built-in variables are `workspace` (the working directory) and
`platform` (`GOOS/GOARCH`), overridable by `Config.PromptVariables`.

`validatePrompt` runs at the top of both model-call boundaries, before
compaction or any request, so an unknown or malformed reference fails
the run with `assemble system prompt: …` and no model call happens.

Two deliberate deviations from DSH:

1. **Per-section system messages.** DSH renders one joined prompt
   string. ZenForge keeps one system message per section because the
   message list is the durable checkpoint surface and the Anthropic
   adapter already folds system messages into one provider block;
   joining here would change nothing the model sees but would rewrite
   the resume representation.
2. **Selective interpolation.** DSH interpolates every registered
   section; ZenForge interpolates only sections whose host opted in
   (the persona slots), so files discovered from disk stay verbatim.

Runtime contexts are available through `RenderContext` and
`ContextSections` but the live runtime-context injection path from DSH
(durable user-role snapshots replacing earlier ones) is not adopted
here: ZenForge's environment re-injection already covers the
diff-only case (ADR 0027) and a second, competing snapshot channel
would need its own supersede semantics.

## Consequences

Benefits:

- deployment guidance gets a stable, documented position relative to
  first-party sections instead of relying on call order in one
  function;
- a template typo fails a run loudly at the next model boundary, naming
  the variable and listing what is registered, rather than shipping a
  prompt with an unresolved placeholder;
- the registry is a plain library type, so hosts can assemble and test
  prompt sections without running an agent;
- existing prompt order is unchanged when no persona is configured.

Costs:

- two prompt-discipline rules now coexist (interpolated persona
  sections, verbatim discovered content) which must be documented for
  hosts;
- `validatePrompt` renders the prefix an extra time per model boundary
  (the sections are short, so this is a bounded cost);
- prompt-shape tests that inspect checkpoint messages must inspect the
  model request instead, because the prefix is applied at request time.

## Alternatives Rejected

### Interpolating Every Section

Applying strict interpolation to discovered instruction files would turn
any `{{` in a user's `AGENTS.md` into a run failure. DSH controls its
own section text; ZenForge does not control what is on disk.

### Substituting Unknown Variables With Empty Text

That is the failure mode the reference design exists to prevent: the
host believes its policy was injected when the model actually received a
sentence with a hole in it.

### Replacing The If-Chain Without Adding Persona Slots

The registry alone would be a refactor with no new capability. The
persona prefix/suffix slots are what make ordered placement useful, and
they are also the natural place for the strict interpolation contract.

### Joining Sections Into One System Message

It would break the one-message-per-section resume representation for no
behavioral gain, since the Anthropic adapter folds system content into
a single block anyway.
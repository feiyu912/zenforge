# ADR 0031: Headless Exec Protocol

Status: accepted

## Context

ZenForge's CLI could run a task (`run`, `code`, `resume`) and could replay
a stored event log as JSON (`events --json`), but it had no headless
contract for a machine consumer: the only live output was the human
renderer, and there was no way to demand a structured final answer.
Codex's `exec` command defines exactly that surface — `--json` for a
JSONL event stream, `--output-schema FILE` for a schema-constrained
final response, `-o/--output-last-message FILE` for the final message,
and a prompt read from an argument, `-`, or piped stdin. DSH exposes a
comparable headless stream.

## Decision

### A separate `exec` command rather than more `run` flags

`run` keeps its current human-facing behavior; `exec` is the documented
machine entry point. It reuses the same flag set (`bindOptions`), agent
construction, and stream machinery, so `--config`, tools, approvals,
checkpointing, and exit codes behave identically.

### `--json` emits the event record, not a bespoke envelope

Each line is `json.Marshal(zenforge.Event)` — the same record the
`events --json` command prints — so a consumer that already reads event
logs parses live output with the same decoder. JSONL mode disables the
human renderer entirely, and the terminal conditions (cancellation,
approval rejection, run error) are still detected and mapped to the
existing exit codes.

### `--output-schema` is forwarded to the provider

The schema travels as `model.Request.OutputSchema` (plus a label and a
tri-state strict flag that defaults to strict) and the OpenAI adapter
sends it as a `response_format` of type `json_schema`. ZenForge does not
re-validate the answer locally: with strict provider enforcement the
response is already guaranteed to match, and a second validator would
add a divergent definition of "valid".

The schema label is derived from the file stem with non-alphanumeric
characters replaced, because providers require a name and rejecting an
otherwise valid schema over a filename would be user-hostile. A schema
file must parse as a non-empty JSON object; an array, a scalar, or
malformed JSON is a usage error (`exit 2`), never a silent fallback to
unconstrained output.

### Adapters that cannot enforce a schema fail loudly

The Anthropic Messages API has no `json_schema` response format, so the
adapter returns `model.ErrUnsupportedOutputSchema` instead of ignoring
the request. Silently returning free-form text where the caller asked
for a schema is the worst outcome: the caller's structured parsing fails
later, far from the cause.

### Prompt input

`exec [prompt]` accepts the prompt as arguments, as `-`, or from piped
stdin, capped at 1 MiB to bound memory. `-o` and
`--output-last-message` are aliases, matching the reference short flag.

## Consequences

Benefits:

- a script or editor integration can drive ZenForge without parsing
  human output, and can reuse its existing event-log decoder;
- structured final answers become a first-class capability on providers
  that support schema enforcement;
- provider limitations surface immediately and actionably.

Costs:

- the CLI gains a second run-shaped command, so `printUsage` and the
  CLI design doc must mention both;
- `--json` output is the internal event shape: a consumer depends on
  event names and payload keys, which the parity plan and event docs
  already treat as a stable, versioned surface;
- Anthropic users cannot use `--output-schema`; the documented
  workaround is a provider that supports schema enforcement (or a tool
  call the caller validates).

## Alternatives Rejected

### Local schema validation before `run.done`

It would duplicate the provider's job, and a hand-rolled validator would
disagree with the provider at the edges (formats, unions, recursion)
exactly where a second opinion is least useful. Provider enforcement is
the contract.

### Emitting A Custom JSONL Envelope

A bespoke `{seq, kind, data}` envelope would break the symmetry with
`events --json` and force consumers to learn two shapes for the same
data.

### Forcing A Tool Call On Anthropic

A `tool_choice`-forced structured-output tool would work on the Messages
API, but ZenForge's loop would then treat the forced call as a real tool
invocation, requiring the kernel to special-case "this tool call is
actually the final answer". That is a model-layer change with real
semantic risk; failing loudly keeps the boundary honest until it is
designed properly.

### Adding The Flags Only To `run`

`run` is the interactive convenience path; overloading it with
machine-output modes makes both modes worse and leaves no obvious entry
point for a headless caller.
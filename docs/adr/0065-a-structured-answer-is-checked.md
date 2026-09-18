# ADR 0065: A Structured Answer Is Checked, Not Trusted

Status: accepted

## Context

A workflow's `agent(prompt, {schema})` asks a child for one object, and the
engine has always parsed the child's answer and handed the object to the
script. What it never did was *check* that object: `ValidateObjectSchema`
holds the requested schema to the supported subset, but a child that answered
`{"verdict": 7}` for `{"verdict": {"type": "string"}}`, omitted a required
field, or returned `[1, 2]` where an object was asked for still produced a
value. A script that trusted its schema — which is the entire reason to ask for
one — was trusting the model's formatting.

The parity plan recorded this as a C9 gap ("JSON Schema *data* validation for
structured children"). The reference implementation delegates conformance to
the model and the provider's structured-output support; its engine does not
verify the answer. Verifying is a deliberate divergence, and the reason is that
this engine's schema is also a *contract for the script*: `structured.verdict`
may be concatenated into a prompt, and a number there fails much later than the
call that promised a string.

## Decision

### `ValidateObjectValue` checks the answer against the schema

`workflow.ValidateObjectValue(schema, value json.RawMessage) error` compares a
parsed answer against a subset schema and reports **every** violation at once,
in the same spirit as `ValidateObjectSchema`: one pass fixes a child's answer,
not one field per run. It covers the whole subset — `type`, `oneOf`
(exactly one branch), `properties`, `required`,
`additionalProperties: false`, `items`, `enum`, and `const` — reporting paths
like `value.notes[1]` and `value.action must match exactly one oneOf branch (0
matched)`.

Details that follow from the subset already being enforced upstream:

- the schema needs no re-validation, and a node the subset cannot produce
  (no `type`, no `oneOf`) is read by the value's shape, so a hand-written
  schema given to the exported function still enforces its `enum`, `const`,
  `properties` and `items` rather than silently doing nothing;
- numbers compare as numbers: `1` and `3.0` satisfy `integer`, using the
  equality helper the subset checker already uses for `enum`/`const`;
- `required` is reported from the declared properties, because the subset
  checker already refuses a required name that is not declared;
- a `oneOf` node with sibling constraints cannot exist (the subset refuses
  them), so conformance is decided by the branches alone.

### A non-conforming answer is a failed item, with its reason logged

The engine treats a violation exactly like an answer that is not JSON at all:
the child's item resolves to `null` and its outcome is `failed`. That
consistency is the point — from the script's side there is no useful
distinction between "no object came back" and "an object came back that I
cannot use", and both are per-item failures that must not abort a `pipeline`.

A `null`, however, carries no explanation, and an empty answer and a violation
would look identical to a script author debugging a flaky child. So the
violations go to the workflow's own log channel — the same
`Observer.WorkflowLog` a script's `log()` uses — as one line per failing child:

```text
agent() structured output does not match the schema — value.verdict must be string; value.extra is not allowed (additionalProperties is false)
```

That is the one place a per-item failure with no representation in the
contract can stay visible, and it needs no interface change.

### The check is the engine's, not the host's

The validation lives where the schema does. A host that parses a child's answer
may hand the engine `Structured` bytes from any source — a model, a replay, a
test — and the engine holds all of them to the same contract. The runner
therefore did not change: it still asks a `schema` child for one JSON object
and passes the parsed bytes through, and the engine decides whether they count.

## Consequences

- The C9 gap is closed. A script can rely on `opts.schema`: a conforming
  answer resolves to the object, and anything else is `null` with a logged
  reason.
- Because a requested schema is now enforced beyond "it is JSON", an answer
  that is valid JSON but not an object is a failed item where it previously
  became a value. That is the schema being kept, not a new restriction: an
  object-rooted schema always meant an object.
- A flaky child costs one item, never the run, and never a wrong value. The
  cost is that a script cannot inspect an almost-conforming answer; a script
  that wants partial data should request the fields it will actually use, or
  omit the schema and parse the text itself.
- The engine does one extra parse per structured child. Answers are small
  (one object), and the parse is the same decoder the subset checker uses.

## Alternatives Rejected

### Reject the whole workflow on a violation

A schema is per call; a child's formatting is not the script's mistake. Fatal
would turn one flaky answer among a hundred parallel children into a failed
run, and `pipeline` already has a per-item failure slot that fits exactly.

### Resolve the value anyway and let the script validate

Then the schema would be documentation rather than a contract, and every script
would need its own validator — the engine would ship the subset checker and
still not use it for the thing it describes.

### Include the reason in the null (a sentinel object, or an error property)

The script's contract for a failed item is `null`, and inventing a second shape
that is neither a value nor a failure would make every script check for it.
The log channel already exists for narration the script does not consume.

### Validate in the runner, before the engine sees the answer

The host knows how to parse; it does not know the schema's meaning, and a
runner that validated would have to import the engine's subset rules and
duplicate the error code path. The engine owns both halves of the `agent()`
contract, so it holds both halves of the check.

## Verification

`go test ./workflow/` — `TestValidateObjectValueAcceptsConformingAnswers` (the
minimum, every field, an integer written `3.0`, a `oneOf` object),
`TestValidateObjectValueReportsEveryViolation` (missing required, wrong type,
non-integer for `integer`, `enum`, undeclared field, a bad array item, a
`oneOf` with zero matches, an array root, and several violations in one
message with the `AGENT_RESULT` code),
`TestValidateObjectValueReadsATypelessNodeByShape`, and
`TestValidateObjectValueWithoutASchemaAcceptsAnything`;
`TestAgentNullsAStructuredAnswerThatViolatesTheSchema` (the item is null, the
outcome is failed, and one log line names every violation) and
`TestAgentNullsANonObjectAnswerForAnObjectSchema`; plus the existing
`TestAgentResolvesTextAndStructuredResults` for the conforming path. Fails
without the engine hook: the violating answer resolves to a value. Plus
`go test ./... -count=1`, `go vet ./...`, and `gofmt -l`.
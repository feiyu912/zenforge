# ADR 0064: A Child Can Run On A Model It Names

Status: accepted

## Context

The workflow engine's `agent()` accepts `provider` and `model` options, and the
parity plan's C9 row recorded what the host did with them: nothing. The runner
refused them outright —

> `agent() provider/model overrides are not supported by this host`

— because the name in a script is a string and the host had no way to turn a
string into an adapter. Refusing was the honest stopgap (a silent fallback to
the host's model would have made the option a lie), but it left a real
capability missing: a workflow could not run one child on a cheap model for
summarizing and another on a strong one for the deciding call, which is the
main reason a workflow script wants to name a model at all.

Two pieces were missing. `Config` had no way to resolve a name, and the
sub-agent contract could only choose a model per *agent spec*
(`SubAgentSpec.Model`), not per *task*, which is the granularity a per-call
option needs.

## Decision

### `ModelResolver` is the seam

```go
type ModelResolver interface {
	Resolve(provider, model string) (model.Model, error)
}

type Config struct {
	// ...
	ModelResolver ModelResolver
}
```

The root package gained the interface and the field; the workflow runner reads
it once per tool call and keeps the function. A host without a resolver keeps
the old refusal, reworded to say what is actually missing ("this host cannot
resolve a model by name") rather than "not supported", because supporting it is
now a matter of configuration.

Resolving is per call, not cached: the adapter a name produces is the
resolver's business, and a host that wants to share one can. A resolver that
returns an error, or a nil adapter without one, is a start failure the script
hears as fatal — the same rule the runner already applies to any child that
never started. A child that asked for a model it cannot have must not quietly
run on the host's.

### The model rides on the task

`subagent.TaskSpec` gained `Model model.Model`, and `runChildSubAgent` selects
in this order: the task's adapter, then the agent spec's, then the host's.

The task is the right home because the request is per call: two `agent()` calls
in one script may name two different models, and both go through the same
agent spec. The field is `json:"-"` for the same reason `SubAgentSpec.Model`
is: a model is a live client, and a task that arrives as JSON must not be able
to name one. That also keeps the task tool's model-facing schema unchanged —
the model cannot ask for another model, only a host-side caller can.

### The CLI resolves names the way it already authenticates

`cliModelResolver` wraps the command's model options:

- the host's own provider (or an empty provider name) keeps the host's
  configuration — protocol, model, base URL, and the explicit key or key
  environment variable from the config file — because that is how this host
  authenticates and a script's `model: "gpt-other"` must not lose the key that
  makes it work;
- any other provider is read from *its own* environment variables
  (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, and so on). This host has one model
  section in its configuration, so the environment is the only place a second
  provider's credentials can live; without them the resolution fails and the
  error names the missing variable;
- an empty model name falls back to the host's configured model, and an empty
  provider name to the host's protocol, so `agent("x", {model: "cheap"})` means
  "this provider's cheap model" without repeating the provider.

## Consequences

- The C9 gap is closed: a workflow child can run on a named model, and the
  parity row's "refused" clause is gone.
- The selection order is now explicit and tested: task, then spec, then host.
  A caller that sets a task model overrides everything, which is what
  specificity means here.
- A host that does not implement `ModelResolver` loses nothing: the refusal is
  the same shape, and now says the host cannot resolve names.
- Worst case for a misconfigured resolver is a failed child (a null item for an
  ordinary `agent()` call, fatal for a `pipeline` start), never a child on an
  unexpected model. The alternative — falling back to the host's model — would
  spend the host's budget on work the script explicitly assigned elsewhere.

## Alternatives Rejected

### Resolve against `SubAgentSpecs` by model name

Then the set of usable models would be whatever sub-agents happen to be
configured, and a script would name a model by accident of naming an agent.
Specs describe *who* does the work; a resolver describes *what* can run it.

### Put the resolver on the workflow tool's configuration only

The need is not workflow-specific: any host-side caller composing tasks may
want to place one on another model, and the selection order has to live in the
sub-agent runtime either way. A workflow-only field would have duplicated the
lookup in the tool layer and left `TaskSpec` unable to express what the runtime
already supported.

### Cache adapters by name inside the runner

One workflow call resolves at most a handful of names, and a cache would have
to answer for credentials changing between calls. Resolution is the
resolver's, so caching there is a host decision, not a runner's.

### Accept a model adapter over the wire in a task

The task tool's arguments come from the model. Letting a task carry a live
client would let the model choose its own model and bypass the host's
configuration, which is precisely what the host-side seam prevents.

## Verification

`go test .` — `TestRunChildSubAgentPrefersTheTaskModel` (the task's adapter
runs, the spec's and the host's are not called; fails when the task's model is
not preferred), `TestWorkflowToolRunsANamedChildOnAResolvedModel` (the resolver
is asked for exactly the script's names, the named child's task carries the
resolved adapter while a sibling's carries none; fails when the resolved model
is not attached), `TestWorkflowToolReportsAnUnresolvableModel` (a resolver
failure is a reported start failure and no child starts), and
`TestWorkflowToolRefusesAModelOverrideWithoutAResolver` (a host without the
seam still refuses). `go test ./cli/` — the resolver keeps the host's
configuration for its own provider, reads a second provider's environment named
in the error when it is absent, treats provider names case-insensitively, falls
back to the host's model, and refuses an unknown provider. Plus
`go test ./... -count=1`, `go vet ./...`, and `gofmt -l`.
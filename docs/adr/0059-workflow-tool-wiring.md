# ADR 0059: A Workflow Script Drives Sub-Agent Runs Through The Orchestrator

Status: accepted

## Context

ADR 0057 shipped the workflow engine as a library with a `Runner`/`Child`
seam, and deliberately left the wiring out: the engine could run a script, but
nothing in the agent connected `agent()` to a real child run. This batch is
that wiring — the `workflow` tool definition, `agent.go` routing, and a runner
over the existing sub-agent orchestrator — so C9 is a capability a model can
actually call instead of a package with tests.

The seams already existed and were built for this:

- the tool is intercepted by name in `invokeToolOrRuntime`, exactly like
  `task`, because running a script needs the sub-agent runtime the agent owns;
- `subagent.Orchestrator.Invoke` already runs one `TaskSpec` end to end
  (registry lookup, child agent, events) and is stateless apart from the
  registry and runner it was built with, so several workflow children can run
  through it concurrently;
- `subagent.Registry.List()` is ordered, so a host with one registered
  sub-agent does not have to name it.

## Decision

### The runner is the orchestrator, one child per `agent()` call

`workflowRunner.StartChild` builds a one-task `subagent.Request` — parent run
id, step, tool-call id, and the run's sub-agent depth, so the host's nesting
policy applies unchanged — and returns a `workflowChild` whose `Result` calls
`orchestrator.Invoke`. Nothing new runs child agents: a workflow child is a
sub-agent run with the same identity, checkpointing, and event stream as a
`task` child.

The host's options are merged the same way the task tool merges them
(`mergeSubAgentRequestOptions`), with `MaxTasks` pinned to one because each
`agent()` call is its own request.

### Which sub-agent a script's children are

`agent()` has no agent-name option — the reference's workflow spawns its
host's worker agent. So the host resolves it: `Config.WorkflowAgent` names it
explicitly, and otherwise the first registered sub-agent is used (registry
order, which is stable), which is the only sensible default when a host
registered exactly one. With no registry and no specs, a workflow call fails
with a clear message instead of starting nothing.

### `provider`/`model` overrides are refused, not ignored

The engine accepts `opts.provider` and `opts.model` (the reference does). This
host has no way to resolve a per-child model — `runChildSubAgent` builds a
child from the host's model, and there is no provider-to-model factory in
`Config`. Silently dropping the option would hand a script a child on the
wrong model while claiming otherwise, so the runner returns a start failure
and the script hears a fatal `AGENT_START` naming the option. A model resolver
is a follow-up; until then the refusal is loud.

### A schema child is asked for JSON, and its answer is parsed

Child agents have no structured-output contract of their own
(`PlanningDisabled`, no schema), so `agent(prompt, {schema})` appends the
schema and an instruction to answer with one JSON object, and the runner
parses that object back out (tolerating a fence, because a model told not to
use one sometimes does). An answer that is not a JSON object is a *failed
item*, not a fatal error: the engine turns it into `null`, which is what
"the child did not satisfy the schema" means to a script.

What this does **not** do is re-validate the object against the schema's
keywords. `ValidateObjectSchema` checks the schema, not a document; ZenForge
has no JSON Schema *data* validator, and the engine's contract only requires
that `opts.schema` be inside the subset. Deep conformance is therefore the
model's obligation until either a data validator or provider-native structured
output lands — recorded here rather than implied.

### Progress rides the event contract that exists

Child lifecycle and child stream events are forwarded as `subtask.started` /
`subtask.event` / `subtask.done` / `subtask.error` — the events a `task` child
already produces — so a log reader and the ZenMind projector see workflow
children as sub-agent runs without a new event type. `phase(title)` and
`log(message)` have no wire event of their own, so they travel through
`subtask.event` with `type: "workflow.phase"` / `"workflow.log"` and a
workflow payload; `workflow.agent.start` / `workflow.agent.end` report the
child bookkeeping the engine already tracks.

### Workflow children stay out of the parent's subtask state

The task tool records its children in run state so a resumed run reuses
terminal ones. A workflow's children are the *script's*, not the parent's
plan: the script decides how many there are and in what order, so registering
them as run-state subtasks would fabricate a plan the model never made. The
run state keeps no entries, and the loop replays the whole tool call on
resume. Child reuse still happens, deterministically: child ids are
`<toolCallID>_agent_<n>` with `n` counted per call in start order, and the
sub-agent runtime already resumes a child whose checkpoint exists — so a
replayed script lands on the same child runs instead of duplicating them,
as long as the script's own call order is deterministic.

### The tool is advertised where the runtime is

`workflow` is appended next to the task tools in `configuredTools()`, so it
appears exactly where sub-agents are configured (`SubAgents`,
`SubAgentOrchestrator`, `SubAgentRegistry`, or `SubAgentSpecs`) and never
otherwise. ZenForge's CLI does not configure sub-agents at all — that is SDK
and adapter surface — so no CLI flag is added here.

### Failure text, and what the model sees

A completed run renders the reference's text shape (`workflow "name"
completed (N agents)` plus the return value) and structured content
(`workflow`, `agentsStarted`, `stopReason`, `value`). A run that did not
complete carries `Error` and `ExitCode: 1`, which is the text the harness puts
in the model's tool message (`toolResultContent` prefers `Error`), with the
error `code` in structured content as well. The engine's own message is used
verbatim rather than wrapped: it already names the reason and the limit.

## Consequences

- C9 is complete as a capability: a model can call `workflow`, children run
  as real sub-agent runs with live events, and the script's return value
  reaches the model.
- `Config.WorkflowLimits` bounds a run from the host side (zero fields take
  `workflow.DefaultLimits`), which is also how a host stops an expensive
  fan-out before the engine's own 1000-agent backstop.
- A workflow run is bounded by its tool call's context, so cancelling the run
  cancels the script and the children through the engine's cancellation path.
- Not ported in this batch: per-child `provider`/`model` (refused), JSON
  Schema *data* validation for structured children, and a dedicated workflow
  event type (`phase`/`log` ride `subtask.event`).
- Replay semantics are "re-run the tool call": a resumed run re-executes the
  script, and children are reused only through their deterministic ids and
  existing checkpoints. A non-deterministic script would re-fan-out.

## Verification

`go test .` (root: scripted-model end-to-end for the tool, fatal-cap and
refused-override paths, structured-output parsing, outcome rendering),
`go test ./tools/workflow/`, `go test ./workflow/`, `go test ./... -count=1`,
`go test -race` on the touched packages, `go vet ./...`, `gofmt -l`, and a
Linux amd64/arm64 cross-build.
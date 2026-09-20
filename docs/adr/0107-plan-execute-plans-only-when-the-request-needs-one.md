# ADR 0107: Plan-Execute Plans Only When The Request Needs A Plan

Status: accepted

Amends the plan-execute preset specified in `docs/s5-planner-todo-spec.md` and relates to
[ADR 0105](0105-the-durable-log-is-projected-into-the-consoles-vocabulary.md) (which made
the preset's work visible in the console) and
[ADR 0106](0106-a-session-title-names-the-operators-task.md).

## Context

The operator typed `who are you` into the console and read this answer:

> I'm a senior Go backend engineer, assisting with tasks like code analysis, refactoring,
> debugging, and infrastructure-aware development — focused on correctness,
> maintainability, and idiomatic Go.
>
> Let me create a concise todo plan for your request ("who are you"):

then a todo list, a research loop that read the workspace, and an approval prompt for a
shell command. Typing `h` did the same. Their question was reasonable: a plan is wanted
for work that needs one, but why does a simple question produce one?

Two things made it unavoidable:

1. The plan stage's instruction was appended to **every** input, and it allowed only one
   outcome:

   ```
   Create a concise todo plan for the user's request. You must call todo_write with the
   todo list before giving any final answer.
   ```

2. A plan stage that produced no todos **failed the run** (`plan_not_created`). The preset
   therefore had no way to answer a question: the model had to invent a plan for `h`, and
   the answer it did produce in the plan stage was discarded — the run's output comes from
   the summary stage, which only runs after every todo is executed.

So the preset's name promised a choice it did not offer: it always planned, and the plan
stage's own answer was never the run's answer.

## Decision

The preset asks the model to decide, and the orchestrator accepts the decision.

### The instruction asks for a plan, conditionally

`planner.PlanPrompt` now asks whether the request needs a plan and states both branches:
if it needs more than one step, call `todo_write` before doing anything else and do not
answer until the plan exists; if it is a question or a single-step request, answer it
directly and create no plan.

The model is the only component that can tell a question from a project, and it already
has the request. The host does not classify input by length, keywords or heuristics.

### A plan stage that answered is the run's answer

When the plan stage ends with no todos and a non-empty answer, that answer is the run's
output: the run finishes as `plan` terminal, with no execute stage and no summary call.
An orchestration-level empty result (no plan **and** no answer) still fails with
`plan_not_created`, so a model that silently does nothing is still a failure rather than
an empty success.

`planExecuteTerminal` treats a **completed plan stage with no todos** as terminal, which
is exactly the state a direct answer leaves behind. The stage's own checkpoint already
carries the answer in its messages, so a resume replays the run's output through the
existing `resumeTerminal` path instead of planning the request a second time. The
`planning.terminal` marker is not written for this state: it exists to distinguish a
terminal *stage* from a completed run, and the completed plan stage with no todos already
carries that distinction in the state itself.

## Consequences

- A question in plan-execute mode costs one model call and one answer. `who are you` now
  returns the sentence the model actually wrote, with no todo list, no workspace research
  and no approval prompt.
- Multi-step work is unchanged: the model must still create the plan before acting, the
  execute stage still drives each todo to a terminal status, and the summary stage still
  produces the final user-facing text.
- The plan stage's answer is no longer discarded. It either becomes the run's answer (no
  plan) or remains the plan-stage transcript the console renders before the work starts.
- The console shows a run with no todos for a question: the answer appears as the
  projected `assistant/message` (ADR 0105) and no todo UI is populated.
- A resumed direct answer is replayed, not re-planned, and costs no model call — tested.

## Alternatives Rejected

### Keep the mandatory plan and let the operator pick a different mode

`--mode react` or `oneshot` does answer questions, and the console advertises all three
presets (`agentPresets/list`). But it moves the decision to the operator *before* they
know what the request will need, and plan-execute is the host's default for the sessions
where the plan is valuable. The preset should hold both outcomes, not force a choice of
host.

### Classify the request in the host

A length or keyword heuristic would decide the model's job with less information than the
model has, and would be wrong in both directions (a one-line request can be real work; a
long question is still a question). The prompt states the rule; the model applies it.

### Accept any plan-stage answer even when todos exist

A plan stage that created todos and then answered in the same turn would end the run at
the plan and skip the work the plan describes — the failure the earlier draft of this
change produced in `TestAgentPlanExecutePresetPlansExecutesAndSummarizes`. The direct
answer is only accepted when the stage created no todos.

### Make the plan stage's answer a summary call

Sending the answer through the summary stage would cost a second model call to restate
what the model just said, and would give the summary stage a todo list it never had.

## Verification

`go test -run TestAgentPlanExecuteAnswersAQuestionWithoutAPlan .` — a plan-execute run of
`who are you` returns the model's own sentence, makes exactly one model call, leaves a
completed `plan`-stage checkpoint that `planExecuteTerminal` accepts, and replays that
answer on resume without another call. The test fails against the pre-change derivation
(`plan_not_created`, no terminal state).

`go test -run 'TestAgentPlanExecute|TestAgentPlanNotCreated' .` — the existing preset
tests still pass: a planning run creates its plan, executes each todo, summarizes, and
still fails closed on the states that must fail (`plan_not_created` when the stage
produced nothing usable, a non-terminal todo, a summary turn with tool calls, a failed
terminal checkpoint save).
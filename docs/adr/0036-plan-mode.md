# ADR 0036: Plan Mode As A Durable Read-Only Phase

Status: accepted

## Context

Codex offers a plan collaboration mode in which the agent investigates and
proposes before touching anything, and DSH has an equivalent plan mode.
The value is not politeness: a model that can write files while it is still
"thinking out loud" produces half-finished edits, and a human who wants to
review an approach needs a phase boundary where the workspace is
guaranteed untouched.

ZenForge's policies already know how to refuse an operation (shell rules,
file roots, sandbox escalation), but they refuse *per operation* and are
configured statically. Plan mode is different: it is a phase of a run,
entered at the start and left exactly once, and the phase must survive a
resume — otherwise a run that crashed mid-planning would come back with
write access.

## Decision

### Read-only is a declaration, and the default is "mutating"

`tool.ReadOnlyDeclarer` is a one-method interface (`ReadOnly() bool`), and
`tool.ReadOnlyOf` reports false for any tool that does not declare itself.
The middleware therefore fails closed: a tool nobody classified is
refused, exactly like an unclassified shell command. Declarations were
added to the tools whose effect is informational or run-state-only:
workspace read/list/glob/grep, todo bookkeeping, ask_user, present,
get_context_remaining, tool_search, and the web tools. Writing, editing,
patching, shell, and subagent tools stay undeclared.

`tools.ReadOnly(inner)` wraps a `TypedTool` (whose concrete type lives in
the tools package and cannot grow a method), so a tool package declares
read-only in one line instead of defining a bespoke wrapper.

### The refusal is a structured result, not a crash

`tool.PlanMode(resolve)` returns a `tool.Result` with
`Metadata["code"] = PLAN_MODE_READ_ONLY`, a structured payload naming the
tool, the phase, the arguments, and guidance, plus `ErrPlanModeReadOnly`.
The model sees a recoverable, machine-readable refusal telling it to
investigate read-only and then call `exit_plan_mode`, rather than an
opaque failure it might retry.

### The phase lives in run state

The phase is a value in `harness.RunState.Meta`
(`zenforge.plan.mode` = `planning` | `executing`). `toolCallMetadata`
already copies the run's Meta into every tool call, so the middleware
reads the phase without any new plumbing, and because Meta is checkpointed
the phase survives a resume. The agent writes `planning` only when the key
is absent, so resuming an approved run does not push it back into
planning.

### `exit_plan_mode` presents the plan to the approval broker

`tools/plan`'s tool takes the plan text, and without approval returns
`approval.RequiredResult` naming `plan.approve` with the **plan text** as
the payload. Its approval check compares the plan's SHA-256 fingerprint
rather than using `MatchesApprovedMetadata`'s rule-key fallback: that
fallback exists so a broker can grant an operation by rule, but for a plan
it would let one approved plan authorize a different one. The human (or
policy broker) therefore approves exactly the text they read.

On the approved result the agent flips `zenforge.plan.mode` to
`executing`, emits `plan.approved` with the plan, and continues the same
run — the next checkpoint carries the new phase.

### Opt-in, both in config and on the command line

`agent.planMode` (or `--plan`) enables the phase. The CLI registers
`exit_plan_mode` and the middleware only when it is enabled, so a host
that never opts in pays nothing and cannot be surprised by a refusal.

## Consequences

Benefits:

- a plan review has a hard boundary: while the phase is planning, no
  write, patch, shell, or subagent tool can succeed, and the refusal is
  structured and actionable;
- the phase is durable, so a crash or an explicit `resume` cannot silently
  restore write access mid-plan;
- the approval is bound to the plan text a human actually read, so
  approving one plan cannot authorize another;
- read-only classification is fail-closed and cheap to extend: one method,
  one wrapper helper.

Costs and limits:

- the middleware needs a resolver from name to tool, so a host that
  registers tools without one gets every call refused during planning
  (documented; the CLI uses the same resolver as the timeout middleware);
- a tool that *is* effectively read-only but forgets to declare it is
  refused. That is the intended direction of failure, and the list of
  declarations is small enough to audit;
- MCP tools and host-provided tools default to mutating, so plan mode is
  conservative with catalogs it cannot classify: an MCP read tool needs a
  declaration on the adapter before it can run in plan mode (the adapter
  does not yet expose a per-tool annotation);
- plan mode does not constrain *reads*: a plan-mode run can read anything
  its file policy allows, which is the point, and it can still ask the user
  questions and update todos;
- there is no separate `present_plan`/`exit_plan_mode` distinction and no
  "plan revision" loop: a rejected plan stays in plan mode, and the model
  can call the tool again with a revised plan, which produces a new
  approval request with a new fingerprint.

## Alternatives Rejected

### Enforce Plan Mode Through The File Policy

Rewriting the file policy per phase would work for file tools but not for
shell, subagents, MCP, or network tools, and it would need a durable
"phase changed" signal anyway. A tool-level middleware covers every tool
uniformly and leaves the file policy to express what it is good at
(paths).

### Hide Mutating Tools From The Model In Plan Mode

Hiding has an appealing property — the model cannot call what it cannot
see — but it changes the tool catalog mid-run, invalidates prompt caching
for the tool block, and makes the refusal path untestable (there is no
call to refuse). A structured refusal also teaches the model *why*, which
hiding does not.

### Approve Plan Mode By Prompt Text Or Flag Only

Accepting any `exit_plan_mode` call as approval would let the model leave
plan mode unilaterally, which defeats the purpose. Requiring a broker
decision is what makes the phase meaningful, and the CLI's existing
approval modes (`prompt`, `always`, `never`) already cover unattended
runs: with `--approve always`, plan mode is effectively "approve the
first plan automatically", which is the sensible unattended default.

### A Separate `present_plan` Tool Plus An `exit_plan_mode` Tool

Two tools would split one decision across two calls: `present_plan` to
show text and `exit_plan_mode` to switch. The reference collapses them
into one call whose approval *is* the switch, which is both fewer round
trips and a single fingerprint to bind.
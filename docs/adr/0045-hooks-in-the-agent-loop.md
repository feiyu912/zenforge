# ADR 0045: Lifecycle Hooks In The Agent Loop

Status: accepted

## Context

ADR 0044 added the hook engine and wired `PreToolUse`/`PostToolUse` as tool
middleware. That covers a hook that guards or annotates one tool call, but
not the three events that shape a whole run: `SessionStart` (context the
agent must have before it starts), `UserPromptSubmit` (a decision about the
task itself), and `Stop` (whether the agent is allowed to declare victory).

`Stop` is the interesting one. A hook that refuses it is not a gate that
fails a run; it is an instruction to keep working, which means the refusal
has to re-enter the loop with the hook's reason as the agent's newest
instruction. That is a change to the run loop's terminal path, and the
obvious implementation — appending the reason and looping — has an
unbounded-loop hazard whenever the hook never relents.

## Decision

### The run-scoped events run once, and their context is frozen

`SessionStart` and `UserPromptSubmit` run in `applyRunContext`, which only
executes for a fresh run, and their `additionalContext` is stored in durable
`RunState.Meta` under `zenforge.hook_context`. It is rendered as a system
prompt section between the project rules and the skill catalog, so the model
can tell hook-injected instructions apart from its own deployment policy.

Freezing matters more than it looks: a resume replays what the original run
saw. Re-running `SessionStart` on resume would let an edited hook script
change the meaning of a run halfway through, which is exactly the class of
drift the frozen-context invariant exists to prevent.

### A blocking run-scoped hook fails the run before the model is called

`SessionStart` and `UserPromptSubmit` can block (`decision: block`) or stop
(`continue: false`). Both end the run with an error naming the hook's
reason, and no model request is made — there is no point paying for a turn
whose preconditions are unmet. The refusal is also reported as a
`hook.completed` event, so the log shows which hook refused and why.

### `Stop` refusals re-enter the loop, bounded

`Runner.StopHook` is consulted at every point where the run would otherwise
finish: the resumed-finalize path, the ordinary text-answer completion, and
the tool-use-limit final answer. A refusal appends the hook's reason as a
user message, clears pending tools, and puts the run back in the model
phase; the runner emits `stop.blocked` with the reason and the attempt
number.

The bound is three refusals per terminal path (`MaxStopHookRetries`). A hook
that always refuses therefore cannot hold the agent hostage: after the bound
the run finishes, and the log shows every refusal. This is the same shape as
the hook-level failure policy — the mechanism is visible, and its limits are
explicit.

### A refusal without a reason is not a refusal

A hook that exits 2 with nothing on stderr, or returns `decision: block`
without a reason, is a *failure* here as everywhere else in ADR 0044. It
fails open (unless the hook set `failClosed`), because the agent cannot act
on an unexplained refusal, and silently stopping would be worse than
continuing.

## Consequences

Benefits:

- a repository rule can be injected into the system prompt, and an
  out-of-scope request can be refused, without patching the harness;
- a hook can require verification before the agent finishes, which is the
  highest-value use of lifecycle hooks;
- the reason for a refusal reaches the model as an instruction it can act
  on, not as an opaque error;
- every refusal is logged, and the retry bound is small and explicit;
- a resumed run replays the hook context it started with.

Costs and limits:

- the retry bound is per terminal path and resets on resume, so a
  pathological hook plus many resumes can still add turns;
- a refused `Stop` costs a model turn;
- hook context is frozen: changing the hooks file does not affect a run that
  is already underway, and a new run is needed to pick up the change;
- `SessionStart` cannot add tools or change the sandbox; it adds context and
  can refuse, nothing more.

## Alternatives Rejected

### Treat A Refused Stop As A Run Error

Failing the run would punish the user for a hook that is trying to improve
the result. The reason is an instruction, so the agent gets to act on it.

### Loop Until The Hook Relents

A hook with a bug (a path that never exists, a test that cannot pass) would
spin the agent forever and burn tokens. The bound is three refusals, after
which the run finishes and the refusals remain in the log for the user to
see.

### Re-run The Run-Scoped Hooks On Resume

That would let a changed script retroactively alter a run in flight, and it
would make a resumed run's prompt differ from the checkpointed one. The
frozen-context invariant wins.

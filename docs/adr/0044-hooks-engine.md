# ADR 0044: Hooks With A Fail-Open Default

Status: accepted

## Context

Users need to influence what the agent does without patching it: refuse a
tool call in a repository with strict rules, inject a reminder before every
shell command, or run a formatter after a write. The reference does this
with hooks: user-configured commands bound to lifecycle events, receiving a
JSON payload on stdin, whose exit code and stdout carry the decision.

The contract has three parts worth porting exactly, because each encodes a
judgement:

- **exit 0 parses stdout as JSON**, so a hook can return a rich decision
  (block with a reason, rewrite the tool's arguments, add context, ask the
  agent to stop) rather than only "yes" or "no";
- **exit 2 blocks, with stderr as the reason**, which makes a simple shell
  script a complete hook;
- **any other exit code is a failure**, not a block.

The sharp edge is what a *failure* means. A hook that crashes, times out, or
prints malformed JSON has produced no decision — and both possible defaults
are wrong in some deployment.

## Decision

### Default is fail-open, per-hook `failClosed` is opt-in

A failing hook is recorded as a failure, surfaced in the result metadata,
and does not block. Failing closed by default would turn one broken hook
script into a dead agent that cannot be diagnosed from the model's point of
view, which is the same class of failure as a sandbox that silently does not
apply. A hook that guards something important sets `failClosed: true`, and
then its failure blocks — including a timeout, which is the case where
fail-open is most tempting and most dangerous.

### A malformed decision is a failure, never an opinion

Output that is not JSON, JSON that fails to parse, an unknown `decision`
value, a block with no reason, and a bare `exit 2` with nothing on stderr
are all failures. The alternative — treating unparsable output as "allow" —
means a hook that *meant* to block is silently ignored because of a
formatting mistake. The test suite asserts each of these is a failure and
that none of them blocks by accident.

### Both key styles are in the payload

The payload carries `hook_event_name`, `hook_event_name`'s camel-case
sibling, `tool_name`/`toolName`, `tool_input`/`toolInput`, and so on. The
reference uses snake case; hand-written hooks and other harnesses use camel
case. Emitting both costs a few bytes and removes a whole category of
"my hook works everywhere but here" bugs.

### Matchers only where they mean something

`PreToolUse` and `PostToolUse` match on the tool name; a matcher on
`Stop` or `SessionStart` is a configuration error rather than a silently
ignored field. An invalid regular expression is an error at dispatch, not a
skip: a hook the user wrote must not disappear because of a typo.

### Wired as tool middleware

`hooks.Middleware` wraps the tool runtime. A blocking `PreToolUse` hook
returns `ErrBlocked` and the tool never runs; `updatedInput` replaces the
arguments for the call; `PostToolUse` annotations and context land in the
result metadata, and a `PostToolUse` block turns a successful call into a
refusal after the fact. The engine itself is independent of tools and takes
a request, so `SessionStart`, `UserPromptSubmit`, and `Stop` are
implemented and tested but not yet wired into the agent loop — the
remaining work is recorded in the parity plan rather than implied.

## Consequences

Benefits:

- users can refuse, annotate, and rewrite tool calls without changing the
  agent;
- a simple `exit 2` script is a complete hook, and a JSON decision is
  available when more control is needed;
- hook failures are visible (metadata, log, summary) instead of silent;
- `failClosed` gives security-relevant hooks the strict behaviour without
  forcing it on everyone;
- the engine is testable without a model or a process, and its process
  runner is injectable.

Costs and limits:

- hooks run arbitrary commands with the agent's privileges: the hooks file
  is as trusted as the configuration itself, and the CLI rejects unknown
  fields so a mistyped hook cannot be half-applied;
- only the tool events are wired today;
- a hook adds latency to every matching tool call, bounded by its timeout;
- hook output is capped (64KiB by default, 1MiB hard) and truncated rather
  than streamed;
- the payload is JSON on stdin, so a hook that wants to reason about the
  exact tool arguments receives them as data and must parse them itself.

## Alternatives Rejected

### Fail Closed By Default

A crash in an unrelated hook would stop the agent with a message the model
cannot act on. Security-relevant hooks opt in with `failClosed`, which is
the same shape as the rest of this project's configuration: explicit, and
visible in the file.

### Treat Unparsable Output As Allow

Then a hook that meant to block is ignored for a formatting mistake. The
project's rule is that a policy which cannot be evaluated fails loudly.

### Only Support Exit Codes

`exit 2` covers blocking, but not "add this context" or "rewrite this
argument", which is where hooks earn their keep. The JSON decision is what
makes a hook more than a tripwire.

### Run Hooks In The Agent Process

A hook is user code: running it in-process would let a panic or a hang take
down the agent. Hooks run as child processes with a timeout, and the output
is bounded.

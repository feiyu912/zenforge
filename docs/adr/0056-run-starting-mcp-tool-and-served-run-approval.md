# ADR 0056: A Run-Starting MCP Tool Is Gated Three Times, And A Served Run Never Prompts

Status: accepted

## Context

ADR 0053 shipped `zenforge mcp-server` with two read-only tools and stated why:
a tool call arrives from another process with no approval prompt in front of
it, so a run-starting tool had to wait until the approval path existed. ADR
0054 built the client half of that path (a call the server did not declare
read-only asks the *client's* operator). That still leaves the half that
matters to the machine running the server: the client's approval protects the
client's human, and the operator of this host has nobody standing in front of
the protocol stream to answer for them.

## Decision

### `--allow-run` is the grant, and without it the tool does not exist

`zenforge mcp-server` keeps its read-only tool set unless the operator passes
`--allow-run`. With it, the catalog gains `zenforge_run`. The choice of "not
advertised" over "advertised but always refused" is deliberate: a tool that
never works is a tool a caller's model keeps trying, and the operator's file is
the place to express the decision. An unadvertised tool is also unreachable —
the server answers an unknown tool with the protocol's own error.

### The tool is not declared read-only, so the client asks first

`zenforge_run` carries no `readOnlyHint`. By the rule ADR 0054 implements (and
the reference applies), a conforming client therefore asks its own operator
before calling it. Two operators, two decisions, one protocol field.

### The operator configures the run; the caller chooses only the prompt

The served run's workspace, tool set, sandbox, hooks, model, and checkpoint
store are the server's own options — the remote caller cannot widen them. The
tool takes `prompt` and nothing else. The grant therefore has a stated meaning:
`--allow-run` alone is "an agent that can read and write *this* workspace and
nothing else" (the shell and out-of-root writes still need approval), and
`--approve always` is the operator adding those. To make that configuration
possible, `mcp-server` now binds the standard option set instead of accepting
three flags.

### A served run never prompts, and says what it refused

The interactive approval broker reads the same streams the MCP protocol is
spoken on: a prompt would consume the next request as its answer, or block
forever. A served run therefore never gets that broker. `--approve always`
installs an allow broker; every other mode installs a recording deny broker
through a new `options.approvalOverride`, so `prompt` becomes a refusal whose
reason names the flag that would change it, and `never` refuses as the
operator chose. The recording is not incidental: the tool tells the *caller*
which tool calls were refused, because a run that was quietly hobbled is worse
than one that says so. `ask_user` is covered by the same rule (it rides the
approval channel), which is a real limitation of a served run and is now
documented as one.

### Refusals are an outcome; failures are an error result that keeps the run id

A refused tool call does not fail the run: the run finished, and its answer
already accounts for the refusal, so the refusal count and the tool names ride
in the result text and structured content. A run that errored, timed out, or
was cancelled is an `isError` result — and the handler sets that flag itself
rather than returning an error, because the server synthesizes the error text
when a handler returns an error and would drop the structured content, and the
caller needs the run id to inspect what happened. `--run-timeout` (default 15
minutes) bounds one run, because the server answers requests in order and a
run holds the connection until it answers. A single run slot keeps that honest
for hosts that do not use `Serve`: `Handle` is exported and may be driven from
several goroutines, and two runs on one agent would share state the agent is
not built to share, so the second caller either waits its turn or — when its
own deadline expires first — is told the server is busy.

### A supporting fix, because the timeout path exposed a real defect

Making served runs cancellable surfaced a pre-existing race: a JSONL
checkpoint save that lands and then reports the context error (the deadline
expired inside the pending-transaction window) left the loop's in-memory
sequence counter behind the store, so the *next* save — the terminal
cancellation checkpoint — collided with a checkpoint that was already there.
The run then reported `checkpoint sequence must increase` instead of the
cancellation, which is exactly the kind of misreport fail-closed reporting is
supposed to prevent. The batch fixes both ends:

- the store completes a durably-pending save (and a recovery of one) with a
  context that cannot be cancelled, so a save that has a durable intent either
  completes or is completed by the next recovery, and never reports a
  cancellation for work that landed;
- the loop re-derives its counter from the store after a failed save, so any
  remaining partially-applied save cannot mask the failure that follows it.

Both changes have tests that fail without them.

## Consequences

Benefits:

- an agent on another machine can use a ZenForge run as a tool without the
  operator of this host losing a say in it;
- the grant has a stated, conservative meaning, and the stricter reading
  (`--allow-run` with the default `--approve prompt`) is the useful one rather
  than a broken one;
- a refused call inside a served run is visible to the remote agent, so it can
  tell its own human what to ask for instead of silently returning less;
- the checkpoint fix makes cancellation report itself truthfully for every
  boundary interrupt, not only for served runs.

Costs and limits:

- the grant is static: there is no interactive or pending approval for
  starting a run, because a stdio server has no operator at a keyboard. An
  embedding host that wants a richer decision supplies its own broker (the
  override is a `options`/`approval.Broker` seam) or serves runs behind the
  HTTP API's access controller;
- a served run cannot use `ask_user` or any tool that needs approval unless
  the operator passed `--approve always`, and there is no per-request way to
  ask for an exception;
- the caller waits for the run; this server has no detached-run registry, so a
  caller with a shorter tool-call budget recovers the run from the durable log
  rather than receiving it;
- nested `mcpServers` of a served run start with the server process and live
  for its lifetime, not for one run.

## Alternatives Rejected

### Expose The Tool And Always Refuse Without The Grant

The caller's model would keep calling a tool that can never work, and the
refusal would be indistinguishable from a misconfiguration. Not advertising it
is both clearer and stricter: the catalog is the capability list.

### Ask The Client's Operator And Trust That

The client's approval is a decision by a different person about a different
machine. It is a useful layer (and the tool keeps it), but it cannot be the
only one when the risk is a process on this host.

### Route The Grant Through The HTTP `AccessController`

That interface takes an `*http.Request` and answers HTTP status codes; a stdio
server has neither. Reusing it would mean inventing a synthetic request or
weakening the interface for both callers.

### An Interactive Or Pending Approval For Starting A Run

The protocol streams are the prompt channel, so an interactive broker is
unusable. A pending-approval side channel (a durable inbox plus a separate
command or endpoint to answer it) is a real design, but it needs a decision
about where the human is; the client's own approval already covers the case
where a human is present on that side.

### Return A Run Id Immediately And Poll

It removes the caller's timeout problem, but it requires a detached-run
registry with its own lifecycle (the HTTP run manager has one, the MCP server
does not) and a status tool to go with it. The durable log already makes a
finished run recoverable, so the blocking form was kept and the limitation
recorded.

### Let The Caller Choose The Workspace

That turns one grant into every directory on the host, and it removes the only
thing that makes the grant auditable. One server per workspace (or per
project configuration) is the shape the reference servers use too.

### A Config Key Instead Of A Flag

The grant is a property of *this process*, and the flags are where the other
properties of the served run already live. A key in a file that every other
command also reads would make the grant easy to set by accident and hard to
see in a process listing.
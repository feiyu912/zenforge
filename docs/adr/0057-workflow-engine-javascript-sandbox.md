# ADR 0057: The Workflow Engine Is A JavaScript Sandbox Inside The Go Process

Status: accepted

## Context

C9 is the last unported capability of the reference's authored-agent surface:
a *workflow* is one script that fans work out across many child agents — a
fan-out, a map over a list, a chain of stages — where the reference's model
would otherwise have to issue every `task` call by hand and stitch the results
together itself. The reference implements it with a JavaScript body run inside
an async function, a handful of hooks (`agent`, `parallel`, `pipeline`,
`phase`, `log`), an `args` input, a bounded schema subset for structured child
results, and a strict error taxonomy that decides which failures kill the run
and which only null one item.

Nothing in that contract needs a Node process. The value is the *script* as
the authoring surface for orchestration, not the JavaScript runtime as a
place to put the orchestration. So this batch ports the engine — script
parsing, hook semantics, caps, cancellation, result materialization, and the
schema validator — as a self-contained `workflow/` package, with child agents
behind a `Runner` seam. Wiring the hooks to ZenForge sub-agent runs (a
`workflow` tool, `agent.go` routing) is the next batch and is deliberately not
in this one; the package ships complete and tested on its own, with no dead
code and no half-wired tool.

## Decision

### The script contract is the reference's, verbatim

The body is wrapped in `(async () => { … })()`, so top-level `await` is legal
and `return <value>` is the result. The script sees:

- `agent(prompt, opts?)` — resolves to the child's final text, or to the
  object behind `opts.schema` when one was requested; resolves to `null` when
  the child did not complete;
- `parallel(thunks)` — every thunk starts at once and the call awaits all of
  them;
- `pipeline(items, ...stages)` — each item walks the stages on its own, with
  no barrier between stages, and a stage is called as
  `stage(previous, item, index)`;
- `phase(title)` — names the phase the following `agent()` calls belong to;
- `log(message)` — narrates progress;
- `args` — the caller's JSON input, parsed into a fresh value so a script
  cannot mutate the caller's bytes.

`opts` accepts exactly `label`, `phase`, `schema`, `provider`, and `model`.
The reference's deferred names (`effort`, `isolation`, `agentType`) are
refused with a message that says they are deferred, and any other key is
refused as unrecognized. Ignoring an option would let a script ask for a
constraint the engine cannot hold it to and silently get a different run.

### Fatal and non-fatal are separated by a marker, not by message text

An ordinary error thrown inside a thunk or a stage nulls that item and skips
its remaining stages. Everything the *engine* refuses — a misused hook, an
unsupported option or schema, a tripped cap, cancellation — rejects the
combinator's promise instead, which surfaces as a failed `await` in the script
(where the script may still catch it, exactly as in the reference). The two
are told apart by a marker: engine failures are thrown as an `Error` carrying
`code` and `fatal: true`, and the combinators test the `fatal` property of a
rejection reason. Message matching would break the moment a child's output
quoted an engine message.

The code survives to the caller, too: when a marked failure reaches the top
level uncaught, the run reports that code instead of a generic script error.
The reference flattens this to a stop reason plus a message; keeping the code
is a strictly better answer for a Go caller and costs nothing, since the
message is unchanged.

### Concurrency is a slot count, and children start in call order

`agent()` calls beyond `MaxConcurrentAgents` queue and start in call order as
slots free, so a wide fan-out cannot starve the first call the script made.
`MaxTotalAgents` (default 1000) is the runaway-loop backstop: a loop that
calls `agent()` forever stops with `AGENT_CAP` and reports how many children
actually started. `MaxItemsPerCall` (4096) bounds one combinator call.
`MaxConcurrentAgents` defaults to the reference's shape — leave room for the
host, never more than 16 at once — computed from `runtime.NumCPU()`.

Children run on their own goroutines and report through a channel to the one
goroutine that owns the JavaScript runtime. A child's promise is settled only
on that goroutine, and the settle drains the runtime's microtask queue, which
is what lets a script resume mid-drain and start *more* children (the
reference's nested-chaining behavior, verified against goja before this
package was written).

### Cancellation is cooperative, bounded, and always reported

Cancellation is the next hook boundary: after the run context is done, every
hook throws `CANCELLED`, and `agent()` calls still waiting for a slot are
rejected as cancelled rather than nulled. Two bounds keep a misbehaving script
from holding the run: a grace timer (`CancelGrace`, default 5s) settles the
run as cancelled even when the script is parked on a promise no hook owns — a
script that awaits its own `new Promise(() => {})` cannot hang a run forever —
and a last-resort interrupt armed with that same grace window (this engine's
equivalent of the reference terminating its worker thread) unblocks a script
that will not come back to a hook boundary at all. The interrupt deliberately
waits: cancelling must not race the cooperative path, so a script that settles
on its own — including one that catches `CANCELLED` and returns the partial
result it has — is allowed to, and only a script that goes quiet is
interrupted and reported as cancelled.

The engine also derives a child context per run and always cancels it when the
run ends, so no child outlives its run on any path: completion, script error,
or cancellation.

### The schema subset is enforced, not approximated

`ValidateObjectSchema` implements the reference's subset exactly: an
object-rooted schema may use `type`, `oneOf`, `properties`, `required`,
`additionalProperties` (boolean only), `items`, `enum`, and `const`, plus the
`description`, `title`, `default`, and `examples` annotations. A type array,
`oneOf` combined with sibling constraints, a `required` name that is not
declared, `enum`/`const` that disagree, and any keyword outside the set are
refused, and every violation is reported in one message so an author can fix
the schema in a pass instead of one error at a time. A schema that asked for
something the child cannot be held to must fail loudly — the alternative is a
model quietly returning data the script will misread.

### The result must be plain JSON

The script's return value is serialized through the runtime's own
`JSON.stringify` and re-validated as JSON. A function, a cyclic structure, or
anything else that is not plain data is `RESULT_UNSERIALIZABLE` with a message
that tells the author what to return; `undefined` and `null` become `null`.
The result is always a `json.RawMessage`, never a Go value the runtime still
owns.

### Two error codes are Go-side additions, and are documented as such

The taxonomy is the reference's, with two additions: `SCRIPT_ERROR` (the
script threw) and `SCRIPT_TIMEOUT` (the script's synchronous prefix outlived
`SyncTimeout`). The reference reports a script failure as a stop reason with a
free-form message, while this package always names a code; a caller that wants
to branch on "the script is broken" versus "a hook was misused" needs one, and
inventing an untyped error for the most common failure would be worse than
naming it. Both are recorded here rather than hidden in a doc comment.

### `Runner`/`Child` is two calls, because start and observe fail differently

The engine starts a child through `Runner.StartChild` and reads its outcome
through the returned `Child.Result`. A failure to *start* is `AGENT_START`; a
failure to *observe a started child* is `AGENT_RESULT`. Collapsing them into
one call would make a topology problem indistinguishable from a broken child,
and both are fatal to the script because neither can be reported to it
truthfully. `StartFunc` adapts a plain function for simple runners and
documents that it reports every failure as `AGENT_RESULT`.

## Consequences

Benefits:

- the reference's workflow scripts run as authored — top-level `await`,
  `pipeline` without a barrier, per-item nulls — with no Node, no subprocess,
  and no new deployment surface;
- the engine is a library: the CLI tool, an embedding host, and the tests all
  use the same `Engine.Run`, so the semantics are pinned in one place;
- every cap, the fatal/null split, and cancellation are enforced by the engine
  rather than trusted to the wiring, so the wiring batch cannot accidentally
  weaken them;
- the schema validator is separately exported and tested, so the child's
  structured-output contract is checkable before a run starts.

Costs and limits:

- the workflow capability is not yet reachable from the CLI: there is no
  `workflow` tool and no `agent.go` routing until the next batch, and the
  parity row says so;
- a workflow is one JavaScript realm per run, so a script cannot share state
  across runs, and the engine does not persist anything itself — a caller that
  wants durable progress uses the `Observer` seam;
- an embedding host owns its own tool set, so a script authored against one
  host's `Runner` may ask for a provider or model the host does not have; the
  engine passes those through and the child fails as a normal failed item;
- `SyncTimeout` bounds the synchronous prefix and cancellation interrupts the
  runtime only after the grace window, so a script that spins inside a promise
  continuation is bounded by the grace plus that interrupt rather than by an
  immediate kill; the alternative would have made cancellation racy for every
  well-behaved script;
- a script that catches `CANCELLED` and settles before the grace timer has
  completed as a normal run rather than as a cancelled one — the script made a
  decision, and the caller that asked for cancellation already has its own
  `ctx.Err()` to consult.

## Alternatives Rejected

### Implement Workflows In Go Instead Of JavaScript

A Go API would be safer and faster, but it would not be the reference's
authoring surface, and the whole point of C9 is that a workflow is *written*
by a model as one script rather than as a sequence of tool calls. Diverging
here would replace the capability with a different one and call it parity.

### Run The Script In A Node Subprocess

It would match the reference's runtime exactly and give real thread
termination, but it adds a second runtime to deploy, a second protocol to
secure, and a failure mode where the workflow engine is unavailable for
reasons unrelated to ZenForge. The engine's bounds (interrupt plus grace)
cover what the subprocess buys, and the sandbox stays a single process.

### A Transpiler Or A Restricted Go-Like DSL

A DSL needs its own parser, its own error messages, and its own ecosystem
story, and it still would not run a script a model already knows how to write.
The JSON-Schema subset validator already shows the cost of maintaining a
deliberately small language; a second one is not worth it.

### Map Every Hook Failure To A Per-Item Null

It makes the script's error handling look uniform and hides the difference
between "this item failed" and "the engine will not do that". A cap or an
unsupported option is a bug in the script, and a null tells the author nothing.

### Ignore Unsupported Schema Keywords

The reference refuses them, and for the same reason: a schema is a contract
with a child's model. Silently dropping `pattern` would make the script's
verification look stricter than it is.

### Wire The Tool In The Same Batch

The engine is the whole of C9's risk (runtime semantics, cancellation, caps);
the wiring is mechanical once the engine is trustworthy. Shipping them
together would have made one large, hard-to-review batch whose failure mode is
a half-wired tool. The package ships and is verified first; the tool follows.

### Let The Script Await Its Own Promises For Cancellation

A promise no hook owns never settles, so "cancelled" would depend on the
script's cooperation. The grace timer makes cancellation the engine's
decision: the script is told first, and then it is not asked again.
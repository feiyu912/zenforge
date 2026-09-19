# ADR 0090: The Console's Slash Commands Are the Host's Commands

Status: accepted

## Context

The console's composer fills its `/` menu from `commands/list` and admits a
submitted line through `commands/execute`
(`client/ui-commands/src/client/service.ts:90, 387`; `ui-plan/src/client/index.ts:106`).
The semantics are admission, not output: an unmatched line is answered **without
a value** and the composer turns that into "unknown or malformed command" while
keeping the draft (`service.ts:390-396`), and a successful call means the host
took the command and logged its lifecycle for the conversation to render.

This host already has commands: `commands.Catalog` loads definitions from
`--commands` (default `<workspace>/.zenforge/commands`) and a per-user directory,
and `cli/commands.go` resolves `/name args` into a task for the command line,
including `@file` includes and a command's shell permission.

## Decision

### The menu lists the host's real catalog

`commands/list` answers the descriptors of the catalog `zenforge run` resolves, so
the console offers exactly the commands this host has. A command file that
declares no `description` gets the catalog's own listing line, because the menu
needs a row and `/name <hint>` is the honest one. `input.hint` carries the
`argument-hint`, and `attachments` is never advertised: these commands are text
templates.

### execute admits the command by running what it stands for

`commands/execute` resolves the line with the host's own `commands.Expand` and
submits the expanded text through the same path a typed prompt takes. That is why
the console's commands behave like the command line's -- arguments, `@file`
includes and the command's shell permission all follow the rules this host already
enforces rather than a second interpretation written for the console -- and it is
why success is honest: the command's effect is the run, which the operator then
sees stream in the conversation.

Upstream additionally emits `command/run`/`command/done` flow nodes describing the
lifecycle. This host logs a normal run instead, so the conversation shows the
answer without a command node. The difference is real and is stated here rather
than papered over with events that do not exist.

### An unresolvable line is answered without a value

That is the client's own "unknown" path. A line that only looks like an
invocation -- `/usr/local/bin/thing`, `/review-a-file.md` -- is treated the same
way, matching the command line's own shape check. An expansion that fails (a bad
argument, an unreadable include) is also reported as unresolvable rather than as a
success whose effect the operator cannot see.

### No catalog degrades by feature

A host without a readable catalog answers `unimplemented` with `CommandSource`
named, so the menu reports a named gap instead of "command.list failed" with no
reason.

## Consequences

- The `/` menu lists this host's commands, and picking one runs it as the
  conversation's next turn.
- A command's model/tool restrictions declared in its own file apply through the
  existing prompt path; the console adds no new capability.
- Attachments are refused by name rather than dropped, and no command advertises
  them, so the refusal is not reachable from a listed command.

## Alternatives Rejected

### Report success with the expansion as the result text

Upstream's success text is the command's *output*. The expansion is the prompt, so
showing it would present the question as the answer.

### Invent a handler outcome

This host has no server-side command handlers. A fabricated `{kind: 'error',
text}` would be a lie about a run that never started, and a fabricated success
text would be a lie about output that does not exist.

### Treat an unknown `/review` as a literal task

The command line refuses it for exactly this reason: silently running the text
loses the review the operator asked for. The console gets the same answer.

### Advertise attachments so the composer offers them

The host cannot carry them; offering the affordance would end in a refusal after
the operator attached a file.

## Verification

`go test ./internal/dshapi/ -run TestCommands` -- the menu listing with and
without argument hints, an empty catalog answering `[]` rather than failing,
execute proven by the run reaching running with the expanded text recorded as its
input, an unknown line and a path-shaped line answered ok with **no value key**,
the validation table (missing line, missing agent, unknown session, attachments
refused by name, attachments that are not an array) with the catalog proven
untouched, and both methods answering `unimplemented` with `CommandSource` named
without a catalog. `go test ./cli/ -run TestConsoleCommands` -- a real catalog
directory: the descriptors, the description fallback to the listing line, the
argument hint, no advertised attachments, expansion through the host's own rules
including an `@file` include against the workspace, a namespaced command, and the
five line shapes that must not resolve.

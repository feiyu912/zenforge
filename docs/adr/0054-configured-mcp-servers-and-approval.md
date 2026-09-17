# ADR 0054: Configured MCP Servers, Gated By What The Server Declares

Status: accepted

## Context

ZenForge could adapt MCP tools into `tool.Tool` since the first adapter wave,
and ADR 0053 added the other direction (serving ZenForge as an MCP server),
but nothing ever started a server: `adapters/mcp` was library-only, and the
CLI had no way to say "these MCP servers exist". That left the client half of
the capability unreachable from the product, and it made the next step —
wiring a run-starting tool into `zenforge mcp-server` — premature, because
there was still no approval path for a remote tool call.

The gap had a sharp edge. MCP tool annotations are optional hints, and the
obvious wiring (adapt the tools, register them, call them) makes every remote
tool a tool the model can invoke with no operator in the loop. A server that
declares nothing is indistinguishable from a server that deletes rows, so a
wiring that trusts absent hints would have turned "add MCP support" into "let
a remote process mutate the environment unattended".

## Decision

### The CLI starts `mcpServers`, validated before anything is spawned

The config gains a top-level `mcpServers` map: `command`, `args`, `env`, and
`deferred`. The section is turned into an ordered list before any process is
started, and a malformed entry fails configuration loading — not the run. A
missing `command`, a server name with surrounding whitespace, a server name
containing `__` (the namespace separator, which would make
`mcp__<server>__<tool>` unrecoverable), and an environment name that cannot be
passed to a process are all errors. Server names are sorted, so the same file
starts and registers tools in the same order on every run.

### A server that cannot be started fails the command

The reference harness logs a failed server and continues (`failOnStartupError`
defaults to false), because its server list is often shared and user-level. A
`mcpServers` section here is the operator's own file, like the hook file and
the sandbox backend, and both of those fail closed: a typo must not silently
remove half the tool set from a run. A server that cannot spawn, or whose
`initialize`/`tools/list` does not answer within 30 seconds, therefore fails
the command with the server's name in the error.

### Tools register before the deferred decision, namespaced per server

Remote names are `mcp__<server>__<tool>` (ADR 0053's client half), so two
servers can both offer `read_file`. The tools join the catalog *before* the
`tool_search` decision, because a server marked `deferred` is exactly what
makes lazy loading necessary. A cross-server collision of exposed names — two
server names whose sanitized forms collapse onto one namespace — is refused at
startup instead of surfacing as a registry error at run time.

### The approval gate defaults to asking

A call is gated behind the approval channel unless the server declared the
tool read-only. The rule is the reference's: a destructive tool always asks, a
read-only tool never asks, and anything else asks unless the server explicitly
declared it non-destructive *and* closed-world. An absent hint asks. The gate
lives in the tool, next to the definition and the server name it belongs to,
rather than in a CLI middleware: it is the same shape as the workspace and
shell tools, it works for any host that composes an approval broker, and it
keeps the decision out of a list of names the CLI would have to maintain.

The request carries the server, the remote name, and both hints, plus two
keys. The *rule key* is the tool's identity (`mcp:<server>:<tool>`) — the
scope a session-wide "always allow this tool" is persisted against. The
*fingerprint* hashes that identity together with the call's arguments, so a
run-scoped approval is scoped to exactly the payload it was granted for and
cannot be replayed for a different one. `ServerOptions.SkipApproval` is the
explicit opt-out for a caller that already made the trust decision.

### A server process inherits a scrubbed environment

`StdioConfig.Env` is merged onto the ambient environment after
credential-shaped names (`KEY`, `PASSWORD`, `SECRET`, `TOKEN`, case
insensitive) are dropped, which is the reference's heuristic. The operator's
provider key must not leak into a third-party process implicitly, and the
explicit `env` entry is how a server that genuinely needs a credential gets
one.

### The command owns every process it opened

`options` carries named closers. `buildAgent` registers each MCP client (and,
fixing an older leak, the event and checkpoint stores) as soon as it opens it,
and every command drains them on the way out. The schedule loop drains per
firing, because each firing builds its own agent and would otherwise
accumulate server processes. A close failure is reported and does not change
the command's outcome: the run has already finished, and a server that exits
badly on shutdown must not turn a successful run into a failed one.

## Consequences

Benefits:

- the client half of MCP is reachable from the product: a config file is
  enough to give a run another process's tools;
- the default posture is the safe one — an unrelated MCP server with no
  annotations cannot be called unattended, and the model still sees the
  request in the event stream when it is refused;
- one owner per process, so a failed build, a finished run, and a repeated
  schedule all leave no stray server behind;
- the deterministic order and the startup collision check turn two classes of
  silent surprise (tool order, a hidden shadowed tool) into configuration
  errors.

Costs and limits:

- a project-level `mcpServers` entry breaks every run on a machine where that
  server is not installed, where the reference would have continued; the
  section is the place to be strict, but a shared config file has to stay in
  sync with what is installed;
- the 30-second handshake and 60-second call budget are constants, not
  configuration: a server that legitimately needs longer has to be changed in
  code (the reference exposes both as per-server settings);
- `resources`, `prompts`, elicitation, sampling, and `listChanged` are still
  not bridged, so a server's non-tool surface is invisible to the model;
- the gate is a per-call prompt for a server that declares nothing, which is
  noisy for a chatty non-destructive server; the server can fix that by
  declaring `destructiveHint: false, openWorldHint: false`, or the operator
  can set `SkipApproval` from an embedding host (not from the CLI config).

## Alternatives Rejected

### Warn And Continue On A Failed Server

That is the reference default and it is wrong here: this configuration is one
file the operator owns, the failure is almost always a typo or an
uninstalled binary, and the run would proceed with a tool set that silently
does not match the file. A deployment that wants tolerance can remove the
entry.

### Gate In A CLI Middleware Instead Of The Tool

A middleware would have to resolve a tool by name, re-derive which server and
which hints it came from, and keep the mapping in sync with namespacing. The
tool already knows all of it, and putting the gate there means an embedding
host that composes its own broker gets the same protection without the CLI.

### Ask For Every Call, Including Read-Only Ones

The reference's own "auto" mode does not, and a read-only tool that prompts is
a prompt the operator learns to approve without reading — which is worse than
no prompt at all.

### One Approval Scope For Everything

Collapsing the rule key and the fingerprint into one key either makes "always
allow this tool" impossible (too narrow) or lets one approval authorize any
future payload (too wide). The two keys keep both decisions available and keep
the wide one explicit.

### Start Servers Lazily On First Use

A tool that starts its server on first call hides a startup failure inside a
turn, where it surfaces as a mysterious tool error, and the deferred-tool
decision has to be made before any tool runs. Startup belongs to agent
construction.

### A `--mcp-server` Flag Instead Of Config

The value is structured (command, args, env, deferred) and per-server; a
repeated flag would grow a miniature parser for something the config file
already expresses, and would not be shareable or layerable.
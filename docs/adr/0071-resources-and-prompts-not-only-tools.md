# ADR 0071: Resources And Prompts, Not Only Tools

Status: accepted

## Context

ZenForge's MCP server exposed one primitive: tools. The parity plan recorded
the rest of the protocol as unported — `resources`, `prompts`, elicitation,
sampling, and the change notifications — and tools are the wrong shape for two
things a client already wants. A client that shows a user what a server has
should be able to *browse* recorded runs rather than call a tool to list them,
and a client that offers reusable tasks should be able to *offer* the
workspace's commands as prompts rather than have a model discover them by
calling a tool and reading the result.

Both are read-only surfaces: they expose what this install already recorded and
what its command catalog already defines, with no new capability and no new
operator grant.

## Decision

### The protocol layer can serve resources and prompts

`ServerConfig` gained `Resources []ServerResource` and
`Prompts []ServerPrompt`, declared the way tools already are, with
`Server.Resources()`/`Prompts()` accessors. New methods: `resources/list`,
`resources/read`, `prompts/list`, `prompts/get`.

`initialize` advertises only what is behind it: `resources` (with
`subscribe: false, listChanged: false`) when at least one resource is
registered, `prompts` (with `listChanged: false`) when at least one prompt is,
and no capability at all for an empty set. A capability is a promise, and
advertising one with nothing behind it is a lie a client cannot see through.

### Failure modes are the protocol's, not a tool result's

Resources and prompts are not tools, so their failures are JSON-RPC errors
rather than `isError` results. An unknown URI in `resources/read` is the
spec's resource-not-found code **-32002**; the same code answers a handler that
wraps `ErrResourceNotFound`, which is how the CLI reports a run id this install
never recorded. An unknown prompt, a blank name, or a missing required argument
in `prompts/get` is invalid params **-32602**. Malformed params are -32602, and
an unknown method stays -32601. A handler that panics fails that one call and
the stream survives, exactly as a panicking tool does.

### URI templates, because a run id is not a fixed URI

A registered URI may contain a `{name}` segment, which matches one non-empty
segment; an exact registration wins over a template. This is what lets one
`zenforge://runs/{runId}` serve every recorded run. The template is listed
verbatim in `resources/list`; the spec's separate `resources/templates/list`
is not implemented and is recorded as a gap rather than half-built.

### The CLI server exposes runs and commands

- `zenforge://runs` — the index of recorded runs, the same summaries
  `zenforge_runs` reports.
- `zenforge://runs/{runId}` — one run's recorded summary, or the
  resource-not-found error for an id this install did not record.
- One prompt per command in the workspace's catalog, named after the command
  (namespaces included) and described by it, rendering to the command's task
  text. An empty catalog means no prompts and no `prompts` capability.

Prompt arguments come from the command's own `argument-hint` placeholders
(`<x>` required, `[x]` optional); a command with no hint but a body using
`$ARGUMENTS` or `$1`..`$9` gets one optional `arguments` argument. A value
containing whitespace is quoted, so a positional stays whole. Prompts never run
inline shell: the copy handed to a prompt clears the inline-shell permission,
so `` !`...` `` stays verbatim text while workspace-confined `@file` includes
still expand. A prompt is a piece of text a client shows its user, and text
that executes a command when it is rendered is not a prompt.

## Consequences

- A client that browses can see this install's runs and this workspace's
  commands without calling a tool, and the tool list is unchanged: the existing
  protocol test still pins three read-only tools without `--allow-run`.
- Neither surface needs an operator grant, because neither can start, stop, or
  change anything; the resources read the same store the read-only tools read.
- `resources/subscribe`, `listChanged` notifications, elicitation, and sampling
  remain unported, and the advertised capabilities say so (`subscribe: false`,
  `listChanged: false`) rather than promising them.
- Prompt arguments have one asymmetry worth knowing: a whitespace-containing
  value is quoted for positional expansion, which means `$ARGUMENTS` sees those
  quotes. It is recorded here and in the limitations rather than left to be
  discovered.

## Alternatives Rejected

### Tools that return the same data

Then a browsing client's resource list is empty and every view is a tool call
the model has to be trusted to make. The data is not an action; the primitive
should say so.

### `resources/templates/list`

The correct spec home for a template, but it is a second listing with its own
list-changed story, and one template in `resources/list` is a smaller lie than
a half-implemented listing. The gap is recorded.

### Reuse the tools' error shape (`isError` results)

A client that asked for a resource and got a successful-looking result with an
error inside cannot tell "this server does not have it" from "here is the
error text you asked for". JSON-RPC codes are what a client's error handling is
written against.

### Expose prompts for shell-capable commands with their shell intact

Rendering a prompt would then execute the workspace's shell. A prompt is
staged, inspected, and often edited by a person before it runs; it must be
inert text.

## Verification

`go test ./adapters/mcp/` — `TestServerListsResources`,
`TestServerReadsResourcesAndTemplates`,
`TestServerReportsUnknownResourcesAsResourceNotFound`,
`TestServerAdvertisesCapabilitiesOnlyWhenServed`,
`TestServerSurvivesAResourceHandlerPanic`,
`TestNewServerRejectsBadResourceConfigurations`,
`TestServerListsPromptsWithArguments`, `TestServerGetsAPromptWithArguments`,
`TestServerReportsPromptErrorsAsInvalidParams`,
`TestServerSurvivesAPromptHandlerPanic`,
`TestNewServerRejectsBadPromptConfigurations`. `go test ./cli/` —
`TestMCPServerResourcesListTheRunsIndex`,
`TestMCPServerResourcesServeRecordedRuns`,
`TestMCPServerPromptsRoundTripACommand`,
`TestMCPServerPromptsAreAbsentWithoutACommandCatalog`,
`TestMCPServerPromptArgumentsComeFromTheCommand`,
`TestMCPServerPromptRenderingNeverRunsInlineShell`, and the untouched
`TestMCPServerCommandSpeaksTheProtocol`. Plus `go test ./... -count=1`,
`go test -race ./adapters/mcp/ ./cli/`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.
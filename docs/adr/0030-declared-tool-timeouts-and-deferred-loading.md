# ADR 0030: Declared Tool Timeouts And Deferred Tool Loading

Status: accepted

## Context

Two reference behaviors shape how a tool catalog reaches the model, and
ZenForge had neither.

Codex and DSH mark tools whose full schema should stay out of the
initial prompt (`defer_loading`) and expose a single `tool_search` tool
(codex `TOOL_SEARCH_TOOL_NAME`, default limit 8) whose results are
*loadable* definitions: a search returns matching names and
descriptions, and those become callable. This keeps a large remote
catalog — typically MCP servers — from consuming prompt space for tools
a run never uses. ZenForge sent every registered definition on every
request.

DSH also lets a tool *declare* a cooperative call timeout
(`timeoutMs` on the tool definition) rather than relying on the model to
pass one. The declaration is runtime metadata — `schemas()` whitelists
only name, description, and parameters — and a wrapper arms it and maps
its expiry to a structured `TOOL_TIMEOUT` outcome. ZenForge had a
uniform `tool.Timeout(d)` middleware that no runtime used, and only the
shell tool bounded itself (through a model-visible `timeoutMs`
argument).

## Decision

### Timeouts are declared by tools, never by the model

`tool.TimeoutDeclarer` lets a tool report a cooperative budget, and
`tool.TimeoutBudgetOf` reads it (negative budgets read as none). The
new `tool.TimeoutPolicy(resolve, fallback)` middleware resolves the
called tool, arms its declared budget — or the fallback for tools that
declare none; a non-positive fallback means no deadline — and, when the
deadline expires, replaces the outcome with a structured result carrying
`Metadata{code: "TOOL_TIMEOUT", timeoutMs: N}` plus `ErrTimeout`. The
declaration never reaches the model: schemas still carry only name,
description, and parameters.

The shell tool declares its policy maximum, so a call that omits
`timeoutMs` is bounded at the chain level too, while the existing
model-visible argument and its clamp stay intact. The CLI composes the
policy with a zero fallback, so tools that declare nothing run
unbounded — the reference default.

A tool that ignores its context still cannot be interrupted: the
wrapper reports expiry once the call settles. DSH behaves the same way
(it awaits the tool promise rather than racing it), and Go has no
cancellation mechanism stronger than the context.

### Deferred definitions load through `tool_search`

`tool.DeferredTool` marks a definition as withheld;
`tool.IsDeferred` reads the marker; and
`MemoryRegistry.DefinitionsMatching(predicate)` exposes a filtered
definition view. `tools/toolsearch` implements codex's tool: a required
case-insensitive `query` matched against deferred names and
descriptions, a `limit` clamped to a default of 8, and an outcome with
matches, a total, and a footer that distinguishes complete, truncated,
and empty results. The search only ever sees deferred definitions, so
eager tools cannot be rediscovered.

The agent activates every returned match: `activateTools` merges the
names into durable `RunState.Meta["zenforge.active_tools"]` (lowercased,
sorted) and emits `tools.activated` with the tool-call id. From then on
`toolSpecs(state)` includes those definitions, so activation survives
checkpoint resume — the loaded catalog is part of the run's durable
state, not process memory. `tool_search` itself is eager, and because
the CLI builds the search source from the tool set *before* appending
the search tool, a search can never return `tool_search`.

`adapters/mcp.ToolsDeferred` lists remote definitions with every tool
marked deferred, so an MCP-heavy host opts into lazy loading with one
call; `Tools` keeps eager behavior. The CLI registers `tool_search` only
when at least one configured tool declares itself deferred, which keeps
the default prompt byte-identical for the common case.

## Consequences

Benefits:

- a hung tool can no longer stall a run for tools that declare a
  budget, and timeouts are classified structurally (`TOOL_TIMEOUT` plus
  the budget) instead of by matching an error string;
- large MCP catalogs cost prompt space proportional to what the run
  actually uses, and the activation record is durable, so a resumed run
  keeps the schemas it loaded;
- both features are opt-in and additive: an existing tool set produces
  exactly the same prompt and the same behavior as before.

Costs:

- timeout enforcement depends on the tool forwarding its context, so a
  blocking tool that ignores cancellation is bounded only after it
  returns;
- `toolSpecs` now takes run state, because the visible catalog is a
  function of the run rather than of the configuration alone;
- deferred definitions must be discoverable by description text: a
  poorly described remote tool is hard for the model to find, which is
  why the search footer tells the model to narrow or raise the limit.

## Alternatives Rejected

### A Model-Visible `timeoutMs` Argument On Every Tool

The shell already has one, but a per-call model-supplied budget is not
a policy: the model can always omit it or ask for more time. The
declaration belongs to the tool author and the host policy.

### Returning Every Match From `tool_search`

Codex caps results, and an unbounded search would defeat the purpose of
deferral by injecting the whole catalog in one result.

### An Agent-Local Activation Set

Process-local state would be lost on resume and would diverge from the
checkpoint, breaking the replay guarantee that a resumed run reproduces
the original prompting.

### Always Registering `tool_search`

An unused search tool is prompt noise, and with nothing deferred a
search can never return a match. Registering it only when something is
deferred keeps the default tool list unchanged.

### Marking MCP Tools Deferred By Default

Deferral changes what the model can call without searching, so flipping
the default would silently change behavior for existing hosts. The
opt-in `ToolsDeferred` variant keeps the migration explicit.
# ZenMind Adapter Guide

ZenForge core emits neutral runtime events. `adapters/zenmind` provides a
platform boundary without importing `agent-platform` packages. Its wire
contracts are checked against fixtures captured from
`agent-platform@1893edb5`. This repository owns the neutral harness and adapter
contracts. The downstream engine integration is implemented and tested on
`agent-platform` branch `codex/zenforge-engine-bridge` at `82ca4d3`; it is not
part of this repository and has not been merged to platform `main`.

## Catalog And Session Mapping

`CatalogAgent` and `Session` model the platform catalog agent and the subset of
query request/session fields needed at execution time. When a platform
`modelKey` is present, the host resolves it through `ModelResolver`:

```go
run, err := zenmind.BuildRun(ctx, catalogAgent, session, zenmind.Runtime{
    ModelResolver: zenmind.ModelResolverFunc(func(ctx context.Context, key string) (model.Model, error) {
        return platformModels.Resolve(ctx, key)
    }),
    SkillResolver:     platformSkills,
    ToolResolver:      platformTools,
    WorkspaceResolver: platformWorkspaces,
    Events:            events,
    Checkpoints:       checkpoints,
})
if err != nil {
    return err
}

agent := zenforge.New(run.Config)
events, err := agent.Stream(ctx, run.Task)
```

The adapter maps platform `key`, `modelKey`, `mode`, tools, skills, context
tags, budget, stage settings, tool overrides, workspace/host access, request,
chat/run identity, history, resolved prompt, and access level into
`zenforge.Config`, `Task`, and namespaced task metadata. Unknown model keys and
modes fail closed. A missing model, including a typed-nil model returned
directly or by `ModelResolver`, also fails before a run is built. Deprecated DTO
aliases remain for earlier adapter callers.

`BuildRun` delegates host-owned executable objects through three resolvers:

- non-nil catalog `Skills` use `SkillResolver`; nil keeps `Runtime.Skills`,
  while an explicitly empty list disables the runtime bundle;
- `ToolResolver` receives cloned effective tool names and `ToolOverrides`;
  without it, non-empty overrides fail closed and ordinary names are selected
  from `Runtime.Tools`;
- `WorkspaceResolver` receives the session workspace root (falling back to the
  catalog root) plus cloned `HostAccess.ReadRoots` and `WriteRoots`. Any
  declared root or host access without that resolver fails closed; with no
  declaration, the legacy `Runtime.Workspace` remains valid.

Resolver errors, unknown/nil results, and typed-nil resolvers or workspace
results fail before execution. Mutable resolver inputs and runtime slices/maps
are copied where needed so later caller mutation does not change the run.

The returned `zenforge.Config` contains the resolved model, prompt, skills,
tools, and workspace, and passes through `ToolInvoker`, `ToolRuntime`,
`ToolArgumentRedaction`, `Approval`, `ApprovalGrants`, `ApprovalNamespace`,
`ApprovalGrantTTL`, `Todos`, `SubAgentSpecs`, `SubAgentRegistry`,
`SubAgentOrchestrator`, `SubAgentRunner`, `SubAgentOptions`, `Events`,
`Checkpoints`, and `Trace`. It derives `MaxSteps`, `Mode`, `Planning`, and
`SubAgents` from catalog/session values.

Catalog tool selection preserves the platform list semantics:

- an undeclared tool list exposes the runtime-provided tools;
- an explicitly empty primary `tools` list exposes no tools and overrides the
  legacy `toolNames` alias;
- a non-empty declaration selects those tools once, and fails closed if any
  declared tool is absent or a matching registry entry is nil/typed-nil;
- `toolNames` remains a fallback only when the primary `tools` field is
  undeclared.

`Session.ResolvedPrompt` is the host-resolved execution prompt and takes
precedence over the legacy `CatalogAgent.Instructions` fallback. The host is
responsible for assembling that prompt from its catalog, workspace, memory,
skills, and policy inputs before calling `BuildRun`.

### History Input Contract

`BuildRun` converts `Session.HistoryMessages` into `Task.InitialMessages` in
the supplied order. It accepts `user`, `assistant`, `system`, and `tool` roles,
string `content` and `name`, OpenAI-style assistant `tool_calls`, and tool-call
identity under either `tool_call_id` or `toolCallId`. Each tool call requires a
non-blank `id`, a non-blank `function.name`, and JSON
`function.arguments`. Tool-call arrays are valid only on assistant messages;
tool-call identity is valid only on tool messages, where it is required.

Malformed input fails `BuildRun` before execution. Errors identify the
zero-based history position, for example
`history message 1: tool_calls[0] function.arguments must be valid JSON`.
Unknown platform-only message fields are retained only in namespaced session
metadata and do not enter the neutral model message.

The current session message is mapped to `Task.Input`, not appended to history
by the adapter. ZenForge persists the converted history first and appends that
input exactly once. Resume uses checkpointed messages without reinjecting
history; in `plan_execute`, only planning receives conversation history. At the
core task boundary, `json.RawMessage` tool arguments in `InitialMessages` are
deep-copied into run state, so later caller mutation cannot alter model requests
or checkpoints.

Golden inputs:

- `adapters/zenmind/testdata/platform/catalog_agent.json`
- `adapters/zenmind/testdata/platform/query_session.json`

The host still owns catalog loading, auth, tenancy, model/tool construction,
prompt assembly and policy, and storage selection.

## Routing And Fallback

New integrations should route with resolved platform identities:

```go
router := zenmind.Router{
    AgentRoutes: map[string]zenmind.RouteConfig{
        "reviewer": {Engine: zenmind.EngineZenForge, Feature: zenmind.FeatureEnabled},
    },
    Initialize: func(input zenmind.RouteInput) error {
        return initializeZenForgeRun(input)
    },
}

decision := router.Route(zenmind.RouteInput{
    AgentKey: agentKey,
    ChatID:   chatID,
    RunID:    runID,
})
if !decision.UseZenForge() {
    return runLegacy(ctx, session)
}
```

Agent, chat, and run overrides are applied in that order, so the run route is
most specific. ZenForge is selected only when engine and feature values are
explicitly supported and `Initialize` succeeds. Missing identity/configuration,
unknown values, or initialization failure return `RouteLegacy`. `Decide` and
its legacy fields remain compatibility APIs.

This remains the repository-local, fail-closed routing contract. The downstream
platform implementation at `codex/zenforge-engine-bridge@82ca4d3` installs the
engine selector and holds the chosen engine across HTTP sync/async, SSE,
WebSocket, approval submit, and attach/continuation paths. Its integration tests
cover selector initialization errors and legacy fallback; no fallback occurs
after a selected engine starts streaming. The branch is not yet platform
`main`.

## Stateful Stream Projection

Use one `Projector` per run. It turns neutral events into complete platform
content/tool block lifecycles and assigns platform-local sequence numbers:

```go
projector := zenmind.NewProjectorWithIdentity(zenmind.ProjectorIdentity{
    RunID: runID, ChatID: chatID, AgentKey: agentKey,
})

for event := range events {
    projectedEvents, err := projector.ProjectStrict(event)
    if err != nil {
        return err
    }
    for _, projected := range projectedEvents {
        data, err := json.Marshal(projected)
        // data is a flat platform envelope: seq/type/payload fields/timestamp.
        _ = data
        _ = err
    }
}
```

The projector synthesizes `content.start/delta/end/snapshot`,
`tool.start/args/end/snapshot/result`, and terminal run events. It closes open
blocks before transitions and never emits `run.complete` after error or
cancellation. Bookkeeping events without a lossless platform equivalent are
ignored deliberately. `Mapper`/`MapEvent` preserve the historical one-to-one
API but do not synthesize lifecycle events; they are not evidence of full wire
compatibility by themselves.

`ProjectStrict` is the fail-closed integration path. It requires a non-blank
`ProjectorIdentity.RunID`, rejects events with a missing or different run,
requires `run.started` first, rejects duplicate starts, validates tool IDs and
names across calls/results, and rejects every event after a terminal run event.
Validation errors return before projector state changes. The older `Project`
method remains permissive for compatibility.

For process restart or attach, persist the complete projector state:

```go
state := projector.Snapshot()
encoded, err := json.Marshal(state)

var restored zenmind.ProjectorState
err = json.Unmarshal(encoded, &restored)
projector, err = zenmind.NewProjectorFromState(restored)
```

Restore rejects negative cursors, inconsistent content IDs, conflicting tool
IDs, and terminated states with open blocks. A valid restore continues
`liveSeq`, content IDs, partial content text, and open tool arguments.
Current snapshots use `zenforge.zenmind_projector_state.v2` and persist
`Identity.RunID`. Version 1 snapshots remain readable and are restored as
unbound state; because they have no run binding, `ProjectStrict` rejects them.

Golden lifecycles:

- `adapters/zenmind/testdata/platform/lifecycle_content.jsonl`
- `adapters/zenmind/testdata/platform/lifecycle_tool.jsonl`

## Approval Wire

The adapter has typed platform DTOs for `awaiting.ask`, `request.submit`, and
`awaiting.answer`:

```go
ask, err := zenmind.AwaitingAskFromRequestContext(req, awaitingID,
    zenmind.PlatformRequestContext{
        RequestID: requestID,
        ChatID: chatID,
        AgentKey: agentKey,
    }, timeoutSeconds)
// AgentKey is dispatcher-owned context and is not accepted from request.submit JSON.
submit.AgentKey = agentKey
decision, err := zenmind.DecisionFromRequestSubmit(ask, submit, time.Now())
answer, err := zenmind.AwaitingAnswerFromDecision(ask, submit, decision)
```

Translation binds and validates request/chat/run/agent/awaiting/submit identity,
exact approval IDs, decisions, scope, timeout, and terminal answer status.
Asks built through the compatibility constructor must be explicitly completed
with `BindAwaitingAsk`; unbound asks fail closed. The roundtrip golden is
`adapters/zenmind/testdata/platform/approval_roundtrip.jsonl`.

For the real core event stream, `ApprovalEventBridge` consumes
`approval.requested`, `approval.resolved`, and `approval.expired`. A request
allocates an awaiting ID and emits a bound `AwaitingAsk`; a correlated
resolution emits `AwaitingAnswer`; expiration emits a timeout error answer.
`Snapshot` copies pending and completed correlations, and
`NewApprovalEventBridgeFromSnapshot` restores them after the host supplies
trusted platform context and the allocation policy again.

A resumed request replays its existing ask only when identity and wire content
still match. A resolution marked `reused` represents a core grant that never
opened an awaiting request, so it records completion and emits no answer.
Unknown fields, mismatched identity/tool/run values, uncorrelated or duplicate
terminal events, duplicate awaiting IDs, and invalid snapshots fail closed.

`DecisionFromJSON` remains the older neutral submit helper. The bridge owns
correlation only: core does not own platform submit routes, WebSocket messages,
transport delivery, bridge snapshot persistence, or pending-awaiting storage.

## Platform Event Lines

`ChatJSONLWriter` accepts projected `StreamEvent` values and an explicit chat
ID:

```go
writer := zenmind.NewChatJSONLWriter(".zenmind/chats")
for event := range events {
    projectedEvents, err := projector.ProjectStrict(event)
    if err != nil {
        return err
    }
    for _, projected := range projectedEvents {
        if err := writer.Append(ctx, chatID, projected); err != nil {
            return err
        }
    }
}
lines, err := zenmind.ReadEventLines(ctx, ".zenmind/chats", chatID)
```

It appends to `root/chatId.jsonl`. Each `EventLine` has top-level `chatId`,
`runId`, `updatedAt`, `liveSeq`, `event`, and `_type: "event"`; the nested flat
event does not repeat replay cursors. Unsafe or mismatched chat/run identities,
invalid timestamps/sequences, malformed trailing JSON, and atomic appends from
multiple writer instances in one process are covered by tests. Duplicate or
decreasing `liveSeq` values for the same chat/run are rejected against the
durable file cursor. This writer does
not claim cross-process `flock` durability. The byte-for-byte fixture is
`adapters/zenmind/testdata/platform/chat_event_line.jsonl`.

The deprecated `LegacyChatJSONLWriter` type, constructed with
`NewLegacyChatJSONLWriter(root, mapper)`, writes `root/runId/chat.jsonl` records
in the old `zenmind.chat_trace.v1` format, read by deprecated
`ReadChatRecords`. It exists only for earlier callers. The new event-line writer
is still an event-only projection: it is not the checkpoint source of truth and
does not implement complete Chat Storage V3.1.

## Contract Evidence

Run the adapter contract suite with:

```bash
env GOTOOLCHAIN=local go test ./adapters/zenmind
```

`testdata/platform/manifest.json` records every fixture's source files,
SHA-256, and full commit
`1893edb51b8dc691ae974cea2719a835e0e21de4`. The end-to-end
`TestPlatformContractBuildProjectPersistResumeAndApprove` covers route,
run assembly, projection restore, event-line persistence, and approval binding.
Focused tests cover host resolver/config propagation, event-to-awaiting
recovery and reused semantics, strict run binding, mutation-free rejection,
and projector v2/v1 compatibility.
Passing these tests proves the
repository-local wire contract only. Separate downstream tests at
`agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3` cover the engine
bridge, feature-flag selector, HTTP/SSE/WS delivery, approval, attach, and
legacy fallback. They do not prove deployment from platform `main`, full Chat
Storage V3.1, or real Container Hub connectivity.

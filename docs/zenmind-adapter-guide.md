# ZenMind Adapter Guide

ZenForge core emits neutral runtime events. `adapters/zenmind` provides a
platform boundary without importing `agent-platform` packages. Its wire
contracts are checked against fixtures captured from
`agent-platform@1893edb5`; the external engine integration is not implemented
by this repository.

## Catalog And Session Mapping

`CatalogAgent` and `Session` model the platform catalog agent and the subset of
query request/session fields needed at execution time. When a platform
`modelKey` is present, the host resolves it through `ModelResolver`:

```go
run, err := zenmind.BuildRun(ctx, catalogAgent, session, zenmind.Runtime{
    ModelResolver: zenmind.ModelResolverFunc(func(ctx context.Context, key string) (model.Model, error) {
        return platformModels.Resolve(ctx, key)
    }),
    Tools:       tools,
    Events:      events,
    Checkpoints: checkpoints,
})
if err != nil {
    return err
}

agent := zenforge.New(run.Config)
events, err := agent.Stream(ctx, run.Task)
```

The adapter maps platform `key`, `modelKey`, `mode`, tools, skills, context
tags, budget, stage settings, tool overrides, workspace/host access, request,
chat/run identity, history, and access level into `zenforge.Config`, `Task`, and
namespaced task metadata. Unknown model keys and modes fail closed. Deprecated
DTO aliases remain for earlier adapter callers.

Golden inputs:

- `adapters/zenmind/testdata/platform/catalog_agent.json`
- `adapters/zenmind/testdata/platform/query_session.json`

The host still owns catalog loading, auth, tenancy, model/tool construction,
prompt policy, and storage selection.

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

This is fail-closed routing logic, not an installed `agent-platform` feature
flag or an engine bridge. External engine selection, SSE/WS delivery, and
fallback E2E remain unverified.

## Stateful Stream Projection

Use one `Projector` per run. It turns neutral events into complete platform
content/tool block lifecycles and assigns platform-local sequence numbers:

```go
projector := zenmind.NewProjectorWithIdentity(zenmind.ProjectorIdentity{
    ChatID: chatID, AgentKey: agentKey,
})

for event := range events {
    for _, projected := range projector.Project(event) {
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

Golden lifecycles:

- `adapters/zenmind/testdata/platform/lifecycle_content.jsonl`
- `adapters/zenmind/testdata/platform/lifecycle_tool.jsonl`

## Approval Wire

The adapter has typed platform DTOs for `awaiting.ask`, `request.submit`, and
`awaiting.answer`:

```go
ask, err := zenmind.AwaitingAskFromRequestContext(req, awaitingID,
    zenmind.PlatformRequestContext{AgentKey: agentKey}, timeoutSeconds)
decision, err := zenmind.DecisionFromRequestSubmit(ask, submit, time.Now())
answer, err := zenmind.AwaitingAnswerFromDecision(ask, submit, decision)
```

Translation validates request/chat/run/awaiting/submit identity, exact approval
IDs, decisions, scope, timeout, and terminal answer status. The roundtrip golden
is `adapters/zenmind/testdata/platform/approval_roundtrip.jsonl`.

`DecisionFromJSON` remains the older neutral submit helper. Core still does not
own platform submit routes, WebSocket messages, or pending-awaiting storage.

## Platform Event Lines

`ChatJSONLWriter` accepts projected `StreamEvent` values and an explicit chat
ID:

```go
writer := zenmind.NewChatJSONLWriter(".zenmind/chats")
for event := range events {
    for _, projected := range projector.Project(event) {
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
multiple writer instances in one process are covered by tests. This writer does
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

The golden metadata records source files and full commit
`1893edb51b8dc691ae974cea2719a835e0e21de4`. Passing these tests proves the
repository-local wire contract only. It does not prove a deployed
`agent-platform` engine bridge, feature-flag rollout, SSE/WS transport,
fallback path, full Chat Storage V3.1, or real Container Hub connectivity.

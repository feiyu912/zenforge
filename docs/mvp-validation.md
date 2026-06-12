# MVP Validation

This document maps the MVP acceptance checklist to current evidence in the
repository. The validation commands are:

```bash
env GOCACHE=/private/tmp/agent-platform-go-build-cache go test ./...
go test ./examples/...
rg -n "agent-platform|ZenMind" --glob "*.go" .
```

`go test ./...` includes `docs.TestMarkdownLinksResolve`, which verifies local
Markdown document links.

## Runtime

| Requirement | Evidence |
| --- | --- |
| `Agent.Stream` works with fake model | `TestAgentStreamEmitsLifecycleEvents`, `TestAgentStreamRunsToolAndContinuesModelLoop` |
| `Agent.Run` returns final output | `TestAgentRunReturnsModelText` |
| max steps drain pending tools before the final no-tool answer | `TestAgentMaxStepsRunsPendingToolBeforeFinalNoToolTurn` |
| final no-tool turns reject provider tool calls | `TestAgentMaxStepsRejectsToolCallsFromFinalNoToolTurn`, `TestAgentPlanExecuteRejectsToolCallsFromSummaryTurn` |
| cancellation persists a cancelled terminal state before model/tool execution | `TestAgentCancellationBeforeModelPersistsCancelledTerminalState`, `TestAgentCancellationBeforeToolPreservesPendingCall`, `TestAgentModelCancellationIsNotReportedAsFailure` |
| plan/execute persists a terminal summary with monotonic SQLite checkpoints | `TestAgentPlanExecutePersistsTerminalSummaryInSQLite` |
| checkpoint write failures stop before unsafe progress or false completion | `TestAgentStopsBeforeModelWhenCheckpointSaveFails`, `TestAgentDoesNotCompleteWhenPostModelCheckpointFails` |
| event-log failures stop execution before unrecorded progress | `TestAgentStopsBeforeModelWhenInitialEventAppendFails`, `TestAgentStopsWhenModelDeltaEventAppendFails`, `TestAgentDoesNotRetryEventStoreWhenCheckpointEventAppendFails` |
| trace sink failures remain best-effort | `TestAgentTreatsTraceSinkFailureAsBestEffort` |
| OpenAI-compatible model can stream text | `model/openai.TestClientStreamsTextAndSendsChatRequest` |
| Anthropic model can stream text and tool calls | `model/anthropic.TestClientStreamsTextAndSendsMessagesRequest`, `model/anthropic.TestClientStreamsToolUse` |
| Model tool calls invoke tools | `TestAgentStreamRunsToolAndContinuesModelLoop` |
| Checkpoints written at boundaries | `TestAgentStreamRunsToolAndContinuesModelLoop`, `checkpoint.TestCheckpointJSONRoundTripAndValidate`, memory/JSONL/SQLite `TestStoreSaveLoadDelete` |
| Resume works for supported boundaries | `TestAgentResumeCompletedDoesNotCallModelAgain`, `TestAgentResumeActiveToolRetriesTool`, `TestAgentResumeActiveToolFromJSONLCheckpoint`, `TestAgentResumeWaitingApprovalUsesBroker` |
| Server HTTP/SSE helpers work | `server/harnesshttp.TestServeRunStreamsEvents`, `server/harnesshttp.TestServeResumeStreamsGETAndPOST`, `server/harnesshttp.TestHandlersRejectUnsupportedMethods`, `server/sse.TestStreamHTTPHeaders` |
| HTTP resume reports invalid POST JSON distinctly | `server/harnesshttp.TestServeResumeRejectsInvalidPostJSON` |
| HTTP event replay rejects invalid query values | `server/harnesshttp.TestServeEventsRejectsInvalidQuery` |
| HTTP access hook authorizes and injects trusted metadata | `server/harnesshttp.TestServeRunAuthorizesAndInjectsTrustedMeta`, `server/harnesshttp.TestServeEventsRejectsForbidden` |
| HTTP approval submit authorizes pending run and resolves broker | `server/harnesshttp.TestServeApprovalSubmitsPendingDecision`, `server/harnesshttp.TestServeApprovalAuthorizesPendingRun` |
| HTTP approval submit rejects bad request bodies | `server/harnesshttp.TestServeApprovalRejectsInvalidJSONAndDecision` |
| HTTP pending approval query filters by authorized run | `approval.TestPendingBrokerListsPendingForRun`, `server/harnesshttp.TestServeApprovalsListsPendingRequestsForRun`, `server/harnesshttp.TestServeApprovalsRejectsForbiddenRun` |
| HTTP live event stream subscribes to authorized run fanout | `server/harnesshttp.TestServeLiveEventsStreamsBusEvents`, `server/harnesshttp.TestServeLiveEventsRejectsForbiddenRun` |
| HTTP live event stream rejects invalid buffer configuration | `server/harnesshttp.TestServeLiveEventsRejectsInvalidBuffer` |
| live event fanout stays separate from replay storage | `eventlog.TestFanoutStoreAppendsThenPublishesAssignedSeq`, `eventlog.TestFanoutStoreClosesRunOnTerminalEvent` |
| OpenTelemetry trace sink works | `trace/otel.TestSinkEmitsSpanWithAttributes` |
| trace platform metadata enrichment works | `trace.TestWithFieldsAddsStaticPlatformMetadata` |
| sandbox session state can be checkpointed for resume | `sandbox.TestStateFromSessionCopiesSandboxMetadata`, `sandbox.TestSessionFromStateRestoresRunScopedSession`, `sandbox.TestStateFromMetadataAcceptsJSONMap`, `TestAgentCarriesSandboxStateBetweenToolCalls` |
| sandbox environment prompt can augment normalized tasks | `adapters/sandbox.TestAugmentTaskInjectsSandboxPromptAndMetadata`, `adapters/sandbox.TestAugmentTaskUsesEnvironmentIDFromMetadata` |
| sandbox shell can reuse run-scoped sessions | `tools/shell.TestShellReusesSandboxSessionFromMetadata` |
| sandbox shell failures expose structured error codes | `tools/shell.TestShellSandboxUnavailableDoesNotFallback`, `tools/shell.TestShellSandboxTimeoutIncludesStructuredErrorCode` |
| Container Hub failures map to sandbox error codes | `sandbox/containerhub.TestClientMapsHTTPFailuresToSandboxCodes` |
| repeated SQLite durable runs work | `TestSQLiteDurableRunSoak` |
| benchmark entrypoint exists | `BenchmarkAgentRunStaticModel` |

## Tools

| Requirement | Evidence |
| --- | --- |
| typed tool helper works | `tools.TestTypedToolCallsStructHandler` |
| workspace read/list/grep works | `tools/workspace.TestWorkspaceToolsReadListGrepWrite` |
| workspace write respects roots | `workspace/local` escape tests and workspace tool write tests |
| workspace write can require fresh read snapshots | `tools/workspace.TestWorkspaceWriteRequiresFreshReadSnapshot` |
| shell command allowlist works | `tools/shell.TestShellAllowsAllowlistedCommand` |
| shell allowlist blocks shell control chaining | `policy.TestReviewCommandBlocksShellControlOperatorsBeforeAllowlist`, `tools/shell.TestShellBlocksAllowlistedCommandWithShellControl` |
| risky shell returns approval request or prompt | `TestShellApprovalRequiredShape`, `TestAgentApprovalBrokerApprovesAndRetriesTool`, CLI approval mode tests |
| shell policy can produce broker-free approval plans | `approval.TestRequiredPlanValidatesRequest`, `tools/shell.TestShellApprovalPlanFromReview` |
| MCP tools adapt into ZenForge tools | `adapters/mcp.TestToolsAdaptsMCPTool`, `adapters/mcp.TestJSONRPCClientListsAndCallsTools` |
| memory entries augment normalized tasks | `adapters/memory.TestAugmentTaskAddsMemoryBlockAndMetadata` |
| memory scope metadata filters cross-tenant entries | `adapters/memory.TestScopedStoreFiltersEntriesByQueryMetadata`, `adapters/memory.TestAugmentTaskUsesScopedStoreMetadata` |
| sub-agent task tool delegates work | `TestAgentRunsSubAgentTaskTool`, `subagent.TestOrchestratorRunsTasksInStableOrder`, `tools/task.TestTaskToolSchemaAndAlias` |
| sub-agent task tool decodes bounded runtime options | `tools/task.TestTaskToolValidatesArgs` |
| sub-agent parallel execution keeps stable result order | `subagent.TestOrchestratorRunsParallelTasksInStableOrder` |
| sub-agent parallel fail-fast cancels sibling work | `subagent.TestOrchestratorParallelFailFastCancelsOtherTasks` |
| sub-agent checkpoint state avoids duplicate resumed starts | `TestStartSubtasksDeduplicatesResumedParentToolCall` |
| sub-agent resume reuses terminal child results | `TestInvokeSubAgentToolSkipsCompletedSubtaskOnResume` |
| sub-agent resume continues non-terminal child checkpoint | `TestInvokeSubAgentToolResumesNonTerminalChildCheckpoint` |
| child checkpoint backend failures do not start duplicate model work | `TestChildSubAgentCheckpointLoadFailureDoesNotStartModel` |
| child cancellation cannot become false completion | `TestRunChildSubAgentTreatsCancellationAsFailure` |

## Planning

| Requirement | Evidence |
| --- | --- |
| todo tools work | `tools/todo.TestTodoToolsWorkThroughInvoker` |
| plan/execute preset works with fake model | `TestAgentPlanExecutePresetPlansExecutesAndSummarizes` |
| plan/execute exposes one top-level lifecycle and stops on stage failure | `TestAgentPlanExecutePresetPlansExecutesAndSummarizes`, `TestAgentPlanExecuteStopsAfterInternalStageFailure` |
| plan/execute orchestration failures persist terminal checkpoints | `TestAgentPlanExecuteStopsAfterInternalStageFailure`, `TestAgentPlanExecutePersistsPlanNotCreatedFailure` |
| plan/execute terminal checkpoint failures preserve the previous resume boundary | `TestAgentPlanExecuteFailsClosedWhenTerminalCheckpointSaveFails`, `TestAgentPlanExecuteDoesNotReportSummaryFailureWhenItsCheckpointFails` |
| plan/execute surfaces planner status write failures without false task events | `TestAgentPlanExecuteSurfacesFailureToMarkNonTerminalTodo` |
| plan/execute resume continues active todo checkpoint | `TestAgentPlanExecuteResumeContinuesActiveTodoFromCheckpoint` |
| plan/execute resume summarizes terminal todos | `TestAgentPlanExecuteResumeSummarizesTerminalTodos` |
| todo updates stream | `TestAgentPlanningAddsTodoToolsAndCheckpointsTodos` |

## CLI

| Requirement | Evidence |
| --- | --- |
| `zenforge run` works | `TestRunStreamsOpenAICompatibleEndpoint`, README quickstart, full package tests |
| `zenforge resume` works | `TestResumeLoadsJSONLCheckpoint`, config/checkpoint tests |
| config file works | `TestOptionsFromConfig`, `TestEventsLoadsCheckpointDirFromConfig`, `TestInitCreatesDefaultConfig` |
| invalid shell timeout config fails clearly | `TestOptionsFromConfigRejectsInvalidShellTimeout` |
| invalid planning config fails clearly | `TestOptionsFromConfigRejectsInvalidPlanningMode` |
| invalid approval config fails clearly | `TestOptionsFromConfigRejectsInvalidApprovalMode` |
| invalid provider/checkpoint config fails clearly | `TestOptionsFromConfigRejectsInvalidProviderAndCheckpoint` |
| negative agent/workspace/shell limit config fails clearly | `TestOptionsFromConfigRejectsNegativeLimits` |
| workspace byte limits from config are enforced by CLI runtime | `TestRunWorkspaceReadHonorsConfigLimit` |
| SQLite stores work through CLI | `TestEventsCanReadSQLiteStore`, `TestRunsCanReadSQLiteStore` |
| model provider config works | `TestOptionsFromConfig` covers `model.provider` |
| CLI argument errors are useful | `TestCLIReportsUsefulArgumentErrors` |
| approval prompt works | `approval/cli.TestCLIBrokerReadsDecision`, `TestApprovalBrokerModes` |
| server-style approval submit works | `approval.TestPendingBrokerWaitsForSubmittedDecision`, `approval.TestPendingBrokerRejectsUnknownDecision` |
| code-review example wires safety controls | `examples.TestCodeReviewExampleWiresSafetyControls` |

## Docs

| Requirement | Evidence |
| --- | --- |
| quickstart | `docs/quickstart.md` |
| config reference | `docs/config-reference.md` |
| tool authoring guide | `docs/tool-authoring-guide.md` |
| security guide | `docs/security-guide.md` |
| limitations section | `docs/limitations.md` |
| provider guide | `docs/provider-guide.md` |
| adapter guides | `docs/zenmind-adapter-guide.md`, `docs/mcp-adapter-guide.md`, `docs/memory-adapter-guide.md` |
| ZenMind catalog/session adapter | `adapters/zenmind.TestBuildRunMapsCatalogSessionToConfigAndTask` |
| ZenMind chat JSONL read model | `adapters/zenmind.TestChatJSONLWriterProjectsMappedEvents` |
| ZenMind feature flag routing | `adapters/zenmind.TestRouterRoutesBySessionThenAgent`, `adapters/zenmind.TestRouterRoutesByMetadataFlag` |
| failure-mode guide | `docs/failure-modes.md` |
| docs links resolve | `docs.TestMarkdownLinksResolve` |
| SDK embedded example runs without API key | `examples.TestSDKEmbeddedAgentRunsWithoutAPIKey` |

## Platform Boundary

Core implementation must not import `agent-platform` or ZenMind server/chat
packages. Automated evidence: `docs.TestGoSourceKeepsPlatformBoundary`.
Validate manually with:

```bash
rg -n "agent-platform|ZenMind" --glob "*.go" .
```

The expected result is no matches in Go source files.

## CI Evidence

After each pushed phase, verify the latest commit with:

```bash
gh run list --limit 1 --json headSha,status,conclusion,workflowName,createdAt
```

The expected result is the latest `CI` run for the pushed commit with
`status=completed` and `conclusion=success`.

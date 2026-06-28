# MVP Validation

This document maps the MVP acceptance checklist to current evidence in the
repository. The validation commands are:

Status: the repository-scoped MVP acceptance items below are implemented and
covered by named tests. Provider-backed examples, a deployed `agent-platform`
integration, and a real Container Hub service require external acceptance and
are not implied by this status.

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./examples/...
rg -n '"[^"[:space:]]*agent-platform[^"[:space:]]*"' --glob "*.go" .
```

`env GOTOOLCHAIN=local go test ./...` includes
`docs.TestMarkdownLinksResolve`, which verifies local Markdown document links.

## Runtime

| Requirement | Evidence |
| --- | --- |
| root Agent delegates the core state machine to an independently testable harness runner | `harness.TestRunnerCompletesTextOnlyRun`, `harness.TestRunnerExecutesPendingToolsBeforeNextModelTurn`, `harness.TestRunnerOneshotCapsAutoTurnsAndUsesFinalNoToolTurn`, root Agent lifecycle tests |
| `Agent.Stream` works with fake model | `TestAgentStreamEmitsLifecycleEvents`, `TestAgentStreamRunsToolAndContinuesModelLoop` |
| `Agent.Run` returns final output | `TestAgentRunReturnsModelText` |
| initial conversation history is checkpointed once and owned by run state | `TestAgentInitialMessagesReachModelAndCheckpointResumeWithoutDuplication`, `TestAgentInitialToolArgumentsAreOwnedByRunState` |
| max steps drain pending tools before the final no-tool answer | `TestAgentMaxStepsRunsPendingToolBeforeFinalNoToolTurn` |
| final no-tool turns reject provider tool calls | `TestAgentMaxStepsRejectsToolCallsFromFinalNoToolTurn`, `TestAgentPlanExecuteRejectsToolCallsFromSummaryTurn` |
| cancellation persists a cancelled terminal state before model/tool execution | `TestAgentCancellationBeforeModelPersistsCancelledTerminalState`, `TestAgentCancellationBeforeToolPreservesPendingCall`, `TestAgentModelCancellationIsNotReportedAsFailure` |
| synchronous Run returns cancellation as a Go context error | `TestAgentRunReturnsCancellation` |
| production checkpoint payloads and terminal replay use one durable shape | `TestAgentCheckpointCreatedPayloadMatchesAcrossProductionPaths`, `TestAgentResumeAfterTerminalEventAppendFailureReplaysTerminalWithoutWork`, `TestAgentResumeAfterTerminalCheckpointEventFailureReplaysTerminalWithoutWork` |
| recorder preserves low-level checkpoint-before-event ordering without owning Agent lifecycle | `recorder.TestRecorderSavesCheckpointBeforeCheckpointEvent`, `recorder.TestRecorderCompleteWritesTerminalEventAfterCheckpointEvent`, `recorder.TestRecorderCompletePersistsCancelledTerminalWithCancelledContext` |
| plan/execute persists a terminal summary with monotonic SQLite checkpoints | `TestAgentPlanExecutePersistsTerminalSummaryInSQLite` |
| checkpoint write failures stop before unsafe progress or false completion | `TestAgentStopsBeforeModelWhenCheckpointSaveFails`, `TestAgentDoesNotCompleteWhenPostModelCheckpointFails` |
| interrupted model drafts are replaced at the same logical step without committing draft tools or usage | `TestAgentResumeReplacesStreamingAttemptWithoutSpendingStep`, `TestAgentPlanExecuteSummaryReplacesInterruptedAttempt`, `harness.TestRunnerResumeInterruptedAttemptDoesNotSpendAnotherStep`, `harness.TestRunnerResumeFinalizingReplacesAttemptWithNoToolChoice` |
| committed text boundaries resume without another model request | `harness.TestRunnerResumeCompletesCommittedTextBoundaryWithoutModelCall` |
| model-attempt checkpoint state fails closed on invalid status, identity, timing, step, or links | `harness.TestValidateRunStateModelAttempts` |
| checkpoint loads and resume reject unknown run-state version, phase, or mode while accepting legacy empty version/mode | `harness.TestValidateRunStateVersionPhaseAndMode`, `checkpoint.TestValidateRejectsUnsupportedRunStateDispatchFields`, `checkpoint.TestValidateAcceptsLegacyRunStateVersionAndMode`, `checkpoint_test.TestStoreLoadRejectsInvalidRunStateContract`, `TestAgentResumeRejectsInvalidCheckpointRunState` |
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
| replay-to-live follows durable sequence without gaps or duplicates and recovers from overflow | `eventlog.TestFollowDoesNotMissAppendBetweenWatermarkAndReplay`, `eventlog.TestFollowRecoversFromLiveBufferOverflow`, `eventlog.TestFollowPollsDurableStoreAndHonorsAfterSeq`, `server/harnesshttp.TestServeLiveEventsReplayModeBridgesToLive` |
| HTTP live event stream rejects invalid buffer configuration | `server/harnesshttp.TestServeLiveEventsRejectsInvalidBuffer` |
| live event fanout stays separate from replay storage | `eventlog.TestFanoutStoreAppendsThenPublishesAssignedSeq`, `eventlog.TestFanoutStoreClosesRunOnTerminalEvent` |
| OpenTelemetry trace sink works | `trace/otel.TestSinkEmitsSpanWithAttributes` |
| trace platform metadata enrichment works | `trace.TestWithFieldsAddsStaticPlatformMetadata` |
| sandbox session state can be checkpointed for resume | `sandbox.TestStateFromSessionCopiesSandboxMetadata`, `sandbox.TestSessionFromStateRestoresRunScopedSession`, `sandbox.TestStateFromMetadataAcceptsJSONMap`, `TestAgentCarriesSandboxStateBetweenToolCalls` |
| sandbox environment prompt can augment normalized tasks | `adapters/sandbox.TestAugmentTaskInjectsSandboxPromptAndMetadata`, `adapters/sandbox.TestAugmentTaskUsesEnvironmentIDFromMetadata` |
| sandbox shell can reuse run-scoped sessions | `tools/shell.TestShellReusesSandboxSessionFromMetadata` |
| sandbox sessions cannot restore across run/subtask scope | `sandbox.TestSessionFromStateRejectsDifferentRunOrSubtask`, `tools/shell.TestShellDoesNotRestoreSandboxSessionAcrossRunScope` |
| closed sandbox sessions clear checkpoint state and close is best-effort | `tools/shell.TestShellRoutesCommandToSandboxBackend`, `tools/shell.TestShellSandboxCloseIsBestEffort`, `TestApplySandboxResultStateClearsClosedSession` |
| sandbox shell failures expose structured error codes | `tools/shell.TestShellSandboxUnavailableDoesNotFallback`, `tools/shell.TestShellSandboxTimeoutIncludesStructuredErrorCode` |
| Container Hub failures, deadlines, and cancellation map predictably | `sandbox/containerhub.TestClientMapsHTTPFailuresToSandboxCodes`, `sandbox/containerhub.TestClientMapsTransportCancellationAndTimeout` |
| Container Hub response bodies are bounded | `sandbox/containerhub.TestClientRejectsOversizedSuccessResponses` |
| JSONL checkpoint saves recover pending transactions and reject unsafe run IDs | `checkpoint/jsonl.TestStoreLoadRecoversPendingCheckpointWithoutRetryingSave`, `checkpoint/jsonl.TestStoreRejectsUnsafeRunIDs` |
| JSONL checkpoint/event writers serialize across processes | `checkpoint/jsonl.TestStoresAcrossProcessesSerializeCheckpointSequences`, `eventlog/jsonl.TestStoresAcrossProcessesSerializeAppends` |
| repeated SQLite durable runs work | `TestSQLiteDurableRunSoak` |
| benchmark entrypoint exists | `BenchmarkAgentRunStaticModel` |

## Tools

| Requirement | Evidence |
| --- | --- |
| typed tool helper works | `tools.TestTypedToolCallsStructHandler`, `tools.TestTypedToolSupportsToolContextHandler` |
| tool retry requires an explicit transient marker | `tool.TestRetryOnlyRetriesMarkedTransientErrors`, `tool.TestRetrySkipsContextCancellation` |
| tool call budgets are isolated per run | `tool.TestMaxCallsIsScopedPerRun` |
| tool output caps preserve valid UTF-8 | `tool.TestMaxOutputBytesPreservesUTF8` |
| shell output capture remains bounded while the process runs | `tools/shell.TestShellOutputCapIsBoundedAndUTF8Safe` |
| audit and durable tool arguments redact nested configured keys | `tool.TestRedactArgumentsHidesNestedAuditValuesButPreservesToolInput`, `TestAgentRedactsDurableToolCallArguments` |
| workspace read/list/grep works | `tools/workspace.TestWorkspaceToolsReadListGrepWrite` |
| workspace binary and device files fail closed | `workspace.TestPlatformFileTypeClassification`, `workspace/local.TestLocalWorkspaceBlocksKnownBinaryExtensionsWithoutNULBytes`, `workspace/local.TestLocalWorkspaceGrepSkipsKnownBinaryExtensionsBeforeContentScan`, `workspace/local.TestLocalWorkspaceBlocksPlatformDeviceFiles` |
| workspace write respects roots | `workspace/local` escape tests, `tools/workspace.TestWorkspacePolicyBlocksOutsideRoots`, `policy.TestPlanFileAccessRootsApprovalAndDeny` |
| workspace file policy can produce approval requests | `tools/workspace.TestWorkspacePolicyReturnsApprovalRequest` |
| workspace write can require fresh read snapshots | `tools/workspace.TestWorkspaceWriteRequiresFreshReadSnapshot`, `tools/workspace.TestWorkspaceWriteSnapshotsAreRunScoped` |
| workspace snapshots detect same-size content changes | `tools/workspace.TestWorkspaceSnapshotDetectsContentHashChange` |
| workspace writes emit durable change events and dirty paths | `TestAgentWorkspaceWriteEmitsChangedEventAndDirtyPath` |
| shell command allowlist works | `tools/shell.TestShellAllowsAllowlistedCommand` |
| shell allowlist cannot be bypassed by an allowed first command | `policy.TestReviewCommandDoesNotAllowChainsByFirstCommandPrefix`, `tools/shell.TestShellDoesNotAllowChainByFirstCommandPrefix` |
| shell safety uses AST structure and complete platform classifier semantics | `safety/bashast.TestParseForSecurityReportsCommandStructure`, `safety/bashsec.TestReviewBashSecurityAllowsASTSimpleSafeCommand`, `policy.TestReviewCommandUsesPlatformBashSecuritySemantics` |
| complex, redirected, or dangerous shell syntax fails closed or requires approval | `policy.TestReviewCommandRequiresApprovalForOutputRedirection`, `policy.TestReviewCommandRequiresApprovalWhenASTIsTooComplex`, `safety/bashsec.TestReviewBashSecurityStillBlocksHardFailures` |
| risky shell returns approval request or prompt | `TestShellApprovalRequiredShape`, `TestAgentApprovalBrokerApprovesAndRetriesTool`, CLI approval mode tests |
| shell policy can produce broker-free approval plans | `approval.TestRequiredPlanValidatesRequest`, `tools/shell.TestShellApprovalPlanFromReview` |
| platform execution modes preserve react/oneshot/plan-execute behavior | `TestAgentOneshotCapsToolRoundsAndPersistsMode`, `TestAgentResumeUsesCheckpointedOneshotMode`, `TestAgentPlanExecutePresetPlansExecutesAndSummarizes`, `cli.TestAgentModeParsing` |
| missing approval broker pauses at a resumable checkpoint | `TestAgentPausesOnApprovalWithoutBroker`, `TestAgentRunReturnsApprovalRequiredWhenPaused`, `TestAgentResumeWaitingApprovalWithoutBrokerStaysPaused` |
| approval abort persists a cancelled terminal run | `TestAgentApprovalAbortCancelsRun` |
| run/rule approval scopes reuse only exact matching keys | `TestAgentReusesApprovalScopeWithinRun`, `TestAgentDoesNotReuseApprovalForDifferentScopeKey`, `approval.TestScopeKeyRequiresMatchingRequestIdentity` |
| approval scope grants survive checkpoint resume | `TestAgentResumeReusesCheckpointedApprovalGrant`, `harness.TestApprovalGrantReplacesMatchingScopeKey` |
| persistent rule grants reuse across runs only for the exact tenant/subject, rule key, and fingerprint | `TestAgentReusesPersistentRuleGrantAcrossRuns`, `TestAgentPersistentRuleGrantIsTenantIsolated`, `approval.TestMemoryGrantStoreIsolationExpiryAndRevoke` |
| persistent grant TTL/revoke and SQLite durability are enforced | `approval.TestMemoryGrantStoreIsolationExpiryAndRevoke`, `approval/sqlite.TestStoreRoundTripAndRevoke`, `approval/sqlite.TestStoreConcurrentPutAndGet` |
| absent grant stores preserve compatibility and configured or malformed stores fail closed | `TestAgentTypedNilApprovalGrantStoreKeepsCompatibility`, `TestAgentApprovalGrantStoreFailureFailsClosed`, `TestAgentMalformedPersistentGrantFailsClosed`, `approval/sqlite.TestStoreGetRejectsMalformedPersistedGrant` |
| approval routing identity is harness-owned and decision IDs must match | `TestAgentNormalizesApprovalRuntimeIdentity`, `TestResolveApprovalRejectsMismatchedDecisionRequest` |
| approval middleware binds decision identity and propagates abort cancellation | `tool/middleware.TestApprovalMiddlewareRejectsMismatchedDecisionIdentity`, `tool/middleware.TestApprovalMiddlewareAbortSignalsCancellation`, `approval.TestAbortErrorSignalsRunCancellation` |
| MCP tools adapt into ZenForge tools | `adapters/mcp.TestToolsAdaptsMCPTool`, `adapters/mcp.TestJSONRPCClientListsAndCallsTools` |
| MCP stdio uses a real helper process, captures configured stderr, reports closed calls, and closes concurrently without leaking the child | `adapters/mcp.TestStdioClientLifecycle`, `adapters/mcp.TestStdioClientContextCancellation`, `adapters/mcp.TestStdioClientConcurrentClose`, `adapters/mcp.TestStdioClientCloseUnblocksRPC`, `adapters/mcp.TestMCPStdioHelperProcess` |
| memory entries augment normalized tasks | `adapters/memory.TestAugmentTaskAddsMemoryBlockAndMetadata` |
| memory scope metadata filters cross-tenant entries | `adapters/memory.TestScopedStoreFiltersEntriesByQueryMetadata`, `adapters/memory.TestAugmentTaskUsesScopedStoreMetadata` |
| sub-agent task tool delegates work | `TestAgentRunsSubAgentTaskTool`, `subagent.TestOrchestratorRunsTasksInStableOrder`, `tools/task.TestTaskToolSchemaAndAlias` |
| sub-agent tools are advertised without planner configuration | `TestAgentRunsSubAgentTaskTool` |
| sub-agent task tool decodes bounded runtime options | `tools/task.TestTaskToolValidatesArgs` |
| host sub-agent task limits cannot be widened by requests | `TestAgentSubAgentRequestCannotRaiseHostTaskLimit`, `TestAgentSubAgentHostLimitControlsAdvertisedSchema`, `subagent.TestOrchestratorRequestMaxTasksCanOnlyTightenHostLimit`, `subagent.TestOrchestratorUsesDefaultHostTaskLimit` |
| nested sub-agents are blocked by default before state changes | `TestNestedSubAgentCallIsRejectedByDefaultBeforeStateChange`, `subagent.TestRequestEnforcesNestedDepthLimit` |
| explicit nested delegation is bounded by host maximum depth | `TestRunChildSubAgentSupportsHostBoundedNestedDelegation`, `subagent.TestRequestEnforcesNestedDepthLimit` |
| child metadata is isolated by default and explicitly inherits trusted parent context | `TestInvokeSubAgentToolScopesParentContext`, `TestRunChildSubAgentDoesNotInheritContextWhenDisabled` |
| child metadata precedence and copied file scope are deterministic | `TestRunChildSubAgentBuildsScopedMetadata` |
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
| plan/execute gives history only to planning and does not duplicate it on resume | `TestAgentPlanExecutePresetPlansExecutesAndSummarizes`, `TestAgentPlanExecuteResumeKeepsCheckpointedHistoryOnce` |
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
| `zenforge code` binds workspace and shell tools to its positional repository | `TestCodeUsesPositionalRepositoryAsWorkspace`, `TestCodeUsesPositionalRepositoryAsShellWorkingDirectory`, `TestCodeRejectsNonDirectoryRepository`, `TestCodeRejectsMissingRepository` |
| `zenforge resume` works | `TestResumeLoadsJSONLCheckpoint`, config/checkpoint tests |
| config file works | `TestOptionsFromConfig`, `TestEventsLoadsCheckpointDirFromConfig`, `TestInitCreatesDefaultConfig` |
| invalid shell timeout config fails clearly | `TestOptionsFromConfigRejectsInvalidShellTimeout` |
| invalid planning config fails clearly | `TestOptionsFromConfigRejectsInvalidPlanningMode` |
| invalid approval config fails clearly | `TestOptionsFromConfigRejectsInvalidApprovalMode` |
| invalid provider/checkpoint config fails clearly | `TestOptionsFromConfigRejectsInvalidProviderAndCheckpoint` |
| negative agent/workspace/shell limit config fails clearly | `TestOptionsFromConfigRejectsNegativeLimits` |
| workspace byte limits from config are enforced by CLI runtime | `TestRunWorkspaceReadHonorsConfigLimit` |
| workspace roots from config are enforced by CLI runtime | `TestOptionsFromConfig`, `TestRunWorkspaceReadHonorsConfigRoots` |
| SQLite stores work through CLI | `TestEventsCanReadSQLiteStore`, `TestRunsCanReadSQLiteStore` |
| model provider config works | `TestOptionsFromConfig` covers `model.provider` |
| CLI argument errors are useful | `TestCLIReportsUsefulArgumentErrors` |
| approval prompt works | `approval/cli.TestCLIBrokerReadsDecision`, `TestApprovalBrokerModes` |
| server-style approval submit works | `approval.TestPendingBrokerWaitsForSubmittedDecision`, `approval.TestPendingBrokerRejectsUnknownDecision` |
| code-review example wires safety controls | `examples.TestCodeReviewExampleWiresSafetyControls` |
| repo-refactor example wires explicit workspace policy | `examples.TestRepoRefactorExampleWiresWorkspacePolicy` |

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
| platform catalog/session DTOs, resolved prompt, strict history conversion, model resolution, and declared tool availability | `adapters/zenmind.TestBuildRunMapsCatalogSessionToConfigAndTask`, `adapters/zenmind.TestBuildRunPrefersResolvedPromptAndStrictlyConvertsHistory`, `adapters/zenmind.TestBuildRunRejectsMalformedHistoryWithIndex`, `adapters/zenmind.TestBuildRunPreservesToolCallIdentityWhitespaceAfterValidation`, `adapters/zenmind.TestBuildRunRejectsUnknownModelAndMode`, `adapters/zenmind.TestBuildRunValidatesCatalogTools`; fixtures `adapters/zenmind/testdata/platform/catalog_agent.json`, `adapters/zenmind/testdata/platform/query_session.json` |
| fail-closed AgentKey/ChatID/RunID routing and initialization fallback | `adapters/zenmind.TestRouterFailsClosed`, `adapters/zenmind.TestRouterRoutesOnlyExplicitInitializedZenForge`, `adapters/zenmind.TestRouterFallsBackWhenBuildRunInitializationFails`, `adapters/zenmind.TestRouterTargetsAgentChatAndRunIDs` |
| stateful content/tool wire projection and flat envelope | `adapters/zenmind.TestProjectorContentLifecycleGolden`, `adapters/zenmind.TestProjectorToolLifecycleGolden`, `adapters/zenmind.TestMapperMarshalUsesFlatWireEnvelope`; fixtures `adapters/zenmind/testdata/platform/lifecycle_content.jsonl`, `adapters/zenmind/testdata/platform/lifecycle_tool.jsonl` |
| platform approval ask/submit/answer roundtrip | `adapters/zenmind.TestApprovalRoundTripGolden`, `adapters/zenmind.TestRequestSubmitRequiresIdentityAndExactApprovalIDs`; fixture `adapters/zenmind/testdata/platform/approval_roundtrip.jsonl` |
| platform event-line JSONL wire and flat chat path | `adapters/zenmind.TestChatJSONLWriterMatchesPlatformGolden`, `adapters/zenmind.TestChatJSONLWriterFlatPathAppendAndRead`, `adapters/zenmind.TestChatJSONLWriterRejectsUnsafeOrMismatchedIdentity`; fixture `adapters/zenmind/testdata/platform/chat_event_line.jsonl` |
| failure-mode guide | `docs/failure-modes.md` |
| docs links resolve | `docs.TestMarkdownLinksResolve` |
| SDK embedded example runs without API key | `examples.TestSDKEmbeddedAgentRunsWithoutAPIKey` |

## Platform Boundary

Non-adapter Go source must remain free of `agent-platform` and ZenMind brand
coupling. `adapters/zenmind` may preserve protocol provenance, but its imports
are parsed to reject the platform module and every `internal` package. This is
enforced by `docs.TestGoSourceKeepsPlatformBoundary` and
`docs.TestPlatformBoundaryAllowlist`. A supplementary import-string check is:

```bash
rg -n '"[^"[:space:]]*agent-platform[^"[:space:]]*"' --glob "*.go" .
```

The expected result is no platform module import strings. Protocol comments and
fixture provenance under `adapters/zenmind` may name the external repository.

## CI Evidence

After each pushed phase, verify the latest commit with:

```bash
gh run list --limit 1 --json headSha,status,conclusion,workflowName,createdAt
```

The expected result is the latest `CI` run for the pushed commit with
`status=completed` and `conclusion=success`.

## External Acceptance

Repository tests validate adapter contracts with fake/local HTTP servers and
platform wire goldens captured from `agent-platform@1893edb5`. Separately,
`agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3` has automated
coverage for the engine bridge and selector across HTTP sync/async, SSE,
WebSocket, approval, attach, selector errors, and legacy fallback. This is
integration-branch evidence, not proof of merge or deployment from platform
`main`. Complete Chat Storage V3.1 and a smoke against a real Container Hub
service also remain external acceptance. Both repositories are Go 1.26.x only.

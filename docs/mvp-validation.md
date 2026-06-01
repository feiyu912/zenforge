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
| OpenAI-compatible model can stream text | `model/openai.TestClientStreamsTextAndSendsChatRequest` |
| Anthropic model can stream text and tool calls | `model/anthropic.TestClientStreamsTextAndSendsMessagesRequest`, `model/anthropic.TestClientStreamsToolUse` |
| Model tool calls invoke tools | `TestAgentStreamRunsToolAndContinuesModelLoop` |
| Checkpoints written at boundaries | `TestAgentStreamRunsToolAndContinuesModelLoop`, checkpoint memory/JSONL/SQLite tests |
| Resume works for supported boundaries | `TestAgentResumeTerminalCompletedDoesNotRerunModel`, `TestAgentResumeActiveToolRetriesToolCall`, `TestAgentResumeWaitingApprovalRequestsAgainAndContinues` |
| Server HTTP/SSE helpers work | `server/harnesshttp` and `server/sse` package tests |
| OpenTelemetry trace sink works | `trace/otel.TestSinkEmitsSpanWithAttributes` |

## Tools

| Requirement | Evidence |
| --- | --- |
| typed tool helper works | `tools.TestTypedToolCallsStructHandler` |
| workspace read/list/grep works | `tools/workspace.TestWorkspaceToolsReadListGrepWrite` |
| workspace write respects roots | `workspace/local` escape tests and workspace tool write tests |
| shell command allowlist works | `tools/shell.TestShellAllowsAllowlistedCommand` |
| risky shell returns approval request or prompt | `TestShellApprovalRequiredShape`, `TestAgentApprovalBrokerApprovesAndRetriesTool`, CLI approval mode tests |
| MCP tools adapt into ZenForge tools | `adapters/mcp.TestToolsAdaptsMCPTool`, `adapters/mcp.TestJSONRPCClientListsAndCallsTools` |
| memory entries augment normalized tasks | `adapters/memory.TestAugmentTaskAddsMemoryBlockAndMetadata` |
| sub-agent task tool delegates work | `subagent` and `tools/task` package tests |

## Planning

| Requirement | Evidence |
| --- | --- |
| todo tools work | `tools/todo.TestTodoToolsWorkThroughInvoker` |
| plan/execute preset works with fake model | `TestAgentPlanExecutePresetPlansExecutesAndSummarizes` |
| todo updates stream | `TestAgentPlanningAddsTodoToolsAndCheckpointsTodos` |

## CLI

| Requirement | Evidence |
| --- | --- |
| `zenforge run` works | CLI command wiring, README quickstart, full package tests |
| `zenforge resume` works | CLI command wiring and config/checkpoint tests |
| config file works | `TestOptionsFromConfig`, `TestEventsLoadsCheckpointDirFromConfig`, `TestInitCreatesDefaultConfig` |
| SQLite stores work through CLI | `TestEventsCanReadSQLiteStore`, `TestRunsCanReadSQLiteStore` |
| model provider config works | `TestOptionsFromConfig` covers `model.provider` |
| approval prompt works | `approval/cli.TestCLIBrokerReadsDecision`, `TestApprovalBrokerModes` |

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
| docs links resolve | `docs.TestMarkdownLinksResolve` |

## Platform Boundary

Core implementation must not import `agent-platform` or ZenMind server/chat
packages. Validate with:

```bash
rg -n "agent-platform|ZenMind" --glob "*.go" .
```

The expected result is no matches in Go source files.

## CI Evidence

GitHub Actions `CI` completed successfully for commit `f01b334` in run
`26687585157`.

# Architecture Decision Records

This directory holds the [architecture decision records](https://adr.github.io/)
for ZenForge. Each ADR captures a significant design choice: the context, the
decision, and the consequences. They are immutable historical artifacts —
later ADRs supersede earlier ones, never edit.

!!! note "Reading order"
    The ADRs are numbered chronologically. Read in order to see how the
    framework's design solidified, or jump to a specific topic using the
    table of contents on the left.

## Index

| Number | Topic |
| --- | --- |
| [0001](0001-event-log-and-checkpoint.md) | Event log and checkpoint stores |
| [0002](0002-public-event-contract.md) | Public event contract |
| [0003](0003-tool-runtime.md) | Tool runtime |
| [0004](0004-approval-broker.md) | Approval broker |
| [0005](0005-sub-agent-orchestration.md) | Sub-agent orchestration |
| [0006](0006-package-layout.md) | Package layout |
| [0007](0007-run-state-schema.md) | Run state schema |
| [0008](0008-tool-result-contract.md) | Tool result contract |
| [0009](0009-workspace-policy-separation.md) | Workspace policy separation |
| [0010](0010-shell-deny-by-default.md) | Shell deny by default |
| [0011](0011-harness-receives-normalized-input.md) | Harness receives normalized input |
| [0012](0012-model-adapters-own-provider-protocol.md) | Model adapters own provider protocol |
| [0013](0013-todo-tools-are-core.md) | Todo tools are core |
| [0014](0014-plan-execute-is-a-preset.md) | Plan-execute is a preset |
| [0015](0015-approval-is-core-state.md) | Approval is core state |
| [0016](0016-no-cross-run-approval-in-mvp.md) | No cross-run approval in MVP |
| [0017](0017-subagent-is-runtime-tool.md) | Subagent is runtime tool |
| [0018](0018-nested-subagents-disabled-by-default.md) | Nested subagents disabled by default |
| [0019](0019-sandbox-is-adapter.md) | Sandbox is adapter |
| [0020](0020-no-silent-sandbox-fallback.md) | No silent sandbox fallback |
| [0021](0021-mvp-does-not-require-subagents-or-sandbox.md) | MVP does not require subagents or sandbox |
| [0022](0022-agent-skills-progressive-disclosure.md) | Agent Skills use progressive disclosure |
| [0023](0023-context-management-retry-and-compaction.md) | Context management: classified retry and durable compaction |
| [0024](0024-hierarchical-instructions-and-environment-context.md) | Hierarchical instructions and environment context |
| [0025](0025-sandbox-escalation-ladder.md) | Sandbox escalation ladder |
| [0026](0026-user-questions-and-tool-result-guardrails.md) | User questions and tool-result guardrails |
| [0027](0027-token-meter-search-caps-turn-diffs.md) | Token meter, search discovery caps, and turn diffs |
| [0028](0028-deliverables-titles-and-write-observation.md) | Deliverables, session titles, and the write observation policy |
| [0029](0029-ordered-prompt-sections.md) | Ordered prompt sections with strict variable interpolation |
| [0030](0030-declared-tool-timeouts-and-deferred-loading.md) | Declared tool timeouts and deferred tool loading |
| [0031](0031-headless-exec-protocol.md) | Headless exec protocol (`--json`, `--output-schema`) |
| [0032](0032-layered-configuration-and-redaction.md) | Layered configuration, managed requirements, and redacted secrets |
| [0033](0033-apply-patch-envelope-tool.md) | The `apply_patch` envelope tool |
| [0034](0034-web-tools-and-ssrf-defense.md) | Web tools and the SSRF-safe fetch transport |
| [0035](0035-run-time-travel.md) | Run time travel: checkpoint fork and append-only revert |
| [0036](0036-plan-mode.md) | Plan mode: read-only until the plan is approved |
| [0037](0037-goals-and-ralph.md) | Goals and Ralph: a persisted objective and fresh-agent rounds |
| [0038](0038-seatbelt-sandbox.md) | macOS Seatbelt sandbox: a generated SBPL profile per session |
| [0039](0039-bubblewrap-sandbox.md) | Linux bubblewrap sandbox: the policy is the argument list |
| [0040](0040-landlock-planner.md) | Landlock: an ABI-aware planner plus a Linux-only applier |
| [0041](0041-seccomp-filter.md) | Seccomp: a planned BPF program, verified by a test-local interpreter |
| [0042](0042-restrict-then-exec-helper.md) | The Linux sandbox runs a restrict-then-exec helper (this binary) |
| [0043](0043-background-jobs.md) | Background jobs: detached processes with offset-addressed output |
| [0044](0044-hooks-engine.md) | Hooks: exit-code and JSON decisions, fail-open by default |
| [0045](0045-hooks-in-the-agent-loop.md) | Hooks in the agent loop: frozen run context and bounded Stop refusals |
| [0046](0046-landlock-file-rules.md) | Landlock rules for files, the safe device default, and syscall-based socket tests |
| [0047](0047-durable-memories.md) | Durable memories: readable files, content identity, frozen injection |
| [0048](0048-guardian-review.md) | A guardian review at the stop boundary: report by default, bounded enforcement |
| [0049](0049-commands-and-schedules.md) | Canned commands and unattended schedules: ordered expansion, policy-bound shell |
| [0050](0050-images-and-reasoning-replay.md) | Images as message content, and reasoning replayed with its signature |
| [0051](0051-seccomp-errno-and-unix-socketpairs.md) | A denied syscall returns EPERM, and AF_UNIX means socketpair |
| [0052](0052-pinning-linux-only-abi-numbers.md) | Linux-only ABI numbers are pinned on every platform |
| [0053](0053-mcp-server-mode-and-stdio-framing.md) | An MCP server, and the stdio framing the spec actually uses |
| [0054](0054-configured-mcp-servers-and-approval.md) | Configured MCP servers, gated by what the server declares |
| [0055](0055-mcp-stdio-dispatch-by-id.md) | The stdio client dispatches by id, so a deadline is real |
| [0056](0056-run-starting-mcp-tool-and-served-run-approval.md) | A run-starting MCP tool is gated three times, and a served run never prompts |
| [0057](0057-workflow-engine-javascript-sandbox.md) | The workflow engine is a JavaScript sandbox inside the Go process |
| [0058](0058-job-terminal-after-output-drain.md) | A job is terminal only after its output is drained |
| [0059](0059-workflow-tool-wiring.md) | A workflow script drives sub-agent runs through the orchestrator |
| [0060](0060-terminal-jobs-and-head-tail-buffers.md) | A terminal job is a session, and a bounded buffer keeps both ends |
| [0061](0061-mcp-per-server-time-bounds.md) | MCP server time bounds belong to the server entry |
| [0062](0062-standing-approval-is-a-rule.md) | A standing approval is a rule, not an argument string |
| [0063](0063-grants-list-and-revoke.md) | A standing grant an operator can see and take back |
| [0064](0064-a-child-can-name-its-model.md) | A child can run on a model it names |
| [0065](0065-a-structured-answer-is-checked.md) | A structured answer is checked, not trusted |
| [0066](0066-workflow-progress-is-its-own-event.md) | Workflow progress is its own event family |
| [0067](0067-a-served-run-can-outlive-its-call.md) | A served run can outlive the call that asked for it |
| [0068](0068-a-run-a-caller-can-stop.md) | A run a caller can stop |
| [0069](0069-a-schedule-outlives-the-process.md) | A schedule outlives the process that added it |
| [0070](0070-a-signed-webhook-starts-a-run.md) | A signed webhook starts a run |
| [0071](0071-resources-and-prompts-not-only-tools.md) | Resources and prompts, not only tools |

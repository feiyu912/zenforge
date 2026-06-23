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
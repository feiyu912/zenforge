# ZenForge Design Docs

Recommended reading order:

1. [vision.md](./vision.md)
2. [product-roadmap.md](./product-roadmap.md)
3. [preparation-plan.md](./preparation-plan.md)
4. [architecture.md](./architecture.md)
5. [mvp-scope.md](./mvp-scope.md)
6. [extraction-plan.md](./extraction-plan.md)
7. [current-project-mapping.md](./current-project-mapping.md)
8. [s1-durable-runtime-spec.md](./s1-durable-runtime-spec.md)
9. [s1-implementation-plan.md](./s1-implementation-plan.md)
10. [s2-tool-runtime-spec.md](./s2-tool-runtime-spec.md)
11. [s2-implementation-plan.md](./s2-implementation-plan.md)
12. [s3-safety-workspace-spec.md](./s3-safety-workspace-spec.md)
13. [s3-implementation-plan.md](./s3-implementation-plan.md)
14. [s4-minimal-harness-spec.md](./s4-minimal-harness-spec.md)
15. [s4-implementation-plan.md](./s4-implementation-plan.md)
16. [s5-planner-todo-spec.md](./s5-planner-todo-spec.md)
17. [s5-implementation-plan.md](./s5-implementation-plan.md)
18. [s6-approval-hitl-spec.md](./s6-approval-hitl-spec.md)
19. [s6-implementation-plan.md](./s6-implementation-plan.md)
20. [s7-subagent-runtime-spec.md](./s7-subagent-runtime-spec.md)
21. [s7-implementation-plan.md](./s7-implementation-plan.md)
22. [s8-sandbox-adapter-spec.md](./s8-sandbox-adapter-spec.md)
23. [s8-implementation-plan.md](./s8-implementation-plan.md)
24. [mvp-assembly-plan.md](./mvp-assembly-plan.md)
25. [mvp-validation.md](./mvp-validation.md)
26. [v0.1-release-plan.md](./v0.1-release-plan.md)
27. [cli-design.md](./cli-design.md)
28. [config-reference.md](./config-reference.md)
29. [quickstart.md](./quickstart.md)
30. [sdk-guide.md](./sdk-guide.md)
31. [provider-guide.md](./provider-guide.md)
32. [limitations.md](./limitations.md)
33. [release-checklist.md](./release-checklist.md)
34. [release-notes-v0.1.md](./release-notes-v0.1.md)
35. [sandbox-guide.md](./sandbox-guide.md)
36. [subagent-guide.md](./subagent-guide.md)
37. [approval-guide.md](./approval-guide.md)
38. [planner-guide.md](./planner-guide.md)
39. [checkpoint-resume-guide.md](./checkpoint-resume-guide.md)
40. [failure-modes.md](./failure-modes.md)
41. [harness-state-machine.md](./harness-state-machine.md)
42. [server-http-guide.md](./server-http-guide.md)
43. [server-sse-guide.md](./server-sse-guide.md)
44. [zenmind-adapter-guide.md](./zenmind-adapter-guide.md)
45. [mcp-adapter-guide.md](./mcp-adapter-guide.md)
46. [memory-adapter-guide.md](./memory-adapter-guide.md)
47. [trace-guide.md](./trace-guide.md)
48. [security-guide.md](./security-guide.md)
49. [tool-authoring-guide.md](./tool-authoring-guide.md)
50. [api-sketch.md](./api-sketch.md) historical draft

## Architecture Decision Records

- [ADR 0001: Event Log And Checkpoint Are Separate](./adr/0001-event-log-and-checkpoint.md)
- [ADR 0002: Public Event Contract](./adr/0002-public-event-contract.md)
- [ADR 0003: Tool Runtime And Middleware](./adr/0003-tool-runtime.md)
- [ADR 0004: Approval Broker](./adr/0004-approval-broker.md)
- [ADR 0005: Sub-Agent Orchestration](./adr/0005-sub-agent-orchestration.md)
- [ADR 0006: Package Layout](./adr/0006-package-layout.md)
- [ADR 0007: Run State Schema](./adr/0007-run-state-schema.md)
- [ADR 0008: Tool Result Contract](./adr/0008-tool-result-contract.md)
- [ADR 0009: Workspace And Policy Are Separate](./adr/0009-workspace-policy-separation.md)
- [ADR 0010: Shell Is Deny-By-Default](./adr/0010-shell-deny-by-default.md)
- [ADR 0011: Harness Receives Normalized Input](./adr/0011-harness-receives-normalized-input.md)
- [ADR 0012: Model Adapters Own Provider Protocol](./adr/0012-model-adapters-own-provider-protocol.md)
- [ADR 0013: Todo Tools Are Core](./adr/0013-todo-tools-are-core.md)
- [ADR 0014: Plan/Execute Is A Preset](./adr/0014-plan-execute-is-a-preset.md)
- [ADR 0015: Approval Is Core State](./adr/0015-approval-is-core-state.md)
- [ADR 0016: No Cross-Run Approval In MVP](./adr/0016-no-cross-run-approval-in-mvp.md)
- [ADR 0017: Sub-Agent Is A Runtime Tool](./adr/0017-subagent-is-runtime-tool.md)
- [ADR 0018: Nested Sub-Agents Disabled By Default](./adr/0018-nested-subagents-disabled-by-default.md)
- [ADR 0019: Sandbox Is An Adapter](./adr/0019-sandbox-is-adapter.md)
- [ADR 0020: No Silent Sandbox Fallback](./adr/0020-no-silent-sandbox-fallback.md)
- [ADR 0021: MVP Does Not Require Sub-Agents Or Sandbox](./adr/0021-mvp-does-not-require-subagents-or-sandbox.md)

## Current Direction

The current design bias is:

```text
durable runtime boundary first
tool runtime second
agent loop third
platform adapters last
```

This is deliberate. The existing ZenMind runtime already has useful execution
logic, but its persistence and orchestration are tied to platform concerns.
ZenForge should avoid inheriting that coupling.

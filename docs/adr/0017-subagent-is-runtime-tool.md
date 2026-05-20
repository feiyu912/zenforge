# ADR 0017: Sub-Agent Is A Runtime Tool

Status: proposed

## Context

Sub-agent delegation is exposed to the model like a tool call, but it is not a
normal backend tool. It starts child runs and routes events.

## Decision

ZenForge will expose sub-agent delegation as a runtime tool called `task`.

The tool call is intercepted by the harness and executed by
`SubAgentOrchestrator`.

## Consequences

Benefits:

- model UX remains simple;
- runtime can checkpoint child state;
- child events can be routed properly.

Costs:

- harness must know about runtime tools;
- task tool is more privileged than ordinary tools.


# ADR 0018: Nested Sub-Agents Disabled By Default

Status: proposed

## Context

Nested sub-agent delegation can explode cost, complexity, and checkpoint state.
The current platform blocks nested sub-agent invocation.

## Decision

ZenForge disables nested sub-agent calls by default.

Controlled nesting is available only through explicit host configuration with:

- max depth;
- per-call max tasks;
- explicit opt-in.

The default is `AllowNested = false` and `MaxDepth = 1`. Child agents inherit
sub-agent orchestration only while below an explicitly configured depth.
Model-facing task options cannot enable nesting or raise the host depth limit.

## Consequences

This keeps the default deterministic while allowing bounded orchestration for
hosts that deliberately opt in.

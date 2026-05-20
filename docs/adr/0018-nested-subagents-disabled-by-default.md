# ADR 0018: Nested Sub-Agents Disabled By Default

Status: proposed

## Context

Nested sub-agent delegation can explode cost, complexity, and checkpoint state.
The current platform blocks nested sub-agent invocation.

## Decision

ZenForge disables nested sub-agent calls by default.

Post-MVP may add controlled nesting with:

- max depth;
- max total child runs;
- budget caps;
- explicit opt-in.

## Consequences

This keeps MVP deterministic and easier to reason about.


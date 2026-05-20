# ADR 0011: Harness Receives Normalized Input

Status: proposed

## Context

`agent-platform` builds a rich `QuerySession` from catalog, memory, skills,
runtime paths, channels, chat history, and model config. That is appropriate for
the platform, but too coupled for ZenForge core.

## Decision

ZenForge harness receives normalized `RunConfig`.

Adapters are responsible for translating platform concepts into:

- instructions;
- messages;
- model;
- tools;
- metadata;
- policies.

The harness does not load catalogs, memory, skills, or chat summaries.

## Consequences

Benefits:

- harness stays small;
- SDK users can construct runs directly;
- ZenMind integration becomes an adapter, not a dependency.

Costs:

- platform adapters must do explicit translation;
- prompt assembly is not magically inherited from ZenMind.


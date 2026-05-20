# ADR 0009: Workspace And Policy Are Separate

Status: proposed

## Context

The current platform file tools combine useful ideas:

- path resolution;
- allowed roots;
- approval fingerprints;
- read-before-write snapshots;
- actual file reads/writes.

For ZenForge, workspace storage may be local disk, memory, sandbox, object
storage, or remote API. Policy should not be tied to local filesystem
implementation.

## Decision

ZenForge will separate:

- `workspace.Workspace`: storage operations;
- `policy.FilePolicy`: access decisions;
- `tools/workspace`: model-callable tools combining both.

## Consequences

Benefits:

- sandbox workspace can reuse file policy;
- memory workspace can be used in tests;
- policy can be tested without touching disk;
- tools stay small.

Costs:

- more interfaces;
- local workspace still needs careful path jail logic.


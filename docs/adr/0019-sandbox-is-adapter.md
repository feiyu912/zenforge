# ADR 0019: Sandbox Is An Adapter

Status: accepted

## Context

Sandbox execution is valuable, but requiring containers would make ZenForge
harder to use as a small Go SDK.

## Decision

ZenForge core defines sandbox interfaces. Container Hub and other sandboxes are
adapters.

Core must work without sandbox.

## Consequences

Benefits:

- local-only users can use ZenForge;
- Container Hub can evolve separately;
- other sandbox backends can be added.

Costs:

- shell tools need backend routing;
- some features require optional adapter config.

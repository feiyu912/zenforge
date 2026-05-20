# ADR 0021: MVP Does Not Require Sub-Agents Or Sandbox

Status: proposed

## Context

Sub-agents and sandbox execution are important differentiators, but they are not
required to prove the core harness.

The risky path is delaying MVP until every advanced feature is polished.

## Decision

MVP requires:

- durable runtime;
- tool runtime;
- safety/workspace;
- minimal harness;
- planner/todo;
- basic approval;
- CLI.

MVP does not require:

- sub-agents;
- Container Hub sandbox.

They can ship as experimental features or in V0.1.

## Consequences

This gives the project a smaller first finish line while keeping the roadmap
honest.


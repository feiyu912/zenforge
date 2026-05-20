# ADR 0014: Plan/Execute Is A Preset

Status: proposed

## Context

Not every agent needs planning. Some should be simple ReAct or one-shot agents.
But long-running tasks benefit from a structured plan/execute/summary flow.

## Decision

Plan/execute/summary is a preset, not the only runtime mode.

The core harness supports basic ReAct. The planner package composes the harness
into a staged workflow.

## Consequences

Benefits:

- simple agents stay simple;
- long-task agents get default structure;
- users can create custom presets later.

Costs:

- planner must maintain stage-specific prompts and tool restrictions;
- more tests are needed around stage transitions.


# ADR 0020: No Silent Sandbox Fallback

Status: proposed

## Context

If an agent expects sandbox isolation but silently falls back to local shell, the
user may execute risky commands on the host machine.

## Decision

When sandbox is required and unavailable, ZenForge returns an explicit error.

No silent fallback from sandbox to local execution is allowed.

## Consequences

This may interrupt some runs, but it prevents surprising host execution.


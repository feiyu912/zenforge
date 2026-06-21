# ADR 0010: Shell Is Deny-By-Default

Status: accepted

## Context

Shell tools are powerful and dangerous. The current platform has meaningful
bash security and approval logic. ZenForge should keep this seriousness from
the start.

## Decision

ZenForge shell execution is deny-by-default.

A shell command can run only when one of these is true:

1. it matches an explicit allowlist;
2. a command review marks it allowed;
3. an approval broker approves it.

If none apply, the shell tool returns an approval-required or blocked result.

## Default CLI Behavior

For local CLI MVP:

- common read-only commands may be preconfigured by templates;
- writes, deletes, network-sensitive commands, and unknown commands require
  approval;
- destructive commands should be blocked unless explicitly configured.

## Consequences

This makes first use slightly less magical, but it prevents ZenForge from
shipping an unsafe shell agent by default.

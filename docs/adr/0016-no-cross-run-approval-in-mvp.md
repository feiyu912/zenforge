# ADR 0016: No Cross-Run Approval In MVP

Status: proposed

## Context

Persistent approvals across runs are powerful but risky. They require storage,
identity, revocation, audit, and policy management.

## Decision

MVP approvals are scoped only to:

- once;
- current run;
- current rule within current run.

Cross-run approvals are post-MVP.

## Consequences

This keeps the first approval system safe and simple. Applications that need
global policy can implement a custom broker.


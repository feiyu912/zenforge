# ADR 0015: Approval Is Core State

Status: proposed

## Context

Approval affects execution. A run may pause before a tool call and resume after
a decision. If approval only exists in a UI layer, durable resume is impossible.

## Decision

Approval waiting and decision history are stored in `RunState`.

Approval UI/API protocols are adapters.

## Consequences

Benefits:

- approval survives checkpoint/load;
- CLI, server, and UI integrations share the same core;
- tool middleware can be tested without frontend infrastructure.

Costs:

- core state model grows;
- approval request schema must be generic enough for multiple UIs.


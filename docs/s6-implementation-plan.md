# S6 Implementation Plan

This document breaks `s6-approval-hitl-spec.md` into concrete work.

## 1. Approval Package

Create:

```text
approval/
```

Implement:

- `Request`;
- `Decision`;
- `Option`;
- `RiskLevel`;
- `DecisionAction`;
- `DecisionScope`;
- validation helpers.

Acceptance:

- JSON round-trip tests;
- validation tests.

## 2. Broker Interface And Built-Ins

Implement:

- `Broker`;
- always allow;
- always deny;
- timeout wrapper;
- channel broker.

Acceptance:

- deterministic tests for each broker.

## 3. CLI Broker

Create:

```text
approval/cli/
```

Implement simple terminal prompt:

- show title/description/risk;
- list options;
- read decision;
- return decision.

Acceptance:

- non-interactive tests through injected reader/writer.

## 4. Run State Integration

Update harness state helpers:

- set waiting approval;
- clear waiting approval;
- append resolved decision;
- terminal behavior on abort/cancel.

Acceptance:

- checkpoint tests verify waiting and resolved state.

## 5. Approval Middleware

Create tool middleware:

```text
tool/middleware/approval.go
```

Acceptance:

- middleware pauses before invocation;
- approved decision calls tool;
- rejected decision skips tool;
- abort decision returns run cancellation signal.

## 6. Event Integration

Emit:

- `approval.requested`;
- `approval.resolved`;
- `approval.expired`.

Acceptance:

- event order tests with fake recorder.

## 7. Policy Integration Hooks

Define a generic approval plan type used by S3 file/shell policy:

```go
type Plan struct {
    Required bool
    Request  approval.Request
}
```

Acceptance:

- shell/file policy can produce plan without depending on broker internals.

## Done Criteria

- `go test ./...` passes;
- fake tool approval flow works end-to-end;
- approval waiting state is durable;
- CLI broker is usable for MVP CLI.


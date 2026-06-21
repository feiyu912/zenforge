# ADR 0008: Tool Result Contract

Status: accepted

## Context

Tool results must serve two audiences:

1. the model, which needs a concise text result;
2. the application, which may need structured data.

The existing platform has `ToolExecutionResult` with `Output`, `Structured`,
`Error`, and `ExitCode`. This shape is useful and should be retained in a
cleaner public form.

## Decision

ZenForge tool results will have both text and structured channels.

```go
type Result struct {
    Output     string
    Structured map[string]any
    Error      string
    ExitCode   int
    Metadata   map[string]any
}
```

## Rules

- `Output` is what goes back into model messages by default.
- `Structured` is preserved for SDK/event consumers.
- `Error` is a stable machine-readable error code when possible.
- `ExitCode` follows shell convention where useful.
- Middleware may truncate `Output`, but should preserve full structured
  references when safe.

## Consequences

This avoids forcing every tool into either raw text or arbitrary JSON. It also
matches the current platform's proven shape while removing platform DTOs.

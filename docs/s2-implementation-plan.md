# S2 Implementation Plan

This document breaks `s2-tool-runtime-spec.md` into concrete work.

## 1. Reshape Existing `tool` Package

Current package already has a small interface. Expand it into:

```text
tool/interface.go
tool/definition.go
tool/registry.go
tool/invoker.go
tool/middleware.go
tool/errors.go
```

Acceptance:

- existing root package compiles;
- no public API depends on old placeholder-only types.

## 2. Define Errors

Add:

- `ErrToolNotFound`;
- `ErrDuplicateTool`;
- `ErrInvalidArguments`;
- `ErrTimeout`;
- `ErrBudgetExceeded`;
- `ErrOutputTooLarge`.

Acceptance:

- errors are comparable with `errors.Is`;
- tests cover common cases.

## 3. Implement Registry

Build a simple in-memory registry.

Acceptance:

- register/lookup;
- duplicate detection;
- stable definitions order;
- nil tool rejected.

## 4. Implement Invoker

Invoker responsibilities:

- lookup tool;
- emit call event;
- execute tool;
- normalize result;
- emit result/error event.

Acceptance:

- fake tool call test;
- missing tool test;
- fake event sink receives events.

## 5. Middleware Chain

Implement:

- chain builder;
- timeout middleware;
- retry middleware;
- max output middleware;
- panic recovery middleware.

Acceptance:

- middleware order is deterministic;
- timeout uses context;
- retry does not retry context cancellation;
- panic becomes tool error.

## 6. Typed Tool Helper

Add package:

```text
tools/
  typed.go
```

Acceptance:

- supports `func(context.Context, In) (Out, error)`;
- decodes JSON;
- encodes struct/map/string output;
- rejects unsupported signatures.

## 7. Basic Schema Inference

Add:

```text
tool/jsonschema/infer.go
```

Acceptance:

- struct fields become object properties;
- `json` tags respected;
- `jsonschema:"required"` respected;
- primitives mapped to JSON schema types.

## 8. Documentation

Update:

- `api-sketch.md`;
- `architecture.md`;
- add a short tool authoring example.

## Done Criteria

- `go test ./...` passes;
- S2 tests cover registry/invoker/middleware/typed helper;
- tool runtime can be used independently in a small example.


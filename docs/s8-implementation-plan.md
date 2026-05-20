# S8 Implementation Plan

This document breaks `s8-sandbox-adapter-spec.md` into concrete work.

## 1. Sandbox Package

Create:

```text
sandbox/
```

Implement:

- `Sandbox`;
- `Session`;
- `OpenRequest`;
- `ExecuteRequest`;
- `ExecuteResult`;
- `Mount`;
- error codes.

Acceptance:

- package compiles;
- JSON round-trip tests for metadata structs.

## 2. Fake Sandbox

Implement a fake sandbox for tests.

Acceptance:

- records open/execute/close calls;
- configurable failures;
- used by shell backend tests.

## 3. Shell Sandbox Backend

Extend shell tool backend options:

- local;
- sandbox.

Acceptance:

- command routed to fake sandbox;
- sandbox unavailable error tested;
- no fallback when sandbox required.

## 4. Environment Prompt Provider

Implement interface and fake provider.

Acceptance:

- prompt section can be fetched by environment ID;
- errors are explicit.

## 5. Container Hub Client

Create:

```text
sandbox/containerhub/
```

Implement:

- create session;
- execute session;
- stop session;
- environment prompt;
- runtime info.

Acceptance:

- httptest validates method/path/body/header;
- text vs JSON response handling tested.

## 6. Adapter

Implement `sandbox.Sandbox` backed by Container Hub client.

Acceptance:

- open/execute/close through fake HTTP server;
- auth header passed;
- timeout configured.

## 7. Resume State

Add `SandboxState` to run state metadata or typed state.

Acceptance:

- checkpoint round-trip includes session ID;
- resume can reuse session metadata.

## 8. Documentation

Add setup guide:

- local shell vs sandbox shell;
- Container Hub config;
- environment prompt;
- fallback rules.

## Done Criteria

- `go test ./...` passes;
- fake sandbox tests pass;
- Container Hub adapter tests pass;
- core packages do not depend on Container Hub.


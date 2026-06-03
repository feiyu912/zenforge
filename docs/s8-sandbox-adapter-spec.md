# S8 Sandbox Adapter Spec

S8 adds sandbox execution as an optional backend.

The goal is to let shell and workspace tools run inside an isolated environment
without making Container Hub or any container runtime a core dependency.

## S8 Outcome

After S8, ZenForge should support:

- sandbox interface;
- sandbox session lifecycle;
- local shell vs sandbox shell routing;
- Container Hub adapter;
- environment prompt provider;
- run/subtask session isolation;
- sandbox execution events;
- clear fallback behavior when sandbox is unavailable.

## Design Principles

1. Core must work without sandbox.
2. Sandbox is a tool backend, not a mandatory runtime.
3. Container Hub is an adapter.
4. Session identity must include run/subtask scope.
5. Environment prompts are adapter-provided prompt sections.
6. Sandbox failures should be explicit and recoverable where possible.

## Package Plan

```text
sandbox/
  interface.go
  session.go
  prompt.go

sandbox/containerhub/
  client.go
  adapter.go
  config.go

tools/shell/
  local.go
  sandbox.go
```

## Core Interfaces

### Sandbox

```go
type Sandbox interface {
    Open(ctx context.Context, req OpenRequest) (*Session, error)
    Execute(ctx context.Context, session *Session, req ExecuteRequest) (ExecuteResult, error)
    Close(ctx context.Context, session *Session) error
}
```

### OpenRequest

```go
type OpenRequest struct {
    RunID         string
    SubtaskID     string
    EnvironmentID string
    WorkingDir    string
    Env           map[string]string
    Mounts        []Mount
    Metadata      map[string]any
}
```

### Session

```go
type Session struct {
    ID            string
    RunID         string
    SubtaskID     string
    EnvironmentID string
    WorkingDir    string
    Metadata      map[string]any
}
```

### ExecuteRequest

```go
type ExecuteRequest struct {
    Command   string
    CWD       string
    Timeout   time.Duration
    Env       map[string]string
    Metadata  map[string]any
}
```

### ExecuteResult

```go
type ExecuteResult struct {
    ExitCode         int
    Stdout           string
    Stderr           string
    WorkingDirectory string
    Metadata         map[string]any
}
```

## Environment Prompt Provider

Some sandbox environments need prompt context:

- available binaries;
- default cwd;
- mounted paths;
- network policy;
- environment limitations.

Interface:

```go
type EnvironmentPromptProvider interface {
    Prompt(ctx context.Context, environmentID string) (Prompt, error)
}

type Prompt struct {
    EnvironmentID string
    Content       string
    Metadata      map[string]any
}
```

The harness should accept this as a prompt section from an adapter. Core prompt
assembly should not call Container Hub directly.

## Session Identity

Session keys:

```text
main run:       run-{runID}
child subtask:  run-{runID}-{subtaskID}
```

This mirrors the useful isolation pattern in `agent-platform`.

Rules:

- main agent shares one sandbox session per run;
- each child sub-agent gets a separate subtask-scoped session by default;
- session reuse is allowed within the same run/subtask;
- session close is best-effort.

## Shell Routing

Shell tool can run through:

- local backend;
- sandbox backend.

Config:

```go
type ShellBackend string

const (
    ShellBackendLocal   ShellBackend = "local"
    ShellBackendSandbox ShellBackend = "sandbox"
)
```

If sandbox is required but unavailable:

- return `sandbox_unavailable`;
- do not silently fall back to local shell.

If sandbox is optional:

- application may choose fallback policy.

## Container Hub Adapter

Container Hub adapter maps:

- `Open` -> `POST /api/sessions/create`;
- `Execute` -> `POST /api/sessions/{id}/execute`;
- `Close` -> `POST /api/sessions/{id}/stop`;
- `Prompt` -> `GET /api/environments/{name}/agent-prompt`;
- runtime info -> `GET /api/runtime-info`.

Adapter config:

```go
type Config struct {
    BaseURL       string
    AuthToken     string
    Timeout       time.Duration
    DefaultEnvID  string
}
```

## Mounts

```go
type Mount struct {
    Source      string
    Destination string
    Mode        string
}
```

Mounts are adapter-level configuration. Core should treat them as opaque
sandbox metadata.

## Events

Sandbox adapter can emit or attach metadata to:

- `sandbox.opened`;
- `sandbox.execute.started`;
- `sandbox.execute.done`;
- `sandbox.closed`;
- `tool.result`;
- `tool.error`.

For public MVP, sandbox events can be trace events rather than root event types
if the event contract needs to stay small.

## Checkpoint Behavior

Run state should store sandbox session metadata:

```go
type SandboxState struct {
    SessionID     string `json:"sessionId"`
    EnvironmentID string `json:"environmentId"`
    WorkingDir    string `json:"workingDir,omitempty"`
}
```

Resume behavior:

- if session is still alive, reuse;
- if not alive, open a new session;
- if command was running during crash, do not assume it completed;
- resume from checkpoint before command execution where possible.

`sandbox.StateFromSession` and `sandbox.SessionFromState` provide the small
conversion boundary between adapter session handles and checkpoint metadata.
Sandbox tools pass this state through `sandbox.MetadataStateKey`; the harness
copies successful tool metadata into `RunState.Sandbox` and injects checkpointed
sandbox state into later tool calls.

## Failure Behavior

Common errors:

- `sandbox_unavailable`;
- `environment_not_found`;
- `session_open_failed`;
- `sandbox_execute_failed`;
- `sandbox_timeout`;
- `sandbox_closed`.

Tool results should include structured metadata for these failures.
For shell tools this includes `backend: "sandbox"` and `sandboxError` with the
stable sandbox error code when one is available.

## Migration From agent-platform

Source inspiration:

- `agent-platform/internal/contracts.SandboxClient`;
- `agent-platform/internal/sandbox/client.go`;
- `agent-platform/internal/sandbox/service.go`;
- `agent-container-hub/internal/sandbox/session_service.go`;
- environment prompt behavior in Container Hub.

Keep:

- HTTP adapter idea;
- run/subtask isolation;
- environment prompt;
- no silent fallback when sandbox required.

Change:

- adapter lives outside core;
- no dependency on ZenMind `config.Config`;
- no direct prompt builder calls from sandbox adapter;
- no platform path assumptions.

## S8 Tests

Minimum tests:

- fake sandbox open/execute/close;
- shell tool routes to sandbox backend;
- sandbox required unavailable returns error;
- session key includes run/subtask;
- session state helpers round-trip metadata;
- environment prompt provider returns prompt section;
- Container Hub client request shaping with httptest;
- resume reuses existing session metadata where possible.

## S8 Exit Criteria

- local runtime works without sandbox dependency;
- sandbox shell backend works with fake sandbox;
- Container Hub adapter compiles and is tested with fake HTTP server;
- no Container Hub dependency in core harness;
- docs explain setup and fallback behavior.

# Sandbox Guide

This guide covers sandbox-backed shell execution and sandbox prompt
augmentation for ZenForge host applications.

## Why Use A Sandbox

Use a sandbox when tools need to run commands or inspect files in an isolated
environment.

Good use cases:

- code analysis;
- test execution;
- package installation;
- risky shell commands;
- separate environments per sub-agent.

## Local vs Sandbox Shell

Local shell:

- simpler;
- faster;
- useful for trusted local workflows.

Sandbox shell:

- isolated;
- environment controlled;
- better for untrusted or repeatable runs.

## Configuration Sketch

For local Docker:

```go
sbox, err := docker.New(docker.Config{DefaultImage: "alpine:3.20"})
if err != nil {
    return err
}
root, err := filepath.Abs("./repo")
if err != nil {
    return err
}
shellTool, err := shell.New(shell.Config{
    Policy: policy.ShellPolicy{
        WorkingDir:      root,
        RequireApproval: true,
        MaxTimeout:      30 * time.Second,
        MaxOutputBytes:  256_000,
    },
    Backend:       shell.ShellBackendSandbox,
    Sandbox:       sbox,
    EnvironmentID: "alpine:3.20",
    Mounts: []sandbox.Mount{{
        Source: root, Destination: "/workspace", Mode: "ro",
    }},
})
```

`Policy.WorkingDir` is a host path because shell policy validation happens
before the backend call. The Docker adapter maps it through the configured
mount to `/workspace`.

The Docker defaults disable networking, use a read-only root filesystem, drop
capabilities, enable `no-new-privileges`, and bound process count and output.
They never fall back to host execution.

For a remote Container Hub:

```go
sbox, err := containerhub.New(containerhub.Config{
    BaseURL: "http://127.0.0.1:11960",
    AuthToken: os.Getenv("CONTAINER_HUB_TOKEN"),
    DefaultEnvID: "toolbox",
})
if err != nil {
    return err
}

shellTool, err := shell.New(shell.Config{
    Policy: policy.ShellPolicy{
        WorkingDir:      "./repo",
        AllowCommands:   []string{"go test ./...", "go vet ./..."},
        RequireApproval: true,
        MaxTimeout:      30 * time.Second,
        MaxOutputBytes:  256_000,
    },
    Backend:       shell.ShellBackendSandbox,
    Sandbox:       sbox,
    EnvironmentID: "toolbox",
})
if err != nil {
    return err
}
```

For a real Hub acceptance run, set its endpoint and execute the opt-in adapter
test. It creates and closes a disposable session in the selected environment:

```bash
ZENFORGE_CONTAINERHUB_INTEGRATION_URL=http://127.0.0.1:11960 \
ZENFORGE_CONTAINERHUB_ENVIRONMENT=shell \
go test ./sandbox/containerhub -run '^TestAdapterRunsAgainstRealContainerHub$' -v
```

Set `ZENFORGE_CONTAINERHUB_TOKEN` when the Hub requires bearer authentication.

## Session Scope

ZenForge uses one sandbox session for the main run and separate sessions for
child sub-agent tasks. Session IDs are created by `sandbox.SessionKey`, which
trims both inputs and encodes each non-empty component as its Base64URL length,
a hyphen, and the unpadded Base64URL value. Call the helper rather than
constructing IDs manually; the length prefixes prevent component-boundary
collisions.

```text
sandbox.SessionKey("run_1", "")       = "run-7-cnVuXzE"
sandbox.SessionKey("run_1", "task_1") = "run-7-cnVuXzE-8-dGFza18x"
```

## Resume Metadata

Sandbox adapters can persist session metadata in checkpoints using
`sandbox.StateFromSession` and rebuild a session reference with
`sandbox.SessionFromState` during resume:

```go
state := sandbox.StateFromSession(session)
session = sandbox.SessionFromState(state, runID, subtaskID)
```

The rebuilt session is only a reference to an existing sandbox session. If the
backend reports that it no longer exists, the host adapter should open a new
session before executing more work.

Checkpointed sandbox state includes the owning `runId` and `subtaskId`.
`SessionFromState` refuses to restore state into a different scope. Legacy
state without scope identity is treated as non-restorable and causes a fresh
session to be opened.

When `KeepSessionOpen` is false, shell execution closes the session on a
best-effort basis and does not return that session as reusable metadata. A
close failure cannot replace a successful command result. The shell result
uses `sandbox.MetadataClearStateKey`, and the harness removes any previously
checkpointed session before the next boundary.

## Environment Prompt

Sandbox environments may provide prompt context describing:

- installed tools;
- default working directory;
- mount points;
- network limits;
- execution hints.

Applications can inject this as a prompt section.

```go
augmenter := sandboxadapter.Augmenter{
    Provider:      sbox,
    EnvironmentID: "toolbox",
}
task, prompt, err := augmenter.AugmentTask(ctx, zenforge.Task{
    Input: "Run the test suite and summarize failures.",
})
if err != nil {
    return err
}
_ = prompt
```

The adapter prepends a `Sandbox environment` section to the normalized task and
records prompt provenance under `task.Meta["sandbox.prompt"]`. Core prompt
assembly still does not call Container Hub directly.

## Fallback Rules

If sandbox is required and unavailable, ZenForge must not silently fall back to
local shell.

The run should return an explicit error such as:

```text
sandbox_unavailable
```

Container Hub transport deadlines use `sandbox_timeout`; parent context
cancellation remains `context.Canceled`. Neither condition falls back to local
shell.

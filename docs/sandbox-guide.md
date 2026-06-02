# Sandbox Guide

This is a draft user-facing guide for sandbox execution.

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

```go
sbox := containerhub.New(containerhub.Config{
    BaseURL: "http://127.0.0.1:11960",
    AuthToken: os.Getenv("CONTAINER_HUB_TOKEN"),
    DefaultEnvID: "toolbox",
})

shell := shell.New(shell.Config{
    Backend: shell.BackendSandbox,
    Sandbox: sbox,
})
```

## Session Scope

ZenForge uses one sandbox session for the main run and separate sessions for
child sub-agent tasks.

```text
main run:      run-{runID}
child task:    run-{runID}-{subtaskID}
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

## Environment Prompt

Sandbox environments may provide prompt context describing:

- installed tools;
- default working directory;
- mount points;
- network limits;
- execution hints.

Applications can inject this as a prompt section.

## Fallback Rules

If sandbox is required and unavailable, ZenForge must not silently fall back to
local shell.

The run should return an explicit error such as:

```text
sandbox_unavailable
```

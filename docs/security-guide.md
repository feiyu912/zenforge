# Security Guide

This is a draft security guide for ZenForge users and contributors.

## Default Posture

ZenForge should be useful, but conservative.

Default rules:

- shell execution is deny-by-default;
- workspace access is root-bounded;
- writes require explicit write roots;
- risky operations can require approval;
- tool output is capped;
- secrets should be redacted from events and traces.

## Workspace Safety

Local workspace tools must:

- resolve paths under a configured root;
- block `..` traversal;
- block symlink escape;
- block device files;
- enforce read/write roots;
- cap read and write sizes;
- record read snapshots before writes where enabled.

Recommended configuration:

```go
workspace := local.New(local.Config{
    Root: "./repo",
    MaxReadBytes:  1_000_000,
    MaxWriteBytes: 1_000_000,
})
```

## Shell Safety

Shell tool must:

- require a command description;
- validate cwd;
- enforce timeout;
- cap output;
- filter env vars;
- review command risk;
- require approval for unknown or risky commands.

Recommended configuration:

```go
shell := shell.New(shell.Config{
    WorkingDir: "./repo",
    AllowCommands: []string{
        "go test ./...",
        "go vet ./...",
        "grep",
        "find",
    },
    Timeout: 30 * time.Second,
    MaxOutputBytes: 256_000,
})
```

## Approval

Risky operations should return or emit an approval request with:

- operation type;
- command or file path;
- reason;
- risk;
- fingerprint;
- proposed scope.

Applications decide how to surface the request:

- CLI prompt;
- web UI;
- API callback;
- always-deny policy;
- pre-approved policy.

## Tracing And Secrets

Events and checkpoints are durable. Treat them as sensitive.

Guidelines:

- redact API keys and tokens;
- avoid writing full environment variables;
- truncate large command output;
- store artifact references instead of large contents;
- document where event logs and checkpoints are stored.

## What ZenForge Does Not Guarantee

ZenForge can provide safe defaults and hooks, but application owners remain
responsible for:

- choosing allowed commands;
- protecting checkpoint storage;
- controlling network access;
- managing credentials;
- reviewing custom tools;
- sandboxing untrusted workloads.


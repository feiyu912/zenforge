# Security Guide

This guide describes the current security posture for ZenForge users and
contributors.

## Default Posture

ZenForge should be useful, but conservative.

Default rules:

- shell execution is deny-by-default;
- workspace access is root-bounded;
- writes are bounded by the configured workspace root;
- risky operations can require approval;
- tool output is capped;
- secrets should be redacted from events and traces.

`Config.ToolArgumentRedaction` and `tool.RedactArguments` redact tool-call event
projections recursively. Checkpoints still retain original tool arguments
because deterministic resume must be able to retry a pending call. Hosts must
protect checkpoint storage and should pass secret references instead of raw
long-lived credentials.

## Workspace Safety

Local workspace tools must:

- resolve paths under a configured root;
- block `..` traversal;
- block symlink escape;
- block device files;
- enforce the configured workspace root;
- cap read and write sizes;
- record read snapshots before writes where enabled.

Recommended configuration:

```go
workspace, err := local.New(local.Config{
    Root:            "./repo",
    MaxReadBytes:    1_000_000,
    MaxWriteBytes:   1_000_000,
    CreateParentDir: true,
})
if err != nil {
    return err
}
```

## Shell Safety

Shell tool must:

- require a command description;
- validate cwd;
- enforce timeout;
- cap output;
- filter env vars;
- review command risk;
- require approval for unknown or risky commands;
- block shell control operators such as `&&`, `;`, pipes, and redirects before
  allowlist matching.

Recommended configuration:

```go
shellTool, err := shell.New(shell.Config{
    Policy: policy.ShellPolicy{
        WorkingDir: "./repo",
        AllowCommands: []string{
            "go test ./...",
            "go vet ./...",
            "grep",
            "find",
        },
        RequireApproval: true,
        MaxTimeout:      30 * time.Second,
        MaxOutputBytes:  256_000,
    },
})
if err != nil {
    return err
}
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

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

The local workspace blocks reads through escaping symlinks and blocks writes to
an existing final symlink that resolves outside the workspace root. Reads reject
non-regular files and grep skips non-regular files. File snapshots include
SHA256 for regular files, and workspace tool snapshots are scoped to the current
run ID.

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

Workspace tools can add a policy layer before adapter access:

```go
workspaceTools, err := workspacetools.Tools(workspacetools.Config{
    Workspace:              workspace,
    RequireReadBeforeWrite: true,
    Snapshots:              workspacetools.NewSnapshotStore(),
    Policy: policy.FilePolicy{
        ReadRoots:       []string{"."},
        WriteRoots:      []string{"docs", "generated"},
        RequireApproval: true,
    },
})
```

Paths outside configured roots are denied by default, or returned as approval
requests when `RequireApproval` is set. Approval reuse is matched by the file
fingerprint or root rule key carried in approval metadata.
Text reads reject the platform's known binary extensions before loading file
contents and also detect NUL bytes. Grep skips those extensions before opening
them. Hosts may explicitly set `AllowBinaryRead` only when their consumer can
safely handle arbitrary bytes.

## Shell Safety

Shell tool must:

- require a command description;
- validate cwd;
- enforce timeout;
- cap output;
- filter env vars;
- review command risk;
- require approval for unknown or risky commands;
- parse commands with the Bash AST safety layer before allowlist matching;
- require every AST-parsed command in chains and substitutions to match the
  allowlist instead of trusting the raw command prefix;
- require approval for output redirections and statically unresolved targets;
- block sensitive input redirections, dangerous builtins, ambiguous syntax,
  dangerous wrapper commands, and dangerous embedded interpreter scripts;
- route unsupported-but-not-hard-blocked syntax to approval, or deny it when
  approval is disabled.

AST-safe quoted data such as `echo 'a|b'` is distinguished from a real pipeline.
Legacy ambiguity checks remain intentionally conservative for forms such as
quoted semicolons. Parser precheck failures for control characters, Unicode
whitespace, brace expansion, and similar ambiguity are hard blocks.

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

Approval metadata matching is exact: a replayed call must carry an approved
decision action plus either the same fingerprint or the same rule key. Write
approval fingerprints include the target path and content SHA256.
Broker decisions must also carry the exact request ID. Generic approval
middleware validates that identity and any reusable scope key before retrying
the protected tool.

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

## Served Host Authentication

`zenforge serve` can put a caller-identity boundary in front of every route it
serves — the console's `/api/*`, the harness run routes, `/api/settings` (which
holds the provider credential), `/api/server`, both stream paths and the signed
webhook (ADR 0141). It is off by default on loopback and required by
`--allow-remote` unless the operator passes `--allow-anonymous-remote`. There is
deliberately no loopback exemption when the requirement is on: a reverse proxy
on the same machine reaches this host as a loopback peer, so "the peer is
loopback" is not by itself a security property.

A host started with `--console=off` (ADR 0144) has fewer surfaces under that
same boundary: no console shell or assets, no `/api/*` namespace, no WebSocket
mux, and no `/api/settings` -- because that route is the console's settings
document API, and the settings document is what fields the provider credential
from the page. The harness run routes, `/api/server` and the sign-in routes
remain, and a path the host does not serve answers `404` with the code
`console_disabled` rather than an empty success. A headless host is configured
with `--provider`, `--model`, `--api-key` and `--base-url` only, refuses to start
without a model, and refuses `--settings-file` rather than ignoring it.

Callers present a token as `Authorization: Bearer <token>` or as the
`zenforge_session` cookie the `/auth` sign-in page sets. The cookie is
`HttpOnly`, `SameSite=Strict`, and `Secure` only when configured; its value is
the token itself, so the host keeps no session table and revoking the token ends
the browser session on its next request. A refused request gets `401` with
`WWW-Authenticate: Bearer` and the host's usual JSON error envelope.

Tokens are stored hashed. `zenforge token create` is the only code that produces
a plaintext token and it prints that plaintext once; the token file keeps only
`sha256:` hashes and is refused at open unless it is readable only by its owner
(the refusal names `chmod 600`). Tokens do not expire. A running host re-reads
the token file every two seconds, so a `zenforge token revoke` made in another
process takes effect without a restart.

Every admission decision — allow or refuse — is appended to the audit trail
named by `--audit-log`, which defaults to `audit.jsonl` beside the token file
whenever authentication is required. A line records the time, the decision, the
reason, the method, the path, the status the caller got, the remote address, and
the tenant, subject and token id when one resolved. It never records a request
body, a header value or a credential, and the path is recorded without its query
string. Both secret-bearing files are refused inside the workspace this host
serves, because the console reads workspace files back to the browser (ADR 0089).

The boundary authenticates and attributes; it does not partition the console's
data plane, which stays host-global. See [Limitations](limitations.md) for the
shared surfaces and the one process per tenant interim guidance.

## What ZenForge Does Not Guarantee

ZenForge can provide safe defaults and hooks, but application owners remain
responsible for:

- choosing allowed commands;
- protecting checkpoint storage;
- controlling network access;
- managing credentials;
- reviewing custom tools;
- sandboxing untrusted workloads.

# ADR 0042: A Restrict-Then-Exec Helper For The Linux Sandbox

Status: accepted

## Context

Landlock (ADR 0040) and seccomp (ADR 0041) are each planned as data on
any platform and applied only on Linux, and both share one structural
property: the restriction is installed by the *calling* process and
inherited by its children. Neither can be wrapped around a command from
outside. A `sandbox.Sandbox` backend, by contrast, is called from outside:
`Execute(ctx, session, request)` wants to run a command in a boundary.

The reference resolves this with a helper binary: the Codex executable
re-invokes itself with `CODEX_LINUX_SANDBOX_ARG0`, parses the policy from
its own arguments, installs Landlock and seccomp, and execs the command.

## Decision

### The backend runs a helper, and the helper is this binary

`sandbox/linuxsandbox` provides a `sandbox.Sandbox` adapter that, for each
command, spawns `zenforge linux-sandbox --policy <json> -- <shell> -c
<command>` in the requested working directory. The helper:

1. parses `--policy`, `--arch`, and the command after `--`;
2. decodes the policy with `DisallowUnknownFields`, so a policy the helper
   does not fully understand is an error rather than a partially applied
   sandbox;
3. probes the Landlock ABI and plans **both** layers;
4. applies Landlock, then seccomp, then execs.

The subcommand is hidden from the usage text but present in the dispatch
switch, so a single binary is both the agent and its own confinement
helper. That is the reference's shape and it avoids shipping a second
executable that must be kept in sync.

### Fail before applying half a sandbox

Both layers are planned before either is applied. A policy that cannot be
expressed — most importantly a read-only carve-out inside a writable root,
which Landlock cannot represent — fails at planning time, while the
process is still unrestricted and the error is a clean message rather than
a half-confined command. The adapter additionally refuses such a policy at
construction time, so a misconfigured deployment fails at startup instead
of at the first shell call.

### The policy travels as JSON, and is hashed

The policy is JSON on the helper's command line and is recorded in the
session metadata together with its SHA-256, so a run can be tied to the
policy that governed it. Unknown fields are rejected: silently ignoring a
field the caller believes is in force is the one failure mode a sandbox
must not have.

### Test without Linux, and test the real thing on Linux

The adapter takes an injectable `Runner`, so the helper argument shape —
policy JSON, `--arch`, `--`, shell, `-c`, command, working directory,
environment — and every error mapping (exit code, timeout, oversized
output, closed session, missing helper, off-platform refusal) are unit
tested here on macOS. `RunHelper` takes injectable confinement steps for
the same reason, and a helper-process test exercises the real Landlock and
seccomp system calls on a Linux kernel, where the helper re-execs the test
binary and the child applies real restrictions.

## Consequences

Benefits:

- the whole C5 sandbox stack is now reachable from configuration:
  `--sandbox landlock` selects Landlock plus seccomp, `--sandbox bwrap`
  selects bubblewrap, `--sandbox seatbelt` selects Seatbelt, and
  `--sandbox docker` selects a container;
- one binary, no second artifact to install or version-skew;
- the policy is auditable in the process table and in session metadata, and
  the helper can be run by hand for debugging;
- the security-critical part is tested on the host that can run it, and the
  interface part is tested everywhere.

Costs and limits:

- one extra process per command (the helper execs, so it is not a
  persistent wrapper);
- the helper must be reachable at the same path; `Config.Helper`
  overrides it, and a missing helper fails closed with
  `sandbox_unavailable`;
- a `Runtime`-wide view: the helper inherits the environment, so the
  adapter forwards the session and call environments explicitly rather than
  relying on the parent;
- the helper has no timeout of its own: the adapter's context kills it,
  and the child dies with it because it is killed before exec completes or
  inherits the process group.

## Alternatives Rejected

### Restrict The Agent Process In-Process

Then the *agent* is confined: it could no longer write its checkpoints,
and the restriction would outlive the command with no way to relax it
(neither Landlock nor seccomp can be removed). The exact opposite of what a
per-command sandbox means.

### Ship A Separate `zenforge-linux-sandbox` Binary

The reference does this because of its build layout; for a single Go module
it means a second artifact that can be missing or stale relative to the
agent, which is a worse failure mode than one extra process. The helper is
reachable by path and fails closed when it is not.

### Pass The Policy Through The Environment

Arguments are visible in the process table and easy to log; an environment
variable is easy to lose across a shell re-exec, and a lost policy in a
sandbox means a silently unrestricted command. The policy goes in the
arguments, and the adapter records it.

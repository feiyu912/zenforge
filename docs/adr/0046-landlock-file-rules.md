# ADR 0046: Landlock Rules For Files, And The Safe Device

Status: accepted

## Context

The Landlock plan granted `AllAccessAt(abi)` to every rule, including rules
for single files such as `/dev/null` listed in `ReadWritePaths`. On Linux
this fails: a `LANDLOCK_RULE_PATH_BENEATH` rule whose file descriptor points
at a non-directory may only carry the rights that apply to files
(`EXECUTE`, `READ_FILE`, `WRITE_FILE`, and, from ABI 3 and 5, `TRUNCATE` and
`IOCTL_DEV`). Carrying any directory right — `READ_DIR`, `REMOVE_*`,
`MAKE_*`, `REFER` — makes the kernel reject the rule with `EINVAL`.

The consequence was severe in exactly the way this project tries to avoid:
the whole ruleset failed to apply, so the helper exited before running the
command. It was caught by CI on Linux, not by the local macOS test run,
because the planner compiles everywhere but only a Linux kernel can reject
the rule.

A second, related gap: a plan that granted read access to `/` but no write
access to `/dev/null` made ordinary commands fail. `/bin/sh -c 'cat f
>/dev/null'` dies with "cannot create /dev/null: Permission denied", which
means a sandbox that is technically correct can still be unusable.

## Decision

### File rules carry only file rights

`landlock.FileAccessAt(abi)` returns the file-applicable mask, and `Build`
applies it to any rule whose path exists and is not a directory. Directory
rules keep the full mask. The applied mask is part of the ruleset
fingerprint, so a run's recorded policy still identifies exactly what was
enforced.

### `/dev/null` is granted read-write by default

`Build` adds a read-write rule for `/dev/null` when the path exists and the
caller did not already list it, matching the reference's Landlock plan
(`add_rules(path_beneath_rules(&["/dev/null"], access_rw))`). A sandbox that
cannot write there breaks most shell commands; making it a default removes a
footgun without widening access to anything but the null device. The path is
*not* added when `FullDiskRead` is false and the caller deliberately wants a
closed filesystem? It is still added: the null device is not a data path,
and the reference grants it unconditionally as well.

### The Linux-only tests prove the syscall, not the shell

The socket-denial tests used `exec 3<>/dev/tcp/...`, a bash extension. On a
host where `/bin/sh` is dash the redirection fails before any syscall, so
the assertion could pass without exercising the filter at all. Both tests
now re-exec the test binary under the sandbox and have *it* call
`net.Dial`, reporting a verdict the parent asserts on. The check is now a
real `socket(2)` denial, independent of which shell the host provides.

## Consequences

Benefits:

- the ruleset applies on kernels where it previously failed, so the helper
  actually runs the sandboxed command instead of exiting;
- `cmd >/dev/null` works inside the sandbox;
- the socket tests cannot pass vacuously on a dash-based host;
- the file/directory distinction is explicit in the planner and covered by
  a test asserting the file mask never contains directory rights.

Costs and limits:

- `Build` now stats the paths it plans, so a policy that names a
  not-yet-existing device path gets the directory mask; the caller should
  name paths that exist (which is how the CLI builds them);
- the default `/dev/null` rule is one more line in every fingerprint;
- Landlock still has no per-path carve-outs (ADR 0040), so bubblewrap
  remains the backend for protected-name policies.

## Alternatives Rejected

### Grant Nothing And Let The Shell Fail

That is the state CI caught: a sandbox whose ruleset cannot be applied is
worse than no sandbox in the specific sense that the user believes the
command was confined.

### Grant The Full Mask And Ignore The Kernel Error

Ignoring `EINVAL` per rule would leave the policy silently narrower than
declared — the same class of bug. The plan is what it says it is, or it is
an error.

### Keep The Shell-Based Socket Test And Require Bash

Depending on the host's shell for a security assertion makes the test's
meaning depend on the host. Re-execing the test binary uses the same
mechanism the production helper uses: install the filter, then exec.

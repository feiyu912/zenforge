# ADR 0038: macOS Seatbelt As A Generated Profile, Not A Command Wrapper

Status: accepted

## Context

ZenForge's shell tool can escalate to a sandbox, and the `sandbox.Sandbox`
interface already has Docker and container-hub backends, but both require
a container runtime. Codex ships a macOS backend built on
`sandbox-exec`/Seatbelt, which needs nothing installed and therefore works
on the developer machines where most agent runs actually happen.

Seatbelt is unusual among sandboxing technologies in that its policy is a
*program* (SBPL) compiled by the kernel's policy compiler. That shape
creates three specific temptations that this ADR records decisions about:
paraphrasing the reference's policy text, interpolating host paths into
the policy, and expressing "this subpath is read-only" as a deny rule.

## Decision

### Port the reference's policy files verbatim

`seatbelt_base_policy.sbpl`,
`seatbelt_read_only_platform_defaults.sbpl`, and
`seatbelt_network_policy.sbpl` are embedded byte-for-byte (minus one
backtick that would have ended the Go raw string). They are long, and they
look like boilerplate, but each rule is an empirically discovered
allowance: a permissive-looking `sysctl-name-prefix` is what lets a
runtime detect CPU features, and a missing Mach lookup makes a process
abort rather than fail cleanly. Rewording a rule is not a refactor; it is
a different policy.

The dynamic part — caller read roots, writable roots, protected metadata,
explicit read-only paths, network, extra rules — is generated around them.

### Paths travel as `-D` parameters, never as policy text

Every host path becomes a named parameter (`WRITABLE_ROOT_0`,
`READABLE_ROOT_2`, `PROTECTED_0_1`, …) bound with
`sandbox-exec -D KEY=value`. The profile text contains no host path, so a
path containing SBPL syntax (a quote, a paren, a newline) cannot change
the meaning of the policy. A test asserts that the generated profile
contains none of the inputs it was built from.

### Read-only inside a writable root is an exclusion on the allow rule

This is the subtle one, and the integration test caught the bug. The
natural encoding looks like:

```
(deny file-write* (require-all (subpath ROOT) (require-not (subpath GIT))))
```

but a filter describes the set of operations a rule matches, and
`require-not` *subtracts* from it — so this rule denies exactly the writes
that are not under `.git`: the entire root becomes unwritable. The correct
form is the reference's, an exclusion on the allow rule:

```
(allow file-write* (require-all (subpath ROOT)
                                (require-not (literal PROTECTED))
                                (require-not (subpath PROTECTED))))
```

Because the profile is closed by default, a path that is not allowed is
denied, so one allow rule expresses both the grant and the carve-out. Both
`literal` and `subpath` are needed: `subpath` alone still permits
first-time creation of the protected directory itself.

`(deny file-write-unlink (require-all (literal ROOT) (vnode-type
DIRECTORY)))` is kept as a deny, because it has no negative filters and it
protects the root *anchor* that the next policy will be built from.

### Canonicalize every path

The kernel matches resolved vnodes, so a policy written for `/tmp` does
not cover `/private/tmp`, and one written for `/var/folders/...` does not
cover the temp directory a test actually writes through. Every root,
read path, and working directory is canonicalized with the longest
existing ancestor resolved (a path that does not exist yet — a read-only
path, or a file the run will create — still lines up with its root's
canonical form). The reference canonicalizes for the same reason.

### A writable root is readable, and its ancestors are readable

A workspace must be readable as well as writable, so each writable root is
added to the read allowance, and `path-ancestors` is allowed for it so a
tool can determine its own working directory. Without the latter, `/bin/sh`
fails `getcwd` in a temp directory and git cannot resolve the repository
it is standing in.

### The adapter refuses to run unsandboxed

`Open` returns `sandbox.ErrSandboxUnavailable` when the host is not darwin
or when the runner cannot be found, rather than silently executing
commands without a policy. A test seam (`Runner`, `LookPath`,
`SkipPlatformCheck`) lets the lifecycle, error mapping, and argument
construction be tested on any host, while the enforcement test runs
against the real kernel compiler on darwin.

## Consequences

Benefits:

- a working macOS sandbox with no runtime dependency, usable as the
  escalation target for the shell tool;
- the policy is auditable as data: parameters are visible in the process
  arguments and the profile on the session metadata, along with its
  SHA-256;
- `.git` and the ZenForge config directory cannot be rewritten by a
  sandboxed command, which is what makes the sandbox reusable across
  rounds;
- the enforcement test (write inside allowed, write outside denied,
  `.git` denied, loopback unreachable) runs against the real policy
  compiler, so a policy regression fails the suite on darwin rather than
  silently loosening.

Costs and limits:

- the profile is macOS-specific; Linux (bubblewrap + seccomp, Landlock)
  and Windows backends remain unported, so the escalation ladder still
  needs a container runtime on those hosts;
- a sandboxed process still inherits the host's toolchain and network
  stack — Seatbelt constrains filesystem and network syscalls, not the
  contents of the workspace, so a malicious repository can still run
  arbitrary code with the user's read access;
- the base policy is permissive by design (a toolchain needs it) and is
  therefore not a confidentiality boundary against a determined attacker,
  only a write and network boundary;
- paths in `read-only` and `protected` positions are canonicalized but not
  checked for symlink swaps after policy construction; a root that is
  replaced mid-session is outside the threat model (and the anchor deny
  blocks the most direct form of it);
- the profile is rebuilt per `Open` and stored on the session, so changing
  configuration mid-run requires a new session, which is the intended
  semantics.

## Alternatives Rejected

### Generate The Policy From Scratch

A minimal profile is more readable, but every missing rule is a mysterious
abort or a confusing `Operation not permitted` in an unrelated tool. The
reference's policy is the result of that debugging, so porting it verbatim
and layering caller-specific rules on top is both cheaper and safer.

### Interpolate Paths Into The Profile

Simpler, and it is what a first implementation does. It also means a path
containing `"` or `)` can inject rules. Parameters cost one extra
`-D` argument each and remove the class of bug.

### Encode Protected Paths As Deny Rules

Tested, believed correct, and wrong (see above): it denies the complement
of what was intended. Any future read-only carve-out added to this
generator must be an exclusion on the corresponding allow rule.

### Soft-Fail Off macOS And Run The Command Anyway

A sandbox that silently does not apply is worse than no sandbox: the caller
believes a boundary exists. `ErrSandboxUnavailable` makes the escalation
ladder fall back explicitly instead.

### Require A Container Runtime On macOS Too

Docker on macOS is a VM with slower I/O and an install requirement;
Seatbelt is the native boundary and the reference's default. Keeping both
available lets the caller choose, and the interface is already shared.
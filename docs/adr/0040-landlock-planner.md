# ADR 0040: Landlock As A Planner Plus A Linux-Only Applier

Status: accepted

## Context

Landlock is an unprivileged Linux security module that lets a process
restrict *itself*. It is the other half of the reference's Linux sandbox:
bubblewrap (ADR 0039) owns namespaces and mount layout, while Landlock
provides an in-process filesystem boundary and seccomp covers the network.

Three properties of Landlock shape any port of it:

- **The rights are versioned by ABI.** `REFER` appeared in ABI 2,
  `TRUNCATE` in ABI 3, `IOCTL_DEV` in ABI 5. Requesting a right the
  running kernel does not know makes ruleset creation fail, and the older
  a kernel is the less it can be asked to handle.
- **The restriction cannot be imposed from outside.** It is installed on
  the calling process and inherited by its children, so the process that
  execs the sandboxed command must install it, or a helper must do so on
  the command's behalf. There is no "run this command under this ruleset"
  call.
- **Rights are additive and there is no deny rule.** For a path the kernel
  unions every matching rule. A read-only carve-out *inside* a writable
  root is therefore inexpressible: the root's rule already grants write.

The last two points mean the interesting work is not the syscalls — those
are mechanical — but deciding what a policy may ask for and what must be
refused, and making that decision verifiable without a Linux host.

## Decision

### Split the planner from the applier

`sandbox/landlock/ruleset.go` is platform independent: it computes the
access mask for a given ABI, plans the per-path rules, and decides which
policies are expressible. `enforce_linux.go` (build-tagged) performs
`landlock_create_ruleset`, `landlock_add_rule`, `prctl(PR_SET_NO_NEW_PRIVS)`,
`landlock_restrict_self`, and `Exec`; `enforce_other.go` reports
`ErrUnsupported` from the same API.

The arithmetic is the security-relevant part, and it therefore runs under
test on every platform — including this project's macOS development
machines — while the syscall layer is cross-compiled for both Linux
architectures (`GOOS=linux go vet` and `go test -c`) so a type error or a
missing constant cannot survive.

### Fail closed on a policy Landlock cannot express

`Build` rejects a policy that asks for protected names inside writable
roots with `ErrUnsupportedCarveOut`, whose message says why: Landlock
unions matching rules and has no deny rule. The alternative — ignore the
carve-out and grant the write rights anyway — would hand back a sandbox
that is *wider* than the policy promised while reporting success, which is
the worst possible failure for a security boundary. The caller is told to
use the bubblewrap or Seatbelt backend for such a policy.

### Follow the reference's read/write split

The reference grants whole-filesystem **read** access (`EXECUTE`,
`READ_FILE`, `READ_DIR`) plus read-write on `/dev/null` and on each
writable root. The same shape is planned here, with two additions:
`FullDiskRead: false` plans a genuinely restrictive layout (read on the
declared roots, nothing else visible — inexpressible with the reference's
rules), and `ReadWritePaths` names individual files such as `/dev/null`.
Every planned rule is still granted at most the rights the ABI supports.

### Handle every right the ABI supports

`HandledAccess` covers all rights available at the ABI, not only the
granted ones. A right that is not handled is not merely ungranted, it is
unrestricted, so handling the full set is what makes "not granted anywhere"
mean "denied".

## Consequences

Benefits:

- the security arithmetic is unit-tested on any host, and the syscall
  layer is compile-verified for Linux;
- a policy that Landlock cannot express fails loudly instead of silently
  widening;
- ABI-aware masks make the same code correct on old and new kernels;
- complementary to bubblewrap rather than a replacement: Landlock needs no
  binary, no user namespace, and works on kernels where unprivileged user
  namespaces are disabled.

Costs and limits:

- no per-path read-only carve-outs (the point above);
- Landlock restricts the filesystem only: no network namespace, no PID
  namespace, no capability drop. A full Linux sandbox is bubblewrap plus
  Landlock plus a seccomp filter, and this package supplies one of the
  three;
- the shell-tool binding is still outstanding: `sandbox.Sandbox` opens a
  session and executes a command from outside, which is exactly the shape
  Landlock forbids, so wiring it up requires a helper process that applies
  the ruleset and execs (the reference's `codex-linux-sandbox`). That is
  recorded as remaining work rather than faked with an in-process
  restriction that would confine the *agent* instead of the command;
- the ABI is read at runtime and the plan is built for `MaxSupportedABI`
  by callers that do not pass the probed version; a plan built for a newer
  ABI than the kernel has fails at ruleset creation, which is loud rather
  than silent.

## Alternatives Rejected

### Apply The Ruleset In-Process Without A Helper

That would restrict the agent process itself — the agent could no longer
write its own checkpoints, and the restriction would persist after the
command finished. Landlock is not a wrapper; using it as one is a bug.

### Ignore Unsupported Carve-Outs

Silently dropping a read-only carve-out grants more access than requested
while reporting a confined shell. Failing closed costs a clear error
message and prevents a security regression that no test would catch.

### Hard-Code The ABI 5 Access Mask

Then ruleset creation fails with `E2BIG` on older kernels, or the sandbox
silently handles fewer rights than it believes. Deriving the mask from the
probed ABI keeps one code path correct across kernel versions.

### Skip The Package Off Linux

The ruleset is the policy, and policy bugs are platform independent; a
macOS-only developer should be able to change a Landlock policy and see
the test fail. Only the applier is Linux-specific.
# ADR 0052: Linux-Only ABI Numbers Are Pinned On Every Platform

Status: accepted

## Context

CI has twice failed on tests that only run on Linux, and both times the
cause was a number a macOS development host never executes:

- `ErrnoEPERM` held 22 (`EINVAL`) instead of 1 (`EPERM`), so a denied
  syscall reported "invalid argument";
- a test asserted a unix socket **listener** works, when the policy keeps
  `socketpair(2)` and denies `bind`/`listen`/`connect` by design.

Neither was a flaky test. Both were wrong statements about the Linux ABI or
about the policy, and both were unverifiable locally by construction: the
package's tables and the sandbox itself only exist on Linux.

The general shape of the risk is worth stating plainly. A wrong syscall
number or Landlock access bit in a security filter does not fail loudly: the
program denies the wrong syscall, or grants a *different right* than the one
intended, and the sandbox still installs successfully.

## Decision

### Hand-written tables are restated and checked by a second statement

`sandbox/seccomp` carries hand-written syscall tables for amd64 and arm64,
and `sandbox/landlock` carries hand-written access bits. Both are now
restated independently in tests — `expectedSyscalls` with all 24 names per
architecture, and the bit table via `accessByABI` — and compared in both
directions, so a wrong number, a missing name, and an unexpected extra name
all fail. A table checked only against itself proves nothing.

### The host table is cross-checked against x/sys on Linux

`abi_linux_amd64_test.go` and `abi_linux_arm64_test.go` compare the table for
the running architecture against `golang.org/x/sys/unix`'s `SYS_*` values,
and the audit architecture against `unix.AUDIT_ARCH_*`. On a Linux runner
that is the real ABI — the kernel headers as vendored by the Go project —
not a copy of the table under test, and CI covers whichever architecture it
runs on. The Landlock bits are cross-checked the same way against
`unix.LANDLOCK_ACCESS_FS_*`.

### Portable tests carry the semantics; Linux tests remain the confirmation

The classic BPF program is evaluated in-process (allow/deny/kill, `AF_UNIX`
vs `AF_INET`, cross-architecture, and the exact `SECCOMP_RET_ERRNO|EPERM`
return value), and the numeric constants are pinned by value on any platform.
The on-kernel tests still exist and are still the real evidence; they are no
longer the *only* evidence, which is what made two CI round trips necessary.

## Consequences

Benefits:

- a wrong number in either table fails on the development host in under a
  second, instead of on the Linux runner after a push;
- the numbers that reach the kernel are stated twice, independently, so a
  silent policy hole has to survive two contradicting statements;
- Landlock access bits — where a wrong bit grants a *different* right rather
  than merely denying the wrong syscall — are covered by the same discipline;
- the minimum-ABI column of the Landlock rights table is checked for
  distinctness, coverage of all 16 rights, and the correct ABI generation
  (2 for `REFER`, 3 for `TRUNCATE`, 5 for `IOCTL_DEV`).

Costs and limits:

- every new syscall or right now has to be added in two places, and the test
  failure for forgetting is deliberate;
- the cross-check files are duplicated per architecture (the `SYS_*`
  identifiers only exist for the architecture being compiled), so a third
  architecture means a third file;
- the numbers are still restated from the kernel ABI by hand; the only
  authoritative check is the Linux cross-check, which requires CI to run on
  the architecture in question.

## Alternatives Rejected

### Generate The Tables From The Kernel Headers

Correct in principle, and rejected for this project: it adds a build step
that only runs on Linux, which is the situation being fixed, and the Go
toolchain's own tables (`golang.org/x/sys/unix`) are already the vendored
kernel headers — which is why the Linux tests compare against them.

### Keep The Tables Behind A Linux-Only Test

That is what produced both CI failures. A number nothing checks on the
development host is a number that is only checked in production.

### Drop The Linux-Only Tests And Trust The Portable Evaluator

The evaluator verifies the program, not the kernel's agreement with it; a
kernel that refuses to install the filter, or an errno the runtime wraps
differently, would go unnoticed.

### Derive The Tables From `unix.SYS_*` Directly

The package deliberately compiles on non-Linux hosts (it plans a filter as
data), and `unix.SYS_*` is unavailable there. More importantly, deriving the
table from the toolchain would remove the independent statement the test
needs.

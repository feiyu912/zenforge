# ADR 0041: Seccomp As A Planned Filter, Not A Syscall Wrapper

Status: accepted

## Context

Bubblewrap (ADR 0039) gives a Linux sandbox namespaces and a mount
layout, and Landlock (ADR 0040) adds an in-process filesystem boundary.
Neither can stop a process from opening a socket: bubblewrap can unshare
the network namespace (which removes connectivity but also every local
socket), and Landlock has no network rules at all. A network boundary that
still allows Unix sockets — needed by language servers and by any
toolchain that manages subprocesses — therefore needs seccomp.

Seccomp has the same structural constraint as Landlock: the filter is
installed by the calling process and inherited by its children, so it
cannot be wrapped around a command from outside. Unlike Landlock its
program is a bytecode program, and a bytecode program is exactly the kind
of artifact that can look right and be wrong.

The reference's filter is instructive in three ways: the default action is
allow and matched rules return `EPERM` (so a toolchain sees a permission
error rather than a crash); `recvfrom` is deliberately *not* denied,
because tools such as `cargo clippy` use a Unix socketpair for subprocess
management; and `io_uring` is denied unconditionally because it can create
sockets without ever calling `socket(2)`, which would bypass the network
rules.

## Decision

### Plan the program as data, apply it on Linux

`sandbox/seccomp/filter.go` builds the classic BPF program on any
platform: the architecture guard, one unconditional deny per syscall, the
domain check for `socket`/`socketpair` that keeps `AF_UNIX` and denies
every other domain, a trailing allow return, and an `EPERM` return that
every deny jumps to. `enforce_linux.go` installs it
(`PR_SET_NO_NEW_PRIVS`, then `PR_SET_SECCOMP` with `SECCOMP_MODE_FILTER`)
and execs; `enforce_other.go` reports `ErrUnsupported`.

### Verify the program's semantics, not just its shape

A filter that compiles is not a filter that works. Two defenses:

- **An interpreter in the test suite** runs the generated program against
  modelled `seccomp_data` (architecture, syscall number, `arg0`) and
  asserts allow/deny/kill for representative inputs, including the
  `AF_UNIX` versus `AF_INET` distinction and the cross-architecture case.
  This is what actually pins down jump directions — the bug this package's
  first draft contained was a domain check that jumped to `EPERM` when the
  domain *was* `AF_UNIX`.
- **Architecture-first guard.** The first check compares
  `seccomp_data.arch` against the architecture the table was built for and
  kills the process on a mismatch. Without it, a filter built from the
  amd64 table would be applied to a process running under another ABI and
  would deny and allow the wrong numbers.

### Refuse to guess syscall numbers

Only amd64 and arm64 tables are shipped, because those are the
architectures this project builds and can cross-compile for; a hand-copied
table for an architecture nobody can test is a silent hole. `ExtraDeny`
accepts a name for the syscalls the filter knows, or a bare decimal number
for anything else, so a caller can extend the policy without the package
inventing a name-to-number mapping it cannot verify.

### The probe has no side effects

A seccomp filter cannot be removed, so probing by installing one would
permanently restrict (and permanently set no-new-privileges on) the
calling process. `Available` therefore reads
`/proc/sys/kernel/seccomp/actions_avail` and requires the `errno` action,
which answers the question without touching the process.

## Consequences

Benefits:

- the network half of the Linux sandbox is planned as reviewable data and
  tested on every platform, including a real semantic simulation;
- Unix sockets keep working, so subprocess-based tooling does not break;
- io_uring and ptrace denial close two documented bypasses;
- the default is allow-with-`EPERM`-for-denied rather than kill, so a
  denied syscall surfaces as a normal permission error;
- classic BPF jumps are 8-bit, and `Build` fails loudly if a program would
  need a longer jump instead of silently truncating an offset.

Costs and limits:

- still Linux-only in application, and still needs a restrict-then-exec
  helper to confine a command (shared with Landlock; tracked as remaining
  work);
- `Error`-based denial for `socket` means a program that polls can retry;
  this is a boundary against accidental network use, not against a
  determined attacker with code execution — the reference's proxy-routed
  mode exists for that and is not ported;
- two architectures only, by explicit choice;
- the filter is planned for a named architecture rather than probed from
  the running kernel, so a caller that passes the wrong name gets a filter
  that kills every syscall (loud) rather than one that silently fails
  open.

## Alternatives Rejected

### Kill On Denied Syscalls

Simpler to reason about, but it turns "this tool tried to make a socket"
into a mysterious crash instead of a permission error. The reference's
`EPERM` choice is friendlier and equally effective for accidental access.

### Deny `recvfrom` With The Rest

It is the one intentional asymmetry in the reference's list, and for a
good reason: denying it breaks subprocess management over a Unix
socketpair, and `recvfrom` alone cannot reach the network.

### Deny Every Socket Family Including Unix

Then a confined toolchain that spawns subprocesses over a socketpair
breaks, and callers switch the sandbox off to get work done — a worse
outcome than allowing the local-only domain. Callers who want it can set
`AllowUnixSockets` to false.

### Build The Table At Runtime From The Kernel

There is no portable kernel interface that enumerates syscall names. The
reference hard-codes numbers too; the honest response is a small, verified
table plus a numeric escape hatch.

# ADR 0051: The Denied Syscall Returns EPERM, And AF_UNIX Means socketpair

Status: accepted

## Context

CI caught two things the local macOS run could not: the Linux-only socket
tests failed, reporting

```
ip-socket=error:dial tcp 127.0.0.1:1: socket: invalid argument
unix-socket=error:listen unix /tmp/...sock: setsockopt: invalid argument
```

Both messages say *invalid argument*. The filter was refusing the calls, but
with the wrong errno: `ErrnoEPERM` held **22**, which is `EINVAL`. `EPERM` is
**1**. A mislabelled errno is invisible on a host without seccomp, and on
Linux it makes a deliberate refusal look like a broken probe.

The second message exposed a wrong expectation in the test as well. The
policy keeps `AF_UNIX` for subprocess tooling, but it denies `bind`, `listen`,
and `connect` unconditionally — so a unix socket **listener** is refused by
design, exactly as the reference refuses it. The capability that is kept is
`socketpair(2)`, which is how a toolchain manages child processes; a listener
was never part of it, and the test asserted the wrong capability.

## Decision

### The errno is EPERM, spelled out and pinned

`ErrnoEPERM` is `1`, with the number written out in the comment and pinned by
`TestErrnoEPERMIsTheLinuxValue`, which also checks that
`RetErrno(ErrnoEPERM)` is `0x00050001`: the `SECCOMP_RET_ERRNO` action with
`EPERM` in the low bits. A constant like this cannot be verified by reading
it, and it cannot be verified on the development host, so it needs a test
that asserts the number rather than the name.

### Denial is asserted by errno, not by "an error happened"

The on-kernel tests report the errno they saw (`ip-socket=errno:EPERM`) and
the parent asserts that exact marker. The previous marker (`denied`) accepted
any failure whose text mentioned "operation not permitted", which is how a
refusal with the wrong errno slipped through review; and a looser check
("some error") would pass even with a broken probe.

### The boundary is pinned on any platform by evaluating the program

`TestFilterDecisionsForTheSocketBoundary` runs the classic BPF program in the
test-local interpreter and asserts the exact return value for each case:
`AF_UNIX` `socket`/`socketpair` allow, `AF_INET`/`AF_INET6` deny, `connect`,
`bind`, `listen`, `accept`, `sendto`, `setsockopt` deny, `recvfrom` allow,
`ptrace` and `io_uring_setup` deny, an unrelated syscall allow, and a foreign
architecture `SECCOMP_RET_KILL_PROCESS`. The Linux runtime test remains the
real confirmation, but the semantics are now checkable on macOS, which is
where the change is written.

### AF_UNIX means socketpair, and the comments say so

The probes are `socketpair(AF_UNIX)` (must succeed) rather than a unix
listener (must fail), and the filter comments state that `bind`, `listen`,
and `connect` stay denied even for `AF_UNIX` so nothing can turn an inherited
descriptor into a listening or dialing endpoint.

## Consequences

Benefits:

- a denied syscall now reports `EPERM`, so an operator sees "operation not
  permitted" rather than "invalid argument" for a policy decision;
- the test can no longer pass on a refusal with the wrong errno, and it
  distinguishes `EPERM` from `EINVAL` explicitly;
- the socket boundary's semantics are verified on a non-Linux development
  host, which shortens the loop that produced this bug;
- the documented capability matches the implemented one: socketpair, not a
  listener.

Costs and limits:

- a denied `setsockopt` still breaks Go's unix-socket **listener** path (it
  is denied deliberately, matching the reference); a tool that needs to
  listen on a unix socket cannot run under this sandbox, and that limitation
  is now stated instead of implied;
- the runtime test is still Linux-only, so the errno constant would have to
  be re-pinned by the unit test if the kernel ABI changed (it will not);
- the errno for other deny paths (Landlock, bwrap) comes from the kernel and
  is not normalized, so error handling that matches on errno must stay
  per-layer.

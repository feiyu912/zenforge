# ADR 0039: Linux Sandboxing As An Argument List

Status: accepted

## Context

The Seatbelt backend (ADR 0038) covers macOS, but Linux is where most
CI and container workloads run, and codex's Linux sandbox is built on
bubblewrap (`bwrap`): unprivileged user namespaces, bind mounts, and a
mount namespace that starts from a read-only or empty root. Unlike
Seatbelt, the policy is not a program — it is a list of command-line
arguments — which changes what "faithful port" and "testable" mean.

Two details of the reference are load-bearing and easy to miss:

- bubblewrap cannot bind a mount target that does not exist, so a
  configured path that is absent must be skipped rather than passed
  through, or every command fails at startup with an opaque error;
- mount order is the policy. A later bind wins over an earlier one, which
  is how `.git` is made read-only inside a writable root and how a nested
  writable root is reopened after its parent's protections.

## Decision

### The policy is data: build the argument list, then execute it

`sandbox/bwrap` separates `BuildArgs` (pure: policy plus command plus cwd
in, argv out) from the adapter that runs it. The full layout is therefore
auditable in a test on any host, including this project's macOS
development machines, and a policy regression is a failing assertion
rather than a Linux-only integration mystery. The real enforcement test
still exists, but it is a second line of defense, not the first.

### Ordering encodes the policy

`BuildArgs` emits, in order:

1. `--new-session --die-with-parent` (a new session, and no orphaned
   sandbox after the agent dies);
2. the root: `--ro-bind / /` under `FullDiskRead`, or `--tmpfs /` plus the
   approved read roots and the ported platform defaults (a coding agent
   wants the first; a strict policy wants the second);
3. `--dev /dev` and `--bind-try /dev/shm` after the root, so explicit
   `/dev` binds stay visible;
4. writable roots, **shallowest first**, so a nested root is bound after
   the parent and therefore wins;
5. `--ro-bind-try` for each explicit read-only path, after the writable
   binds, so read-only wins where they overlap;
6. `--ro-bind` over each protected name (`.git`, `.zenforge`) that exists;
7. `--unshare-user --unshare-pid --unshare-ipc --cap-drop ALL`, then
   `--unshare-net` unless network was requested, a fresh `--proc`, and
   `--chdir` to the canonical working directory;
8. `--` and the command.

### Paths are canonicalized and missing targets are skipped

Every path is canonicalized (longest existing ancestor, as in the Seatbelt
backend) because bubblewrap mounts what the kernel resolves, and a bind of
`/tmp/x` against `/private/tmp/x` is a bind of a different path. A writable
root that does not exist yet is dropped instead of failing the session, so
a mixed-platform configuration stays usable. A protected name that does
not exist yet is skipped — and this is a real, documented limitation: a
command can create `.git` inside a writable root before it exists, because
bubblewrap has nothing to bind over. Once it exists, the next execution
pins it.

### Seccomp is out of scope here

The reference applies a syscall filter through a separate `linux-sandbox`
helper process. Bubblewrap itself does not install filters, and a Go
program cannot install one for a child without either a helper binary or
cgo. This backend therefore enforces filesystem and network boundaries
(namespaces, bind mounts, capability drop) and inherits the host's syscall
surface. That is a deliberate, recorded gap rather than an oversight: the
parity plan tracks it as remaining work.

## Consequences

Benefits:

- Linux confinement with no daemon and no image: unprivileged user
  namespaces plus bind mounts;
- the layout is testable and reviewable as data, and the hash of the
  argument list is recorded on the session so a run can be tied to the
  policy that governed it;
- the same `sandbox.Sandbox` interface as the Docker and Seatbelt
  backends, so a host can choose per platform without changing the shell
  tool;
- fail-closed: off Linux, or without `bwrap` on `PATH`, `Open` returns
  `sandbox_unavailable` rather than running unsandboxed.

Costs and limits:

- no seccomp filter, so the syscall surface is the host's (documented
  above);
- `--unshare-user` requires unprivileged user namespaces, which some
  distributions disable or restrict (AppArmor profiles, `kernel.unprivileged_userns_clone=0`);
  in that case `bwrap` fails and the caller sees an execute error rather
  than a silent fallback;
- protected metadata that does not exist yet can be created inside a
  writable root (bubblewrap cannot mask a missing target);
- a read-only bind of the host root exposes the whole filesystem to
  reading, so this is a write and network boundary, not a confidentiality
  boundary;
- the adapter rebuilds the layout per `Open`; changing configuration
  mid-session requires a new session.

## Alternatives Rejected

### Run Bubblewrap And Let It Fail On Missing Paths

It works until a configuration mentions a directory that does not exist on
this host, at which point every command fails with a bubblewrap usage
error that looks like a policy bug. Filtering existing targets (and
recording which were dropped) keeps mixed-platform configuration usable.

### Bind Writable Roots In Configuration Order

Then a nested writable root's protections depend on the order the caller
happened to write them, which is exactly the kind of ordering the mounts
must not depend on. Sorting by depth makes the outcome deterministic:
broader first, narrower last, so the narrow grant wins.

### Apply Landlock Instead Of Bubblewrap

Landlock is in-process and needs no `bwrap` binary, which is attractive,
but it restricts only filesystem access, cannot create a network
namespace, and — in Go — requires raw syscalls in a Linux-only helper
because the restriction must be applied by the process that execs the
command. It is complementary (a possible additional layer), not a
replacement; the reference uses bubblewrap for the sandbox and Landlock
where an in-process layer is wanted.

### Soft-Fail Off Linux

A sandbox that silently does not apply is worse than no sandbox: the
caller believes a boundary exists. The adapter refuses to open a session
instead, and the escalation ladder falls back explicitly.
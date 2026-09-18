# ADR 0060: A Terminal Job Is A Session, And A Bounded Buffer Keeps Both Ends

Status: accepted

## Context

C2 is the reference's unified execution model: one interface that starts a
command, feeds it input, reads its output in bounded windows, and stops it —
including *interactively*, on a pseudo-terminal, where a program sees a
terminal instead of a pipe. ZenForge already had most of the model in
`jobs/`: session ids (`job_N`), stdin writes, offset-addressed output reads,
per-stream caps, a running-jobs cap, timeouts, kill, and the terminal-after-
drain ordering of ADR 0058. The tools in `tools/jobs` expose all of it.

Two things were genuinely missing. Nothing anywhere in the repository used a
PTY, so a command that checks `isatty`, prompts for input, pages, or colours
its output could not be driven at all. And a bounded output buffer kept only
the newest bytes: a job that flooded its output lost the first line — the
banner, the startup error, the port it bound — which is exactly the part a
reader needs when the job later misbehaves.

## Decision

### A terminal is a spec flag, and the manager owns the master

`Spec.PTY` runs the command on a pseudo-terminal (`github.com/creack/pty`,
pinned at v1.1.24) with `Spec.Rows`/`Spec.Cols` (default 24x80, validated to
1..1000 and rejected when a size is given without `pty`). A terminal has one
stream, so the job's stdout receives the merged output and the stderr view
stays empty and zero-total — stated in the tool description rather than
discovered by a reader wondering where stderr went.

The manager records the terminal environment: a `TERM` is added when the
resolved environment has none (an explicit host or spec environment is never
overridden), because a terminal job without `TERM` gets a degraded `dumb`
terminal and colours and cursor control disappear.

Writes go to the master, which *is* the session's stdin, and `write_stdin`
needs no PTY-specific path. Reading the master reports `EIO` once the child is
gone, so the copy loop treats that as the end of output — the same drain signal
the pipes give — and ADR 0058's ordering is unchanged: the job still becomes
terminal only after the drain (or after `DrainGrace`, or after the kill closes
the reader).

### Killing a terminal job signals its process group

A PTY child starts a new session, so it is a process-group leader and
`kill(-pid)` reaches everything it spawned. Killing only the shell is not
enough: a foreground child that ignores `SIGHUP` — which is what a terminal
sends its foreground group when the session leader exits — keeps the terminal
open, and the job then settles only when the drain grace expires, with the
survivor still running. The whole terminal path is Unix-only
(`//go:build unix`, where the pseudo-terminal device and process groups exist);
off Unix `pty: true` is refused with "terminal jobs are not supported on this
platform" instead of silently running the command on pipes while reporting a
terminal.

### A bounded buffer keeps the head and the tail, and measures the hole

`Buffer` now retains the first bytes of a stream and its newest bytes:
`headCap = min(capacity/4, 8KiB)` and the rest is the tail. Offsets stay
absolute and monotonic, and `Chunk.Elided` says how many bytes the reader
skipped, so a reader that polled from the start gets the head, and the read
that continues past it is told exactly what it missed instead of silently
receiving a hole. `Read` never returns a hole it has not flagged: it answers
from the head, then jumps to the oldest tail byte with `Dropped` and `Elided`
set.

The tools surface that as `elidedBytes` and an `output-dropped(N)` footer, so
a model — and a host reading the tool result — can tell "this is everything"
from "this is the ends of something larger".

This changes the documented behaviour of a flooded buffer (previously the
oldest bytes were dropped and `Read(0)` answered from the newest tail); the
buffer test was rewritten to the new contract, and `TestOutputReportsDroppedBytes`
now asserts both that the head survives a flood and that the hole is measured.

### Environment snapshots are not ported, deliberately

The sketch also names credential-scrubbed snapshots. ZenForge never renders a
job's environment: `Spec.Env` is `json:"-"` and no view, event, or tool output
carries it, so there is no snapshot surface that could leak a credential and a
scrubber would be a helper with no caller. That is recorded here as a
deliberate non-port rather than shipped as dead code; if a host ever wants to
record a job's environment, the scrubber belongs with that surface.

## Consequences

- An interactive session is now expressible: start with `pty: true`, drive it
  with `write_stdin`, read it with `job_output`, and stop it with `job_kill`.
- `creack/pty` becomes a direct dependency. It is small, zero-dependency, and
  the same library the reference's ecosystem uses for this.
- PTY abbreviations are the terminal's: `\n` becomes `\r\n`, output is echoed
  when it is written back, and a full-screen program's escape sequences land in
  the job's output. That is the point of the mode, but a caller parsing output
  should not choose `pty` for a machine-readable command.
- Keeping the head costs the tail the head's bytes (a quarter, at most 8KiB),
  which bounds memory exactly as before: at most `capacity` bytes plus the
  head is readable in one call, and `Len` stays within `capacity + headCap`.
- Off Unix, `pty: true` is refused with a clear error rather than
  approximated: the platform has no pseudo-terminal the manager can own. That
  is why `creack/pty` is imported only from the Unix file, and why the jobs
  packages still cross-compile for Windows.

## Verification

`go test ./jobs/` (terminal detection and merged streams against a piped
control run, an interactive session driven by writes and its exit code, the
injected `TERM`, a killed session whose HUP-ignoring child must be gone — the
test fails when the group kill is removed, and the buffer head/tail contract),
`go test ./tools/jobs/` (the `pty` path through the tool schema, the
size-without-pty refusal, elided-byte reporting and the footer),
`go test ./... -count=1`, `go test -race ./jobs/ ./tools/jobs/`, `go vet ./...`,
`gofmt -l`, and Linux amd64/arm64 cross-builds.
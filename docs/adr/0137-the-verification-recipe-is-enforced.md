# 0137. The verification recipe is enforced, and its environment-dependent part is named

- Status: Accepted
- Date: 2026-09-22
- Related: 0099 (the console boundary test this one sits beside), 0060 (a chain's
  verification recipe)

## Context

Every ADR in this repository states its verification as the same three commands:
`go test`, `go vet ./...` and `gofmt -l`. Two of the three were enforced:

- `go test ./...` and `go vet ./...` are CI steps;
- the console boundary rule was given its own test, in `docs/`, precisely so that a
  required CI step runs it (ADR 0099).

`gofmt -l` was never enforced. It appeared as the last line of roughly thirty ADRs
and in the handover's discipline list, but no step ran it -- and `main` carried a
file `gofmt` would rewrite (`internal/dshstream/queue_test.go`, missing its final
newline) through several commits, including green CI runs. A stated verification
that nobody runs is worse than an unstated one: it reads as a guarantee.

A second, smaller fact about the same recipe surfaced while running it: the
`tools/jobs` PTY tests cannot pass in a sandboxed shell on this machine. They fail
with `start pty: operation not permitted` from `creack/pty`, which surfaces as the
`write_stdin ... already finished` assertion. The tests are correct -- PTY
allocation needs a permission the sandbox withholds -- and CI (unprivileged but
unsandboxed) runs them green, so a local red there is the environment, not the
change. Nothing recorded that, so it is read as a regression each time it is met.

## Decision

**The formatting half of the recipe becomes a test in `docs/`, beside the boundary
test, and the recipe's one environment-dependent part is written down where the
recipe is.**

- The check is `TestGoSourcesAreFormatted`, a Go test that walks the module and
  compares every `.go` file against `go/format` -- the same implementation the
  `gofmt` command wraps, so it needs no external binary and no shell, and cannot
  drift from the tool the ADRs name. It fails with the exact `gofmt -w` command to
  run, and refuses to pass vacuously (fewer than 100 files scanned is a failure,
  because the check is only meaningful over the module).
- It lives in `docs/` rather than in the workflow because `go test ./docs/...` is
  already a required CI step: the gate cannot be dropped by an unrelated workflow
  edit, and it runs identically in a developer's shell, where a red result is
  actionable -- which is the same reason the boundary rule lives there (ADR 0099).
- A file that does not parse is skipped rather than reported: the compiler owns
  that failure and says it better. The gate reports only what it can compare.
- `webui/` is skipped: it is the staged console artifact, JavaScript and vendored
  files, no Go. `.git` and `node_modules` are skipped for the same reason.

The environment fact is recorded in the handover's discipline section, in the form
a future window needs: which tests, what the error looks like, and that CI is the
authority for them.

## Consequences

- `main` is `gofmt`-clean, and a chain that leaves an unformatted file fails
  `go test ./docs/...` instead of passing CI -- the recipe's three commands are now
  all enforced somewhere.
- The cost is one walking test (0.4s) and one skipped directory list; there is no
  new binary, workflow step, or dependency.
- The PTY note is documentation only: it changes no test and no host behavior. Its
  value is that the next window reads "this sandbox cannot allocate a PTY" instead
  of "the PTY tests fail", which is the difference between a known environment and
  an unexplained red.
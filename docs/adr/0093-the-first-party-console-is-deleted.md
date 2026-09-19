# ADR 0093: The First-Party Console Is Deleted

Status: accepted

Supersedes the retention half of ADR 0078.

## Context

ADR 0078 introduced a first-party console: `webui.Handler()` served a small
hand-written page on localhost as the harness's browser face. ADR 0079 and ADR
0080 replaced it with the rebranded upstream console at `/`, and the operator's
decision was that the old one should be deleted rather than kept beside the new
one at `/classic`.

The route went first; the package stayed, because `internal/dshmount` still
referenced `webui/` as a fallback source. That reference is gone -- the mount
imports `webui/dsh` -- so the package has been unreachable and unimported since,
compiled only by its own test.

## Decision

Delete `webui/app.js`, `webui/index.html`, `webui/style.css`, `webui/webui.go`
and `webui/webui_test.go`, and keep `/classic/` unserved so no URL promises a
console that does not exist.

## Consequences

- One console to build, mount, test and document.
- A host without the staged artifacts now has **no** console rather than a
  fallback one. That is the trade the mount already made: it fails loudly at boot
  with a described cause instead of serving a shell that cannot talk to the host
  (ADR 0080), and the artifacts are committed, so the fallback was not carrying
  its weight.
- The old page remains in git history for anyone who wants to read it.
- `docs/limitations.md` no longer describes the console as staged-but-unserved,
  as a fallback, or as unable to hold a multi-turn conversation; those statements
  outlived their subject and are replaced with what the host does now.

## Alternatives Rejected

### Keep the package as a fallback

Dead code with a test that keeps it compiling, and a second console whose
behaviour would have to be kept honest in the documentation beside the real one.
The mount already treats "no artifacts" as a fatal, described condition.

### Serve it at `/classic`

The operator's explicit instruction was to delete it. A second console also means
a second answer to "what does this host's browser face support", which is exactly
the ambiguity the consolidated limitations section removes.

## Verification

`grep -rn 'zenforge/webui"' --include=*.go` returns no importer; `go build ./...`
and `go test ./webui/dsh/ ./internal/dshmount/ ./cli/ ./docs/...` pass; the served
console answers `/` and `/plugins`, and `/classic/` is a 404.

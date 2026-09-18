# ADR 0082: The Host Names The Model And Records What It Cannot Answer

Status: accepted

## Context

The rebranded console is mounted on this host (ADR 0080, ADR 0081). Two gaps
showed up once it actually booted in a browser.

The console's model selector asks `POST /api/session/modelCatalog` when a session
activates. The host does not serve it, so the picker shows its failure state —
survivable, since chat still works, but it is a panel that reports nothing about
a host that very much has a model configured.

And the console asks for a great deal more than the host serves: the sidebar,
the workspace, tools, subagents, jobs, plugins. Every one of those is a 404
today, and the only way to learn which endpoint a panel wanted was to read a
browser's network log. The order in which the console asks is exactly the order
worth implementing, and it was invisible from the host side.

## Decision

### The catalog is an injected source, not an invented value

`session/modelCatalog` answers from a source the host supplies through a setter,
so `serve` can hand it its own configured model without this package importing
`cli` (which would be a cycle) and without changing `New`'s signature, which the
mount already calls. With no source set, the method answers a well-formed
`unimplemented` method error: a panel that shows an error is a better failure
than a panel that shows a model the host did not configure.

### The host logs the endpoints it does not serve

An optional logger on the handler writes one line per unknown namespace or
method, recording the namespace and method **only**. Not the arguments, not the
`rpcId`, nothing from the body — this log is read while an operator has an API
key configured, and a diagnostic that leaks a credential into a terminal is
worse than the blindness it cures. The zero value stays silent: no existing test
starts printing, and a host that does not want the log pays nothing.

## Consequences

- The model picker can show the configured model, and the host-side credential
  stays host-side (the console never sends credentials, by upstream design).
- The remaining namespaces can be implemented in the order the console actually
  asks for them, read off the host's own log instead of a browser devtools
  session.
- A 404 remains a 404: the console still degrades a feature it cannot have, and
  the log changes reporting only.

## Alternatives Rejected

### Hard-code a model name in the catalog

Then the picker would offer a model the host may not serve, and a run started
through it would fail somewhere less legible than the picker.

### Answer an empty catalog

An empty list reads as "no models configured" while the host has one; the picker
would be wrong rather than absent.

### Log the whole request body for diagnosis

The body of a settings call is where an API key lives. The namespace and method
are what the next implementer needs, and they are not secret.

### Serve every unknown namespace with an empty success

A panel would render as if it had data. The 404 is what keeps an unimplemented
feature visibly unimplemented.

## Verification

`go test ./internal/dshapi/` — the model-catalog tests (source set and unset, and
the envelope still echoing `rpcId` exactly), the observability tests (one line
per unknown namespace and method, a line that contains neither an argument value
nor the `rpcId`, and silence with a nil logger), plus the existing envelope,
fence and session tests unchanged. Also `go test -race ./internal/dshapi/`,
`go test ./... -count=1`, `go vet ./...`, `gofmt -l`, `go test ./docs/...`.

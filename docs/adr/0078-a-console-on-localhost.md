# ADR 0078: A Console On Localhost

Status: accepted

## Context

`server/harnesshttp` has served a complete detached-run API for several
releases — start, resume, status, list, attach (streaming), cancel, approvals,
and (ADR 0070) a signed webhook — but nothing in the product wired it up:
`examples/http-harness-agent/main.go` was the only place that registered the
routes. An operator who wants to watch a run, or to try the harness against a
model endpoint they have a key for, had to build that example or use the CLI and
read a terminal.

The reference harness ships a browser console, and it is a good testing surface
for exactly this reason: a run is a stream of events with tool calls inside it,
and a terminal scrollback is a poor way to watch several of them at once.

## Decision

### The console is served by the same process, on localhost, by default

`zenforge serve` assembles the runtime the example used to assemble by hand and
serves the console from the same origin as the API. Same origin means no CORS
configuration, one process to start, and no second thing to deploy; the console
is not a separate application that can drift from the API it talks to.

The default bind address is `127.0.0.1:8787`, and binding anything else requires
`--allow-remote`. This is not a formality: the console can start runs in the
server's workspace with the server's tools, so a console reachable from the
network is a remote shell with a nice stylesheet. The flag makes that a
deliberate act.

### The interface is embedded in the binary

The HTML, CSS, and JavaScript live in a `webui` package and are embedded with
`go:embed`. There is no build step, no CDN, and no external font: the binary
contains the console it serves, so a Go build is still the whole toolchain, the
console cannot be out of sync with the release it shipped in, and an air-gapped
machine gets the same interface as any other.

The reference console is a plugin-based React application distributed as a
minified bundle that talks to its own gateway protocol. Copying it would mean
shipping a build artifact we cannot read, implementing that protocol inside
`server/harnesshttp`, and adding Node to a Go repository's CI — three costs for
an interface whose useful part is the interaction model. The interaction model
is what this ADR copies: a run list with live status, a streaming transcript
with tool cards, an input box, and cancellations.

### Settings are write-only where they matter

`GET /api/settings` reports the endpoint, model, provider, and whether a key is
set — never the key. `POST /api/settings` accepts them and applies them to runs
started afterwards, in memory only. An empty key means "keep the one you have",
so the console can change a model without re-typing a credential it was never
shown.

CLI and environment values are the defaults and a page-set value overrides one
until the server restarts. The key is never logged, never returned, and never
written to disk: the operator pasted it into a page, and a page is not a place
from which a credential should be readable back. For the same reason a settings
change is accepted only from a loopback client unless `--allow-remote` was
passed — otherwise any page that can reach the console could set the endpoint a
run is sent to.

### Monitoring reuses the run stream

The console renders the existing attach stream and lists runs from the existing
endpoint; it does not introduce a second event API. The durable checkpoint store
remains the record, so a page refresh re-reads history rather than losing it,
and a run that outlives the browser tab is still the same run (ADR 0067's
detached model, applied to a person instead of a program).

Several runs can be watched at once because the harness already supports
several: the run manager owns the registry, and the console is one more client
of it.

## Consequences

- The harness API stops being an example-only surface: an operator can start,
  watch, cancel, and configure runs from a browser, and try a new model endpoint
  without editing a config file.
- The console is a local tool with no authentication. That is a deliberate
  consequence of the loopback default, and it is why `--allow-remote` exists and
  why the settings POST is loopback-only by default.
- Approvals have endpoints already; surfacing them in the console is the next
  slice, and until then an approval-gated tool call either follows the server's
  `--approve` mode or is refused, exactly as it is from the CLI.
- The console is not in the reference plan's Part C rows: it is an addition,
  recorded in the parity plan as C21 rather than presented as parity with a
  capability the plan listed.

## Alternatives Rejected

### Embed the reference console's bundle

It is minified, coupled to that product's gateway and plugin host, and would
require implementing that protocol here. The useful part — the layout and the
interaction — is not what the bundle contains.

### A separate Node/Vite single-page application

Nicer to extend, but it adds a second toolchain, a build step, and a second
artifact to a repository whose entire deliverable is a Go binary, and the first
useful version of this console does not need a framework.

### Serve the console from a different origin or port

Then every request needs CORS, the operator has two URLs, and the console's
version can disagree with the API's. One origin and one binary keep the failure
modes small.

### Persist the page-set key to the config file

Convenient once and wrong from then on: a credential the operator typed into a
form would land in a project file that gets committed. Preferring an environment
variable or the CLI for persistence is a decision the operator can make
deliberately.

### Server-rendered pages with no JavaScript

Then nothing streams: a run's value is watching events arrive, and a
refresh-to-see-progress page would turn the console into a worse terminal.

## Verification

`go test ./webui/` — the embedded handler serves `index.html` at `/` as
`text/html` and each asset with its own content type, and the HTML references
the files the package actually ships. `go test ./cli/` — `serve` refuses a
non-loopback address without `--allow-remote`; `GET /api/settings` never
contains the key; `POST /api/settings` applies the endpoint and model, reports
the key as set, keeps the existing key when the field is empty, refuses a bad
URL or provider, and refuses a settings change from a non-loopback client. Plus
`go test ... -count=1`, `go vet ./...`, `gofmt -l`, and `go test ./docs/...`.
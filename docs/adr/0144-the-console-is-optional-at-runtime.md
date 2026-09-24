# 0144. The console is optional at runtime: one flag, one seam, and a served run that proves it

- Status: Accepted
- Date: 2026-09-24
- Related: 0099 (the framework core and the console adapter are separate
  layers), 0109 (the run registry is durable), 0102 (the settings document
  survives a restart), 0140 (a run keeps the model it started on), 0141 (the
  admission boundary is one layer), 0137 (the verification recipe is enforced)

## Context

ADR 0099 put the console in its own layer and `docs/console_boundary_test.go`
has enforced the direction ever since: no file outside `internal/dsh*`, `cli/`,
`cmd/`, `webui/` may import the console adapter or use its vocabulary. What that
test cannot see is *assembly*. It classified all of `cli/` as adapter tier, so it
could not notice that `zenforge serve` built the console unconditionally --
`dshmount.New` and `dshstream.New` on every start, `mux.Handle("/", cfg.dsh)`
with a handler that would panic if it were nil -- and it could not notice that a
run's own model came from console-tier code. "The console is a layer" was true at
compile time and false at run time.

For an **embedder** the layering already held: `examples/http-harness-agent`
serves detached runs, SSE, approvals, SQLite stores and a signed webhook from
`server/harnesshttp` alone, and `integration/consumer` requires only the root
module with no websocket dependency at all. The gap was the shipped host, whose
only documented way to run was with the console.

An independent audit of the boundary, run while this chain was in flight, made
the second half concrete. With the console treated as a mount to be skipped, a
headless host would still have taken its model from the console's settings
document: `settingsStore.rebuild()` writes the delegating adapter every run uses,
and `agentConfig.ModelResolver = consoleRouteResolver{...}` is what `/runs/resume`
rebuilds a frozen route through. The flag would then have hidden a coupling
rather than removed one, which is the failure this repository's ADRs are written
to avoid.

## Decision

**1. `--console on|off`, default `on`.** Off is the headless mode. On is what the
command has always done, so no existing deployment loses a surface to an upgrade,
and the default is stated in the option's own help. An unknown value is a usage
error naming the two accepted ones, in the shape `--sandbox` already uses.

**2. The console's assembly is one seam, not a file-wide habit.**
`newServeApp` builds the core path; `newConsoleAdapter` builds the console and
returns `(nil, nil)` when the console is off. The adapter owns every store,
catalog, face and handler that exists only to answer the console: the workspace
registry and its baselines, the goal store, the attachment store, the file face
and file references, the command and skill catalogs, the presets, the credential
and settings faces, the pending queue, the mount and the WebSocket mux. `serve`
is the only caller, and `(*consoleAdapter).handlers()` answers `(nil, nil)` for a
nil receiver, so the route table asks the adapter rather than re-deriving the
mode.

**3. With the console off, the host's model is `zenforge run`'s model.** No
settings store, no settings document, no provider-profile or model-selection
store, no `modelOverride`, and no `consoleRouteResolver`: the model is built by
`buildAgentConfig` from `--provider`, `--model`, `--api-key`, `--base-url` and
the environment those flags read, and the resolver stays the CLI's own. This is
the substance of the decision. It is also why `/api/settings` is not registered
on a headless host -- that route *is* the console's settings document API, and
its POST rewrites the adapter host runs use, so serving it without a console
would be serving a configuration panel that has no page. `/api/server` stays: it
reports the workspace and the remote posture, which are the host's own.

**4. A headless host with no model refuses to start.** A console host may start
with no credential, because the settings panel is where an operator pastes one.
A headless host has no page to fix anything on, so it would serve an address
whose every run fails with a provider error the caller cannot repair. It fails
closed, naming the flags that configure a model, in the same style as the
identity rules that already refuse a host which cannot authenticate. For the same
reason `--settings-file` is refused with `--console=off` instead of accepted and
ignored: the flag names the console's document, which that host never reads or
writes.

**5. Every path the host does not serve answers `console_disabled`.** The
fallback is registered at `/` and answers `404` with the host's JSON error
envelope: `{"error":{"code":"console_disabled","message":"the DSH console is not
served by this host: ..."}}`. The code names the missing capability, so a client
can tell "this host runs headless" from "this route never existed" -- which is
the same distinction the coverage ledger draws between a refusal and a 404. It is
JSON and not HTML: a headless host has no page to send anyone to, and the
assertion is part of the test. The sign-in routes are the one HTML exception and
remain when authentication is configured, because a caller with no token has to
be able to reach the form that gives it one.

**6. The claim is checked by a served run, not by a sentence.** The tests in
`cli/console_disabled_test.go` build the real `newServeApp` with the console off
and drive the real HTTP surface: a detached run started through `POST /runs/start`
answers with the stub the flags named, its events are read over `GET /runs/attach`,
it is listed by `GET /runs`, and a settings document planted at `--settings-file`
is byte-identical before and after -- the document is neither read nor rewritten
on that path. The boundary is asserted from both sides (a console route answers
with the console on and 404s with the code above when it is off), the durable run
registry is shown to adopt stored runs with no console, and the console-on model
path is shown to still come from the settings document.

**7. The ledger describes the adapter, not the host.** `docs/dsh-console-coverage.md`
is unchanged in what it counts and now says so in its own words: it is the state
of the console *surface*, and a host started with `--console=off` serves none of
it. `docs/architecture.md` gains the runtime half of its own claim: the console
is not only optional to import, it is optional to serve.

## Consequences

`zenforge serve --console=off` is a supported headless host: the harness API, the
server-info route, the sign-in routes when authentication is configured, and a
durable run registry, with no console path, no WebSocket mux, no settings
document and no console-shaped vocabulary in the answer to a path it does not
serve. `zenforge serve` with the console on is byte-identical to before: the same
statements in the same order, `/api/settings` still served, the model still taken
from the document.

The consequence worth stating plainly is what a headless host *loses*, because it
is the point rather than a defect: there is no page, so there is no way to change
the model at runtime, no per-session model selection, no session list, no goal
dock, no file browser, no attachment upload, no skills panel. A deployment that
wants those serves the console; a deployment that wants an API coordinates it
with flags and a supervisor. The three scenario examples and
`examples/http-harness-agent` are untouched by this and remain the embedding
reference.

A resumed run on a headless host keeps the model its checkpoint froze and takes
its endpoint and credential from the flags: `Resolve("openai", "the-checkpoints-
model")` rebuilds through the host's own route, so a `--model` flag that names a
different model does not move a resumed run onto it. A route the console had
declared -- a provider the host does not know -- is refused by name
(`unknown model provider: acme-gateway`) rather than silently redirected to the
configured endpoint. That is the correct direction: a run resumes on the model it
started on (ADR 0140), or it fails saying which route it could not rebuild.

## Verification

The chain is verified by the commands in `AGENTS.md`'s recipe plus the headless
run itself:

```bash
go build ./... && go vet ./cli/ && gofmt -l cli/
go test ./cli/ -count=1 -race                      # the whole package, 210s
go test ./cli/ -run 'TestConsoleOffHost|TestTheConsole|TestServeRefuses|TestServeComesUpHeadless' -count=1 -race -v
go test ./internal/dshapi/ -run TestConsoleCoverage -count=1
go test ./docs/ -count=1
mkdocs build --strict
```

The ten tests that carry the claim are
`TestConsoleOffHostServesAFullDetachedRun`,
`TestTheConsoleIsMountedOrNamedDisabled`,
`TestConsoleOffHostRefusesToStartWithoutAModel`,
`TestTheConsoleHostTakesItsModelFromTheSettingsDocument`,
`TestConsoleOffHostResumesThroughTheCoreResolver`,
`TestConsoleOffHostKeepsItsSignInRoutesAndItsBoundary`,
`TestServeRefusesAnUnknownConsoleMode`,
`TestServeRefusesASettingsFileItWouldIgnore`,
`TestServeComesUpHeadlessFromTheFlag` and
`TestConsoleOffHostAdoptsItsStoredRuns`. The ledger's three tests
(`TestConsoleCoverageLedgerMatchesTheRoutingTable`,
`TestConsoleCoverageLedgerListsExactlyWhatTheClientDeclares`,
`TestConsoleCoverageNextUpNamesOnlyUnshippedGaps`) still pass after the
relocation described below.

The headless run itself, from the test that drives the real command:

```text
zenforge serve listening on http://127.0.0.1:<port>
console: disabled (--console=off); this host serves the harness API only
```

It was also run by hand, against a scripted OpenAI-compatible endpoint, to check
the claims above outside the test harness (Apple M5, this chain's binary):

```text
$ zenforge serve --console=off --addr 127.0.0.1:8972 --workspace ./ws \
    --base-url http://127.0.0.1:8971/v1 --model headless-stub --api-key sk-... --provider openai
zenforge serve listening on http://127.0.0.1:8972
console: disabled (--console=off); this host serves the harness API only

$ curl -s http://127.0.0.1:8972/                     # 404 application/json
{"error":{"code":"console_disabled","message":"the DSH console is not served by this host: ..."}}
$ curl -s http://127.0.0.1:8972/api/settings          # the same 404
$ curl -s http://127.0.0.1:8972/api/server
{"allowRemote":false,"workspace":"/tmp/zf-headless/ws"}

$ curl -sX POST -d '{"runId":"headless-tools-4","input":"Write out.txt."}' /runs/start
{"runId":"headless-tools-4","status":"starting",...}          # 202
$ curl -s '/runs/status?runId=headless-tools-4'
{"runId":"headless-tools-4","status":"completed",...}
$ curl -s '/runs'                          # the same run, from the durable registry
$ curl -sN '/runs/attach?runId=headless-tools-4'
tool.call   workspace_read
tool.error  workspace_read   workspace path not found: file does not exist
tool.call   workspace_write
tool.result workspace_write  {"path":"out.txt","bytes":36,...}
run.done    "The headless host read, wrote and answered with no console mounted."

$ cat ws/out.txt
written by a run on a headless host
```

The run's own tool policy is untouched by the mode: the read-before-write rule
(observe the path's absence before creating it) fired on this host exactly as it
does on a console host, which is what the `tool.error` for the read is.

## Deviations and precision fixes

1. **`--settings-file` is refused, not ignored, with `--console=off`.** The first
   implementation accepted it and did nothing, which would have dropped what an
   operator asked for without saying so. Refusing keeps the command's existing
   posture: a host that cannot do what it was told to refuses to start.
2. **The help text and the code comment were narrowed.** "No HTML surface" was
   wrong while the sign-in page exists; the flag now says "no console surface"
   and the comment names the sign-in page as the one exception.
3. **The console's settings document is not a host artifact.** The audit named
   `/api/settings` as the clearest case of a host route with console
   implementation and console side effects. The decision above resolves it by
   scoping: with a console the document is the configuration path, without one
   the flags are.
4. **`workspace` stays in the core path** only because `/api/server` reports it;
   the console reads the same value. It is not a console artifact.
5. **The durable run registry stays in the core path**, though its comment was
   written when the console's sidebar was its only reader. The harness's own
   `/runs` needs it with or without a console, and the comment now says that.
6. **`serveApp.settings` is nil on a headless host.** It is documented on the
   field; no production caller and no test reads it without the console.
7. **The fallback route answers unmatched paths, not only console paths.** With
   no console mounted, `/` is the catch-all, so a typo in a harness path is
   answered by the same envelope. The code names the reason the host has no
   surface rather than pretending the path exists; a client that wants to know
   whether a route exists reads the documentation, and the harness routes are
   exact patterns that win over the fallback.
8. **The route table's nil handling is explicit.** `mux.Handle("/", cfg.dsh)`
   would panic on a nil handler, which is how "the console is required" was
   encoded. The table now chooses between the console mount and the disabled
   handler, and registers the two stream paths only when a stream exists.
9. **The ledger's `workspaceFiles/changes` row moved sections.** It is state
   `stream` and was filed under "Refused by name" -- a placement that reads as a
   refusal while the mux serves the subscription. It now sits with the other
   three stream rows, and its own text says the unary dispatcher has no arm for
   it. The row set, the states and the summary counts are unchanged, which is why
   the ledger tests still pass; `terminal/follow` stays a `refused` row because
   that is what the mux arm answers, and the ledger's "How to read it" table
   already defines `refused` as routed-and-declined rather than unary.
10. **A real `/runs/resume` over HTTP with the console off is not covered.** The
    resume path is pinned at the resolver, where the route decision is made, and
    the route was read to confirm the HTTP handler reaches it the same way. A
    hermetic interrupted-run fixture was out of proportion for this chain, and
    the gap is named rather than implied.
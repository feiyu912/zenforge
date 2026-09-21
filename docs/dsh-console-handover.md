# DSH Console Attachment: State and Remaining Work

Internal planning artifact; excluded from the published site (see `exclude_docs`
in `mkdocs.yml`). It records where the "attach the upstream console" work stands
so a fresh session can continue without re-deriving anything.

**Current capability state lives in
[the console coverage ledger](dsh-console-coverage.md)**, not in the prose
below: every remote method the console client declares, marked served, refused
by name, stream, or unserved, kept honest by
`internal/dshapi/console_coverage_test.go`. This tier's boundary — it may
consume the framework, never the reverse — is
[ADR 0099](adr/0099-the-framework-core-and-the-console-adapter-are-separate-layers.md),
pinned by `docs/console_boundary_test.go`.

## Decision and evidence

- **ADR 0079**: the console is a **rebranded build** of the upstream MIT sources,
  because the published build cannot be renamed at runtime (title, boot-page
  wordmark, layout title, sidebar name/mark fallbacks and hero are compiled in;
  the published bundles have the official-brand gate compiled out).
- **`docs/dsh-console-protocol-recon.md`** (946 lines, `path:line` citations):
  the host protocol this work implements. Upstream pinned at `ddefc45`
  ("0.1.6-alpha.2"), cross-checked against published `0.1.5-rc.2`.
- **ADR 0080**: the module graph is validated where it is built, `/plugins` is an
  exact-match table, revisions are derived, maps are not served.

## Shipped (each pushed, CI + docs green)

| Commit | What |
| --- | --- |
| `9e9e54b` | `zenforge serve` host (harness routes, settings API with write-only key, loopback default, `--allow-remote`) + first-party embedded console (ADR 0078) |
| `a47f7c8` | ADR 0079 + the archived reconnaissance report |
| `1d82100` | rebranded console artifacts in `webui/dsh/` (8.8 MB, no maps, heavy panels dropped) + `scripts/build-console.sh` + `console.yml` rebuild workflow + branding tests |
| `24c7834` | boot half: `internal/dshboot/` (module graph with upstream-shaped strict validation, verbatim module-loader queue script, ordered/idempotent injections, exact-match `/plugins`) + ADR 0080 |

## Remaining, in order

1. **`/api/<namespace>/<method>` unary RPC** — package `internal/dshapi/`
   (an agent task was started for this and had not produced files when this note
   was written). Envelope rules: exact `rpcId` echo, unknown method → HTTP 404,
   `payload.args` the single plain object, malformed → 400 (not an error
   envelope). Trust fence: refuse `Sec-Fetch-Site: cross-site`, refuse an
   `Origin` that does not match `Host`, refuse non-loopback unless configured.
   Namespace `session`: `list`, `create`, `prompt`, `cancel`, `rename`, `page`,
   mapped onto this repository's runs/event store; anything that cannot be
   implemented truthfully returns `ok:false` with code `unimplemented`.
2. **WebSocket mux `/api/remote.mux`** — three streams: `$events` (first frame
   must be `{type:"ready",clientId,host}`; approval waterfalls answered via
   `POST /api/$events/result` with `allowed-once`/`rejected`), `session/control`
   (`{type:"baseline",value:{jobs:{},projections:{}}}`), and `session/follow`
   (snapshot → `event` → `assistant-stream`). Exact-key frames only; 2 s
   heartbeat; no resume token.
3. **Approval bridge** — `approval.PendingBroker` → `approval/request` waterfall,
   answer `allowed-once`/`rejected`, fail closed.
4. **Roster tiers** — start from the minimum chat set (~23 plugins, the closure
   is computed in the recon), adding panels only when the host namespace each
   needs answers; a plugin in the graph without a service provider is a fatal
   boot failure.
5. **Wire into `zenforge serve`** — mount `internal/dshboot` + `internal/dshapi`
   + the WS mux, serve `webui/dsh` as the console, then verify in a browser end
   to end. **Shipped**: the ADR 0078 console was not kept as a fallback -- it is
   deleted, and `/classic/` is a 404 (ADR 0093).

## Rebuilding the console artifacts

`scripts/build-console.sh` is the recipe (pinned revision, asserted brand patch,
`DSH_CLIENT_TITLE=ZenForge`, `DSH_CLIENT_BUILD_PROFILE=local`, staging +
verification). It needs corepack's pnpm on PATH: the upstream build calls bare
`pnpm`, so with only `corepack pnpm` installed, the build dies at `build:lib`
with `sh: pnpm: command not found`. A shim (`exec corepack pnpm "$@"`) on PATH
plus a cwd inside the upstream tree resolves the pinned `pnpm@11.7.0`. The
artifact tree excludes `.map` and `dist/preview/`.

## Discipline that this work needed

- Long subagent tasks stalled when their briefs asked for the full
  `go test ./...` suite; scoped, targeted commands finished. Ask for
  `gofmt`/`go vet <pkg>`/`go test <pkg> -count=1` only.
- Give parallel agents disjoint paths; two agents editing `webui/` at once would
  have collided. `internal/dshboot` vs `webui/dsh` vs `internal/dshapi` worked.
- Never `git add -A` while an agent is writing: stage exact paths.
- Tests must use `t.TempDir()`; an earlier batch committed stray run files from a
  test that used the default checkpoint directory.

## State at the end of the first planning window (round 40)

The console is **mounted and usable**: `zenforge serve` serves the rebranded DSH
console at `/`, it boots without a plugin failure, the session sidebar loads, live
event follow works, approvals can be answered, and the model selector reports the
host's configured model. The interim first-party console is gone (`/classic/` is
a 404) by the operator's decision.

Shipped, each pushed with CI and docs green:

| Commit | What |
| --- | --- |
| `9e9e54b` | `zenforge serve` + the interim console (ADR 0078) |
| `a47f7c8` | ADR 0079 + the archived reconnaissance report |
| `1d82100` | rebranded console artifacts + rebuild recipe + `console.yml` |
| `24c7834`, `331eb1e`, `a6f8654` | boot half (ADR 0080), unary RPC + catalog + unserved-endpoint log (ADR 0081, 0082), catalog wired to the settings store |
| `e267be6` | the `[hidden]` stylesheet fix |
| `4daac90` | this handover note |
| `8e76317` | the mount, the roster, the streams (ADR 0083) |

Verified live at the end of that window: `/` is the injected shell under the
ZenForge title; `WS /api/remote.mux` answers 101; `POST /api/session/list`
answers a well-formed envelope; `/api/session/modelCatalog` answers
`{"default":{"provider":"openai","model":"qwen-plus"},...}` on a host started
with `--model qwen-plus`; `/classic/` is 404.

### To run it

```bash
cd /Users/kaicheng/coding/zenmind-develop/zenforge
go run ./cmd/zenforge serve \
  --base-url https://dashscope.aliyuncs.com/compatible-mode/v1 \
  --model qwen-plus --api-key "$DASHSCOPE_API_KEY"
```

The credential is host-side by upstream design: the console sends no credentials
and has no key field. `--allow-remote` is required to reach it from another
machine, and it also relaxes the settings API to non-loopback callers.

### What remains, with the takeover point for each

1. **The console's model/settings panel is served** as of ADR 0087: `settings`
   (`describe`, `update`, `replace`, `mutate`, the native-open probes) answers
   with a real schemastery envelope and applies endpoint/model/key writes to the
   running process. What is left here is persistence: there is still no settings
   document on disk (`hasDocument: false`), and only one profile per route is
   modelled. The historical note below still explains the mechanism.
2. **The console browses the workspace** as of ADR 0089
   (`workspaceFiles/list|stat|read|readAll|readBytes`, with watching and relation
   reads refused by name and paths confined to the served workspace root).
3. **The console's own settings namespaces are held** as of ADR 0094
   (`ui-onboarding`'s welcome-notice acknowledgement, so the notice's Continue
   works on a loopback page and the notice stops reappearing within a run).
4. **The console's identity reads `zenforge`** as of ADR 0092, in the page
   title, the installable-app name, the favicon's accessible name and the brand
   plugins' copy, with the capital form asserted absent from browser-visible
   bytes.
5. **The console's slash commands run** as of ADR 0090
   (`commands/list|execute`: the menu lists the host's catalog, a submitted line
   expands with the host's own rules and starts its run, an unknown line is
   answered without a value). The one honest difference from upstream is that no
   `command/run`/`command/done` flow node is logged.
6. **The console's plugin inventory is answered** as of ADR 0091
   (`pluginInventory/list` from the mount's roster, `managementAvailable: false`,
   every `pluginManager` write refused with its reason). With it, every namespace
   a shipped client panel calls is served or refused by name.
7. **The console's preset selectors are answered** as of ADR 0088
   (`permissionPresets/catalog`, `agentPresets/list|read`, with authoring and
   selection refused by name). These were found by reading the host's own
   "endpoint is not served" log lines rather than by guessing, which is the
   instrument to keep using for the remaining namespaces.
8. **The console's own model/settings panel** (historical note). Its edits go to a host-owned settings document
   (`settings/document-updated`, `settings/conflict`), not to a credential form.
   The instrument is already in place: `serve` passes `slog.Default()` into the
   RPC handler (`dshmount.Config.Logger` -> `dshapi.Config.Logger`), so every
   endpoint the console asks for that this host does not serve prints one line
   naming only `namespace` and `method`. Open the panel, read the log, implement
   those methods in `internal/dshapi` in the order they appear. The log never
   contains arguments, `rpcId`, or a key — keep it that way.
2. **Multi-turn.** A session maps to one run, so a prompt to a finished run
   answers `unimplemented`. The fix is a session→current-run table (a session id
   that outlives its runs) or an agent API that appends a turn to a finished run.
   Note the related deviation in ADR 0083: `session/follow` ends when the run
   ends, which that layer should revisit.
3. **The remaining panels** (workspace files, tools, subagents, jobs, plugin
   manager): same log-driven approach; unimplemented namespaces must stay 404 so
   the console degrades one feature rather than failing to boot.
4. **Artifact rebuild** (`scripts/build-console.sh`) needs the corepack pnpm shim
   described above; the roster then has to be regenerated
   (`scripts/gen-dsh-roster.py`) because the module edges and the withheld entry
   live in `internal/dshmount/roster.json`.

### Environment notes that cost time here

- `pkill -f 'zenforge serve'` kills only the `go run` parent; the compiled child
  under `/tmp/zenforge-gotmp/go-build*/exe/zenforge` keeps the port. Kill by
  `-f 'exe/zenforge'` too, and never `pkill -f 'go-build'` (it is the same path).
- Long `go test` runs repeatedly produced no output and timed out on this
  machine while short runs passed; CI was the reliable arbiter. Prefer targeted
  `-run`/single-package runs locally and let CI run the suite.

## Multi-turn: shipped (ADR 0086, ADR 0108)

The seam described below is now implemented (ADR 0086): a session's turns are
named by the deterministic chain in `internal/dshsession`, `session/prompt`
starts the next turn with `InitialMessages` rebuilt from the durable logs,
`session/list` groups the turns under one session, and `session/follow` and
`session/page` resolve a session to its newest turn. The turns are also merged
into **one session-wide sequence** (ADR 0108), so a second prompt loads its
history and `Load earlier` reaches an earlier turn.

The research that decided it, kept because it explains the choices:

## Multi-turn: the seam exists, the mapping is the work (researched)

A session maps to one run today, so a prompt to a finished run answers
`unimplemented`. The research that decides how to fix it:

- **`RunManager.Resume(ctx, runID)` takes no new input.** It continues a run that
  has durable events and is not terminal; the `Agent` interface's `Resume` is the
  same shape (`Stream(ctx, task)` is how new work starts). So "resume the run with
  another user turn" is **not** the mechanism and should not be forced into it.
- **`zenforge.Task` already carries `InitialMessages []model.Message`.** A new run
  can therefore be started with the conversation so far prepended, which is the
  honest way to make turn two a continuation rather than a fresh unrelated run.
- The conversation is reconstructable from the durable log: `run.started` carries
  the user input (`input`), and the assistant's text arrives as `model.delta`
  chunks and/or in `model.done`. Pin the exact field names against `events.go`
  before relying on them.

So the shape is a host-side **session -> runs** mapping:

1. a session id stays stable; its first turn is the run whose id equals the
   session id (today's behaviour), later turns start new runs recorded under that
   session;
2. `session/prompt` on a session whose current run is active steers it (today's
   behaviour); on a terminal one it starts a new run with `Input` = the new text
   and `InitialMessages` reconstructed from that session's earlier runs;
3. `session/list` must list **sessions**, not runs, with the current run's state
   and the title from the log; `session/page` and `session/follow` must merge a
   session's runs in order, so history reads as one conversation;
4. ADR 0083's deviation (a follow stream ends when its run ends) becomes
   defensible once a session outlives its runs, because the console re-opens the
   stream after the next prompt -- but re-check it against the client's retry
   behaviour when this lands.

Nothing above requires a harness change; it is mapping plus message
reconstruction, and it belongs beside the session methods in `internal/dshapi`.

## Wire shapes that hid behind working controls (2026-09-19)

Two host bugs in this attachment were invisible to the coverage ledger, because
the method *was* routed and the call *did* answer -- with the wrong reading of its
own arguments. Both are fixed; the shape of the mistake is why the ledger alone
cannot call this tier done.

- **The named `request` parameter** (`5bfe507`). The generated remote map names 21
  methods' single parameter literally `request` (`session/create`,
  `session/prompt`, `session/page`, `session/cancel`, `session/rename`,
  `session/selectModel`, the whole `workspace/*` namespace) and `session/list`'s
  `_request`. The client therefore sends `{"request": {...}}`, and every handler
  that read its fields directly saw an empty args map. `session/create` silently
  dropped its `workspaceId`: the console thought it had grouped a session while
  the host created one with no workspace, which left the session list, the model
  picker and the transcript empty while each individual call looked answered. The
  dispatcher now splices the object into its args
  (`internal/dshapi/requestargs.go`) and `session/create` refuses fields it does
  not read, so a typo can no longer pass for a field. Tests drive shapes captured
  from live traffic, not from the type declarations.
- **The settings user layer and revision** (`a65ff78`). `SettingsNamespaceView`
  carries `user` (the raw section the operator wrote) and a `revision` an editor
  fences its next write with; upstream's own provider editor reads both
  (`ui-settings-models/src/client/ProviderEditor.tsx:97,168,282,284`, and
  `operations.ts:100` maps `settings/conflict`). This host reported neither: the
  layer was always absent, so a custom provider's fields came back blank the
  moment they were saved, and the constant revision could not distinguish an
  accepted write from a lost one. Views now report `user`, the revision moves by
  one per committed change, and a stale `expectedRevision` is refused as
  `settings/conflict` with both revisions named (ADR 0087 amendment).

## Shipped: the console's settings document (2026-09-19)

The chain the previous section decided is in, as **ADR 0102**. What the host does
now, and what was decided along the way:

- **One file, in the host's own configuration directory**:
  `<configlayer.UserConfigDir()>/console-settings.json` -- the directory the CLI's
  user layer already reads from, moved by `ZENFORGE_CONFIG_DIR`, and named directly
  by the new `serve --settings-file`. Not `os.UserConfigDir()`: a host with two
  configuration homes has two places for an operator to look and one of them to
  forget. It is never inside the served workspace, which is where a relative
  `--settings-file` lands by default: `workspaceFiles/read` is confined to that root
  (ADR 0089), so a document inside it is a credential a page could browse. With no
  configuration directory at all the host stays process-local and says so once,
  loudly, at startup.
- **Settings and credential share the file**, so one atomic write covers both. The
  split alternative was declined because a profile whose key has not arrived is a
  state this host would have to invent a second commit for.
- **`0600`, staged and renamed** (`0700` for a directory it has to create), flushed
  before the close. A file whose mode grants access to group or others is *refused
  at startup*, not quietly `chmod`ed: this host does not take control of a file it
  did not create.
- **A document that cannot be read stops the host**, naming the file and the kind of
  damage -- truncated, wrong-typed, an unknown field, a version from a newer host.
  The message never quotes the file, because the value the decoder is failing on may
  be the credential, and the standard JSON error quotes values; a type error is
  re-described here from its field name alone.
- **The document names a field only where the console wrote it.** The store holds the
  whole configuration and writes a snapshot, so naming every field would let a write
  that touched only the model record the credential as the empty string -- and the next
  start would read that as a cleared key and wipe one the operator supplies with
  `--api-key`. A field a console request wrote is written; a field nobody spoke about is
  left out and the seed keeps answering it. The document outranks the seed for every
  field it does name, and the host logs those field names -- `baseUrl`, `model`,
  `provider`, `api-key` -- never their values. `apiKeyEnv` is startup configuration and
  is never copied into the file. ADR 0102 first phrased "wrote" as "differs from the
  seed", which dropped a value an operator typed that happened to match a flag; ADR 0103
  replaced the comparison with the write itself.
- **`hasDocument` and the revisions come out of the file.** A store that versions its
  own namespaces is asked instead of the handler counting per process
  (`dshapi.SettingsRevisionStore`, implemented by the serve command's store), so a
  console tab held open across a restart fences against the number the document
  carries. The document is the commit point: a write that cannot reach the file puts
  the store back and reports the refusal, so the running process and its document
  never disagree.
- **What is durable**: the endpoint, the model, the inline credential, the
  console-owned namespaces (so the welcome notice stays dismissed, replacing ADR
  0094's consequence), the declared provider profiles, and the per-namespace
  revisions. Also durable since ADR 0103: the model each session chose. **What is
  not**: the workspace registry (ADR 0101, deliberately outside this chain -- a
  registered directory this host cannot run a session in is a different question
  from a preference worth restoring), the runtime last-used model hint, and any
  chosen model beyond the 64 most recent.

Ledger: no row moved, because no method was added; the `credentials/set` and
`settings/describe` descriptions now say what backs them.

Verification for the chain, to reuse:

```sh
# The document exists, and its mode is the whole point.
ls -l ~/.config/zenforge/console-settings.json
# Restart the host and read the namespaces back: hasDocument is true, the revisions
# are the numbers the file carries, and the endpoint, model, credential and declared
# profiles are still there.
curl -s -X POST http://127.0.0.1:8787/api/settings/describe -H 'content-type: application/json' \
  -d '{"type":"client-request","rpcId":"d1","method":"settings/describe","payload":{"args":{}}}'
# Take a revision, mutate with it as expectedRevision: the reply carries a revision
# one higher, and the superseded number is then refused as settings/conflict.
```

That sequence was run live against a scratch host, with `ZENFORGE_CONFIG_DIR` pointed
at a throwaway directory so the experiment could not rewrite the operator's document:
`--settings-file` off a scratch directory works the same way. A document is shared
state now, and two hosts over one configuration directory replace each other's file.

Tests: `go test ./cli/ -run TestConsoleSettingsDocument` -- the round trip across a
restart, the mode with no staging leftover, the workspace refusal, the default path
never inside the checkout, the loud process-local fallback when there is no
configuration directory, six damaged documents refused by name with the credential
absent from every message, a failed write keeping the old value standing, the startup
line naming fields rather than values, a field the console never moved staying out of
the file so a host configured with `--api-key` keeps its key across a write that only
touched the model, a cleared override staying cleared, and the key appearing in exactly
one file. And `go test ./internal/dshapi/ -run TestSettings`
-- the store's `hasDocument` passed through both ways, and a store that versions its
own namespaces asked instead of the handler counting.

## Shipped: the user layer is the console's own writes (2026-09-19)

The first host to run ADR 0102 reported three things wrong with it, and this chain
(**ADR 0103**) fixed all three:

- **The Models page showed a saved configuration nobody saved.** `settings/describe`
  built every provider namespace's `user` layer out of the live settings, so the flags
  `serve` was launched with -- `--model qwen-plus --base-url ...dashscope...` -- came
  back as an OpenAI card the operator had never filled in. The store now answers through
  an optional face (`dshapi.SettingsUserLayer`) with the fields the console itself wrote,
  and reports no layer at all when nothing was written. The resolved `value` still
  reports the live configuration, flags included: that is what a run uses.
- **The document did not record the endpoint and model that were saved.** The ADR 0102
  rule recorded a field only where the live value differed from the startup seed, so
  typing the value a flag already carried wrote nothing. The rule is now the write
  itself: the mutator that commits a field marks it as the console's
  (`settingsStore.markSpoken`), the document records it, and a loaded document marks its
  own fields so they stay recorded across the next write. The `--api-key` protection is
  unchanged and is now structural: a field no request wrote is never named.
- **A model picked in the composer did not survive a restart.** The per-session choice
  was process-local. It now lives in the same document (`modelSelections`), is adopted
  before the document is read, and follows the commit rule: a document that will not take
  it puts the record back and reports the failure. Only sessions whose model was actually
  chosen are recorded, and only the 64 most recent; the last-used hint is not persisted,
  because it changes on every run.

A field also belongs to the card it was written through: the route travels with the write
(`SetSettingsEndpoint(route, ...)`, `SetSettingsModel(route, ...)`), the document carries
it (`consoleRoutes`), and the user layer reports it on that route only. Otherwise a value
saved on the Anthropic card would appear on whichever card the host is configured with --
the same lie in a new costume. The added fields are optional in the version-1 document, so
the file the operator's host already has still loads.

Tests: `go test ./cli/ -run TestConsoleUserLayer` and
`go test ./cli/ -run TestConsoleModelSelection`, plus the ADR 0102 suite under the new
naming rule, plus `go test ./internal/dshapi/ -run TestSettings` for the user-layer face.

## Shipped: a session exists before its first turn (2026-09-20)

The host ADR 0103 had just restarted could not hold a conversation at all. The operator
picked a model, sent `hello`, and the page said `Failed to load history: session
"run_..." not found (session/not-found)` with no reply. Two defects met on the first
prompt of a new chat, and this chain (**ADR 0104**) fixed both:

- **A created session's empty history was refused.** The console creates a session, then
  loads its history to render the empty conversation, and only then prompts. The id at
  that moment names no run and no log, and `session/page` and `session/follow` both folded
  that into the unknown-id case. A session this host created is a session: `session/page`
  answers an empty page, and follow sends the empty snapshot (cursor -1) and then waits for
  the prompt's run, so the first turn streams over the connection the page already opened.
  The wait is a 50 ms poll bounded by the stream's context, because the run manager has no
  "a run was created" notification and a run that does not exist has no bus to subscribe
  to.
- **Every registered session's first prompt failed on a selection nobody made.**
  `session/create` registers the session so the console's projection has a `modelSelection`
  key for it (ADR 0102); that record carries no choice, and `ApplyModelSelection` read the
  record's *presence* as a choice -- handing `AdapterFor` a provider of `""`. It now tests
  the record's own `selected` flag, which is what every other reader of that map already
  uses. The registration (`d6931c7`) and the reader (`4f96c4d`) came from different chains
  and each was correct alone; a record without a choice is only reachable because
  registration created one.

Only the RPC handler knows which sessions are drafts, so the mount exposes it
(`dshmount.Mux.IsDraftSession`), the stream takes it as `Config.DraftSessions`, and an id
this host never created is still `session/not-found` on both paths.

Tests: `go test ./internal/dshapi/ -run TestSessionPage`,
`go test ./internal/dshstream/ -run TestSessionFollow`,
`go test ./cli/ -run TestRegisteredSessionWithoutAChoice`,
`go test ./internal/dshmount/ -run TestMuxReportsDraftSessions`. Live, on a scratch host
carrying a **copy** of the operator's document under a throwaway `ZENFORGE_CONFIG_DIR`:
`session/create` -> empty `session/page` -> `session/prompt` accepted -> `run.done` with
the model's own answer in the log (`"Hello! I'm ready to assist..."`). The copy was
deleted with the scratch process.

## Shipped: the log is projected into the console's vocabulary (2026-09-20)

The host ADR 0104 had just restarted still could not hold a conversation: the prompt was
accepted, and the page then reported `Failed to load history: session event "run.started"
is not surface-eligible and cannot carry surfaceOp (gateway/internal)`. Two wire facts
were wrong, and the second had been wrong since the follow stream was written.

- **`surfaceOp` is legal on exactly four types** (`system/message`, `user/message`,
  `assistant/message`, `tool/result`). Both read paths wrote `"surfaceOp":"append"` onto
  every record, on the protocol recon's theory that "every durable event is an append on
  the surface" was the simplest legal encoding. The console's own reader throws on any
  other type that carries it, which is the message above. Every other record now omits the
  field.
- **An unknown event name needs the `ignorable: true` marker.** The console refuses to
  interpret a log containing a type outside its generated catalog unless the event says
  omitting it is deliberate. zenforge's names are all outside that catalog, so the
  passthrough arm marks them.
- **The console's transcript is built from its own vocabulary.** Legality alone loads an
  empty conversation, so the log is now *projected* (ADR 0105): `run.started`'s input
  becomes a `user/message`, each step's streamed deltas settle into one
  `assistant/message` with the host's own provenance and token usage, `tool.call` and
  `tool.result` become `tool/call` and `tool/result`, the step and run boundaries become
  `step/start`/`step/end`/`turn/end`, and every other event travels as an ignorable record
  with its own name and payload. One durable event in, one wire event out, wire `seq` =
  durable `seq`, so the cursor, `throughSeq` paging and the live `afterSeq` keep speaking
  the log's own sequence.

`internal/dshwire` holds the projection; the two packages' duplicated `wireEvent` writers
are gone. `internal/dshwire/boundary_test.go` re-extracts the console's known-type list
and surface set from the vendored client bundle and fails when either drifts, and
re-implements the client's envelope rules over a projected turn.

Verified live on a scratch host carrying a copy of the operator's document against the
real provider: `user/message` at seq 1 with `surfaceOp: "append"`, the step's
`assistant/message` with the model's own text ("Hi there, friend!"), its provenance
(`openai` / `qwen-plus`) and usage, a `tool/call` with its `tool/result`, a `turn/end`,
and every other record `ignorable: true`. The copy was deleted with the scratch process.

## Shipped: the sidebar names the operator's task (2026-09-20)

The first conversation the operator opened in the console was listed as "h Create a
concise todo plan for". The host's fallback title (ADR 0028) is derived from the run's
input, and the plan-execute preset feeds its plan stage a copy of the input with
`planner.PlanPrompt` appended — so the stage's own title publication named the harness's
instruction instead of the operator's `h`, and the sidebar read the instruction back. The
run's records were correct all along (`run.started`, and the `user/message` ADR 0105
projects from it, carry `h`); only the title's derivation input was wrong, in the stage
metadata freeze and in the `session.title` event.

`sessionTitleInput` now reads the task the run recorded (`planning.input`) and falls back
to the stage input only when there is none, at both derivation sites (ADR 0106). A
planning session is listed by what the operator typed.

## Shipped: a simple question is answered, not planned (2026-09-20)

The operator typed `who are you` and `h` into the console and got todo lists, a research
loop that read the workspace, and an approval prompt for a shell command. The plan-execute
preset appended `planner.PlanPrompt` to every input and that prompt allowed one outcome
("You must call todo_write with the todo list before giving any final answer"), and a plan
stage that produced no todos *failed the run* (`plan_not_created`) — so the preset could
not answer a question, and the answer the model did write in the plan stage was discarded
because the run's output comes from the summary stage after every todo executes.

`planner.PlanPrompt` now asks the model to decide: plan before acting when the request
needs more than one step, answer directly when it is a question or a single step. A plan
stage that created no todos and produced an answer *is* the run's answer — one model call,
no execute stage, no summary — and `planExecuteTerminal` recognizes that completed `plan`
stage so a resume replays the answer instead of planning again. A stage that neither
planned nor answered still fails `plan_not_created`, and multi-step work is unchanged
(ADR 0107).

## Shipped: a second prompt keeps the conversation's cursor (2026-09-20)

The operator greeted the console, read the reply, and typed a second message. The history
pane answered `Failed to load history: session event stream resumed at a cursor behind the
last applied entry (gateway/internal)`. A console session is a conversation of several
runs (turn one is the session id, turn *k* is `session~k`), each of which numbers its own
durable log from one, while the console keeps **one** cursor for the whole conversation. Both
read paths served only the newest turn's log, so turn two's snapshot cited sequence 5 after
the conversation had reached 42: `opening(item, resumed)` rejects that, `follows` rejects
turn two's first live event, and `assertPageThrough` cannot end turn two's window at a
cursor the client already passed. The earlier turn was unreachable through `Load earlier`
for the same reason.

`internal/dshwire/session.go` now builds a session's log as the concatenation of its turns
in one sequence: each turn is projected afresh and shifted past every event of the turns
before it (`SeqOffset`), carries its own `turn` number, and `session/page` and
`session/follow` both read it, so the RPC surface and the stream cannot disagree. The shift
is derived from the durable event counts, never stored, so any process rebuilds the same
numbers. Two coordinates stay distinct: the session sequence the console cursors on, and
each run's own durable tail, which is where the live follower attaches (ADR 0108).

## Shipped: the sidebar survives a restart (2026-09-20)

Every transcript the console had served was still on disk, and the sidebar showed one or
three rows: `session/list` answers from `RunManager.List`, `zenforge serve` built its run
manager **without a registry**, and a manager with no registry lists its own in-process
records, which `finishLocked` deletes ten minutes after a run goes terminal. A restart
emptied the list outright. On the operator's host, `/tmp/.zenforge/runs` held 45 runs while
the sidebar held one.

`zenforge serve` now opens `harnesshttp.OpenSQLiteRunRegistry` in its state directory
(`<checkpoint-dir>/run-registry.sqlite`, or beside the store file when
`--checkpoint-type sqlite` names one), seeds it from the runs the store already holds, and
hands it to the run manager, so the list is the durable record of every run this install
served -- including the ones served before the registry existed, whose transcripts were
reachable by id but never clickable (ADR 0109). The seeding enumerates through a new
optional `eventlog.RunLister` (memory, JSONL and SQLite all implement it) and reads each
run's own log for its terminal status; a run another process holds is left alone.

Three rules keep those records honest: `RunInfo.Live(now)` requires an unexpired lease, so
a record left by a process that died is not reported as running and the next prompt
continues the conversation instead of steering a run nobody owns; a record whose run never
wrote an event is omitted, because `session/page` answers not-found for it; and a run whose
log has no terminal event is recorded as cancelled when it is adopted. Drafts stay
process-local (ADR 0104).

## Shipped: a follow stream follows the conversation (2026-09-21)

The operator asked a second question and reported that "Load earlier" appeared and then could
not be used. Driving the served console in a headless Chrome over the DevTools protocol showed
both: the click's `session/page` answered 200 with exactly the right earlier records, the
controller applied them (`prependWindow entries: 6 hasMoreArg: false`) — and the button stayed,
because the store the component reads still said `hasMore: true` and, measuring the session's
WebSocket, the host was sending **a fresh snapshot and end every few milliseconds**, each
reconnect re-installing the tail window over the earlier page.

The cause was this host: `runFollow` returned when the followed run's log ended, and the console
treats a stream that ends after its snapshot as a carrier failure
(`RemoteStreamCarrierError("… ended without a terminal result")`) and reconnects at once. Two
turns are needed to notice because one short turn fits in the opening window; the second question
is what makes the window stop covering the conversation.

The stream now follows the conversation: a turn ending is not the stream's end, the next turn's
projection is stamped with the sequence offset `dshwire.Session` derived (`NewestIdentity`), and
the stream waits for that turn and continues the same sequence (ADR 0114). Measured after the
fix on the same conversation: **0** frames while idle (was a snapshot/end pair every few
milliseconds), the click loads the first turn's `hello` and the button disappears, and a new chat
asked twice streams "hello there" and "and again" over the connection that was already open,
each rendered exactly once.

## Shipped: Stop stops the turn that is running (2026-09-21)

`session/cancel` cancelled the id the caller named. The console names the session it has open —
the conversation's first turn — while a conversation of several turns runs its newest one under
`<session>~<k>` (ADR 0108), so Stop on a second turn was answered
`session "…" already finished with status "completed"; cancel is a no-op` while that turn kept
running. Observed on the operator's host against a second turn waiting on approval, and again
while cleaning up this window's verification session.

`sessionCancel` now resolves the conversation's turns and cancels the **newest** one, which is
the only turn that can be running; a session with no turns yet keeps the id it was named by, and
the conflict for a finished turn names the turn it refused instead of the id the caller used
(ADR 0113). Reverting just that resolution makes the new tests fail with exactly the operator's
message, so the regression is pinned in both directions.

## The ledger's "Next up" list was stale (2026-09-21)

The ledger's first gap said `ui-directory-picker-browse` was withheld and that re-testing it
was the next step. That work had already shipped: commit `a32cc8c` ("Load the browse
directory picker instead of withholding it") moved the plugin out of the roster's `blocked`
list after re-testing its activation on a scratch port, and `internal/dshmount/mount_test.go`
keeps it served. The prose was never updated, so the next window would have spent a session
re-doing it. Re-checked live on the operator's host:

```
$ curl -s http://127.0.0.1:8787/                 # window.__DSH_BOOT__
53 entries, including @deepseek-ai/dsh-client-ui-directory-picker-browse
$ curl -s -o /dev/null -w '%{http_code} %{size_download}\n' \
    'http://127.0.0.1:8787/plugins/??@deepseek-ai/dsh-client-ui-directory-picker-browse/client.js'
200 49199
```

Every advertised bundle in the graph answered 200, and the only `inject` names not on the
graph are the shell-provided singletons and one deliberately omitted package -- the
degradation path the console documents. The operator can use the workspace picker's add
action, which is what the withheld plugin had disabled.

The ledger's table rows are cross-checked against the routing table by a test (ADR 0099), so
a *method* row cannot drift; the prose could, which is what happened. The list is now stamped
with its audit date and its derivation, and a test keeps the same misreading from recurring:
no item in "Next up" may name a method the host routes, or a plugin the roster serves
(ADR 0112).

## Shipped: a submitted question is no longer on screen twice (2026-09-21)

The operator asked `hello`, read the answer, and the transcript showed `hello` once more after
it. That trailing copy was the console's local submission echo: it is retired only when a
durable `user/message` arrives whose `source.rpcId` equals the prompt's `requestId`
(`api/session-controller` `observeSubmissionEvent`; `ui-chat` `observedRpcIds` hides the same
echo in the render). This host projected `"source": {"kind": "user"}` with no identity, so the
echo stayed; and because the harness records a queued turn as `request.steer` with its text
under `message` — a key the projection did not read — a queued question never became a durable
message at all.

`Task.PromptID` now carries the prompt's `requestId` into the run: it is checkpointed as
`harness.RunState.PromptID`, published as `run.started`'s `promptId`, and projected as
`source.rpcId` on the user message. `session/prompt` sets it on every turn it starts, and the
queued arm reads `message` and takes its identity from the `steerId` the host queues the turn
under (ADR 0111). Verified end to end through `zenforge serve`: a prompt sent with
`requestId: req-live-1` pages back `"source":{"kind":"user","rpcId":"req-live-1"}`.

## Operator's one remaining step

The host at `127.0.0.1:8787` is restarted onto this checkout, and the document at
`~/.config/zenforge/console-settings.json` holds everything durable: the credential, the
declared `qwen` profile, the revision numbers, the welcome-notice acknowledgement and --
since ADR 0103 -- the model each session chooses. Two things about that restart are worth
knowing:

- A model picked in the composer **before** ADR 0103 is not in the file: the binary that
  would have recorded it did not write selections. Pick it once more and it stays.
- A draft session created **before** this restart is forgotten with the process (a draft
  has no transcript to keep, ADR 0104). The console opens a new one on its next page load,
  and that one works.
- Since ADR 0109 the sidebar is durable, so the conversations served since the state
  directory was created come back after a restart. Only the runs recorded while a registry
  existed are listed: the host's own log is the way to name a session older than that
  (`ls <state-dir> | head`, where the state directory is
  `/tmp/.zenforge/runs` for the host started from `/tmp`).

The OpenAI card comes back empty rather than showing the `--model qwen-plus` flag as a
saved setting (ADR 0103). That is the fix, not a loss: the resolved configuration still
serves `qwen-plus`, and the card shows only what is written on it.

## Kickoff prompt for the next window

> Read `docs/dsh-console-handover.md` end to end, then
> `docs/dsh-console-coverage.md`. Continue the console-attachment objective: keep the
> three layers separate (deep API -> harness core -> adapters, ADR 0099), land every
> chain with an ADR + docs + tests + commit/push + green CI and docs, and fill the
> ledger's gaps in its order. The settings document shipped (ADR 0102), so did the
> user-layer/model-selection fix (ADR 0103), and so did the first-turn fix that made a
> conversation possible at all (ADR 0104), the log is projected into the
> console's session vocabulary so the transcript renders (ADR 0105), a session is titled
> by the operator's own task (ADR 0106), a simple question is answered instead of planned
> (ADR 0107), a second prompt keeps the conversation's cursor so its history loads and an
> earlier turn is reachable (ADR 0108), the host keeps its run registry on disk so the
> sidebar survives a restart and lists the conversations already in the store (ADR 0109), and
> a second question renders as itself because every message is identified by the session
> sequence (ADR 0110), a submitted question is on screen exactly once because the run
> records the caller's prompt identity (ADR 0111), and the ledger's "Next up" list is a dated
> audit that no longer claims the browse directory picker is withheld (ADR 0112).
> Stop reaches the turn that is running (ADR 0113) and a follow stream follows the conversation
> instead of one turn, so "Load earlier" actually loads (ADR 0114). The next chain is "Next up" 1
> in the ledger: `session/openWorkspacePath` and `session/canOpenWorkspacePath`, opening a
> workspace into a session. If a scratch host is
> needed, give it `ZENFORGE_CONFIG_DIR` or `--settings-file` under a throwaway directory so
> it cannot rewrite the operator's document, and its own `--checkpoint-dir` too, because the
> event store and the run registry are derived from it: a scratch host started in `/tmp`
> writes its runs and its registry into the operator's own state directory. Never
> `git add -A`.

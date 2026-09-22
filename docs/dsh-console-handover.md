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
  the log's own sequence. (Since ADR 0121 that is one durable event into *at most* several
  wire events -- a run's opening event becomes the turn's marker and the question behind
  it -- and the served `seq` counts records, so the page and the tail both drain the whole
  group.)

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

## Shipped: the `@` picker reads the directory the host serves (2026-09-22)

`fileReferences/list` is served (ADR 0134), so the console's `@` menu works: its
file section answers, and the `Promise.all` in `ui-reference/client.js:164` that used
to reject whole no longer does. The chain followed the seam pattern the skill catalog
established (ADR 0131): `dshapi.FileReferenceSource` is the adapter's contract,
`cli/filereferences.go` is the walk over the directory `zenforge serve` was started
with, and `dshmount.Config.FileReferences` connects them.

What the vendored schema decided, and what the reference provider specified:

- **The wire is not the usual one.** Scope is `{context: "agent", wire: "agentId"}`
  (the same scope the goals namespace is served under), the parameters are `agentId`
  and `query` with no `request` object, and the result is a **bare array** of
  `{path, kind}` -- no union, no items wrapper.
- **The ranking is the reference's five bands**: exact name, name prefix, name
  substring, path substring, subsequence, with a 25-point directory bonus and ties
  broken by kind, shorter path, then lexicographic order; at most 20 rows.
- **A slash means "list this directory"**, no slash means "search the tree", hidden
  entries need a query that names a dot, the reference provider's skip list is
  skipped, and symlinks are neither listed nor traversed (each directory segment is
  `lstat`ed, and an escaping path answers nothing).
- **Both bounds are the reference's defaults**: 50000 entries into the walk, 20 rows
  out; this host adds a depth cap of 64.
- A read failure answers no candidates; an unopenable workspace leaves the seam nil
  so the namespace answers `unimplemented` naming `FileReferenceSource`.

Two deviations are recorded in the ADR and in `docs/limitations.md`: every call walks
the tree (the reference keeps a background-rebuilt index and answers a bare query
from the stale copy), and every session sees the one directory the host serves (the
reference composes a provider per workspace root).

Live evidence came from a host over a crafted tree (nested `internal/dshapi/`, hidden
`.github/workflows/`, a `node_modules/` and a symlinked `linkdocs`): the empty query
listed the root's own entries with the excluded and symlinked ones absent;
`internal/dshapi/` listed its files; `main` searched the tree; `dshapi` put the
directory above its files; `fg` found `feedback.go` by subsequence; `.github/` and a
bare `.` reached the hidden entries; `../`, `linkdocs/` and an absent directory all
answered `[]`; an unknown scope answered `session/not-found`, a missing query and an
extra argument answered `gateway/arguments-invalid`.

The ledger reads **56 served / 4 streams / 17 refused / 32 unserved** of 109.

## Next: one refusal sweep, then the inbox fold

**1. One refusal sweep, one ADR -- every remaining row is a namespace whose
capability this host does not have.** Refusal is the honest answer, and batching
keeps the ledger's story readable. The rows, with the reason each needs:

- **`terminal/*` (11 rows)** -- no attachment layer, no runtime resize (the PTY
  master is not exposed and nothing calls `pty.Setsize`), no screen model for
  `follow` (no VT emulator, and the job buffer is lossy where a gapless sequence is
  required), no shell discovery, and `--jobs` defaults false.
- **`subagents/*` (3 rows)** -- no child-session plane: children are one-shot runs
  inside the parent run (`<parentRunID>_sub_<taskID>`), streamed off the agent rather
  than registered, absent from `RunManager` and from `session/list`, and both
  `session/page` and `session/follow` refuse the `subagent` address arm on purpose.
- **`sessionReferenceResolver/candidates` (1 row)** -- nothing in Go parses or expands
  an `@`-mention, so a picked row would reach the model as literal text.
- **`fileUploads/upload` (1 row)** -- the same missing attachment store as the
  refused `session/attachment`; reuse that sentence verbatim.
- **`officeToPdf/*` (2), `agentTeams/*` (3), `dynamicCordisRunner/*` (12)** -- no
  converter or PDF library, no agent-team feature, no dynamic plugin runtime. Note
  for the ledger: these families' consumer bundles are dropped from the console build
  (`DROP_PLUGINS` in `scripts/build-console.sh`: `ui-sidebar-documentpreview`,
  `experimental/client-ui-agent-team`, `cordis-client-runner`, with
  `extensions/ui-cordis` blocked in the roster), and `agentTeams/*` is not declared
  in the vendored client at all -- the rows should say that, the way
  `session/workspaceDesktop` says it is absent from the pinned bundle.

**2. Carried debt: the durable inbox fold.** The pending queue remains process state
(ADR 0130). Closing it means queue, claim and clear events in the run's log plus a
projection fold over them, so a queued message outlives its run and a restarted host
can restore it.

## Shipped: message feedback is the session's own log (2026-09-22)

`messageFeedback/put`, `messageFeedback/list`, `messageFeedback/delete` and
`sessionFeedback/record` are served (ADR 0133), so the console's Like/Dislike pair
works and its feedback dialog can record a remark. Reconnaissance had already
established the shape that made this chain cheap: the reference keeps the state as
**session-log events folded on read**, and this host's vocabulary already reserved
`feedback/message-put`, `feedback/message-delete` and `feedback/record` -- so no
store was added, and `appendTitle`'s tail-retry append was the pattern for writing.

Three things about the chain are worth carrying forward:

- **The outcome rides inside the value.** These four methods *succeed* and answer
  `{ok: true, value: …}` or `{ok: false, error: {code: …}}`; the console reads the
  union (`carried.ok`, then `result.ok`, then `result.error.code`). A business
  refusal sent as a method-level error envelope would be a shape its schema does not
  admit, so `feedbackValue` is the only way these handlers answer. A malformed
  request is still a method error: a rating outside the two literals, a category
  outside the seven, a missing `ifVersion` and an unknown argument are all
  `gateway/arguments-invalid`.
- **The fold reads the raw log, not the projected window.** A feedback event
  projects to a console record no transcript renders, so `sessionEvents` walks every
  turn's durable events -- and checks each event's own `sessionId`, so another
  session's events cannot contribute.
- **The version is the concurrency token.** `ifVersion: null` means "observed
  nothing"; a mismatch is `version-conflict` carrying the live item so the client
  reconciles without a second read; a write that repeats the stored judgment keeps
  the version and appends **no** event (the Like button is idempotent); deleting what
  is already gone succeeds without an event. The note policy is the reference host's
  own: 8192 bytes (`maxNoteBytes: 8192`), whitespace-only is `note-blank`, oversize
  reports `maxBytes` and `actualBytes`, and the stored note keeps the operator's
  whitespace.

Two deviations are recorded in the ADR: appends land on the session's newest turn
(the host has no live-session object, and the item is the session's either way --
which also means a remark about a finished conversation is accepted where upstream
refuses it), and the v4 UUID version is minted here from `crypto/rand`.

Live evidence came from a `zenforge serve` whose provider was a local scripted
endpoint, so a turn really produced a finalized assistant message (`msg-9`): the
empty list, a put with note and category, a replace against the observed version
(new version, same `createdAt`), a stale-version `version-conflict` carrying the live
item, `target-not-found` for `msg-9999`, `note-blank`, `note-too-large`
(`maxBytes: 8192`, `actualBytes: 8193`), `session-not-found` for an unknown session,
the method-level `gateway/arguments-invalid` for a bad rating, delete →
`{absent: true}` twice over, delete's own `version-conflict`, and
`sessionFeedback/record` → `{recorded: true}` with and without text. The session's
log then read `… turn/end, feedback/message-put, feedback/message-put,
feedback/message-delete, feedback/record, feedback/record`.

The ledger reads **55 served / 4 streams / 17 refused / 33 unserved** of 109.

## Next at the time: the `@` picker, then one refusal sweep, then the inbox fold

One read and one sweep remain before the ledger has no unserved row, and both are
already sized.

**1. `fileReferences/list` (1 row)** -- the console's `@` picker. With it unserved the
whole `@` menu fails, not just its file section, because the source's `Promise.all`
has no catch (`ui-reference/client.js:164-166`). `workspaceFileScope`
(`internal/dshapi/workspacefiles.go:164`) resolves the scope the reference method
takes, and `tools/workspace/glob.go:133`'s walker already bounds a recursive scan
with the limits a picker wants. Copy the request/result envelope from
`@deepseek-ai/dsh-api-workspace-controller`'s `fileReferences/list` before writing
code.

**2. One refusal sweep, one ADR -- every remaining row is a namespace whose
capability this host does not have.** Refusal is the honest answer, and batching
keeps the ledger's story readable:

- **`terminal/*` (11 rows)** -- no attachment layer, no runtime resize (the PTY
  master is not exposed and nothing calls `pty.Setsize`), no screen model for
  `follow` (no VT emulator, and the job buffer is lossy where a gapless sequence is
  required), no shell discovery, and `--jobs` defaults false.
- **`subagents/*` (3 rows)** -- no child-session plane: children are one-shot runs
  inside the parent run (`<parentRunID>_sub_<taskID>`), streamed off the agent rather
  than registered, absent from `RunManager` and from `session/list`, and both
  `session/page` and `session/follow` refuse the `subagent` address arm on purpose.
- **`sessionReferenceResolver/candidates` (1 row)** -- nothing in Go parses or expands
  an `@`-mention, so a picked row would reach the model as literal text.
- **`fileUploads/upload` (1 row)** -- the same missing attachment store as the
  refused `session/attachment`; reuse that sentence verbatim.
- **`officeToPdf/*` (2), `agentTeams/*` (3), `dynamicCordisRunner/*` (12)** -- no
  converter or PDF library, no agent-team feature, no dynamic plugin runtime. Note
  for the ledger: these families' consumer bundles are dropped from the console
  build (`DROP_PLUGINS` in `scripts/build-console.sh`:
  `ui-sidebar-documentpreview`, `experimental/client-ui-agent-team`,
  `cordis-client-runner`, with `extensions/ui-cordis` blocked in the roster), and
  `agentTeams/*` is not declared in the vendored client at all -- the rows should say
  that, the way `session/workspaceDesktop` says it is absent from the pinned bundle.

**3. Carried debt: the durable inbox fold.** The pending queue remains process state
(ADR 0130). Closing it means queue, claim and clear events in the run's log plus a
projection fold over them, so a queued message outlives its run and a restarted host
can restore it.

## Shipped: the workspace list carries two manual orders (2026-09-22)

`workspace/insertBefore` and `workspace/insertSessionBefore` are served (ADR 0132),
so both drag-and-drop gestures work: reordering the sidebar's workspaces, and
reordering a session row inside one. The data was already there -- the registry
held `order []string` and each row's `sessionIDs`, and the follow stream's
vocabulary already declared an `order` frame that nothing published -- so the chain
was the mutation, its two refusals and the frame:

- **The registry owns both orders.** `InsertBefore` returns the complete resulting
  order (the client replaces its list, so a delta would be unrepairable) and
  `InsertSessionBefore` returns the whole row (the console renders `sessionIds` in
  the row's own order).
- **The splice is the reference's.** Remove the moved id first, find the anchor in
  what remains, insert: that is what makes a downward drag behave, because an anchor
  that sat after the moved row keeps its meaning once the row is gone. An absent
  anchor appends; an anchor naming the moved row is a no-op that publishes nothing.
- **The refusals are upstream's.** Either id being unknown is `workspace/not-found`
  (the reference's mapping for a reorder it cannot perform), and a session or anchor
  that workspace does not account is `workspace/move-invalid` with
  `cannot move session "…" in workspace "…": the session is not accounted` and
  `{workspaceId, sessionId, beforeSessionId?}` -- the anchor key omitted when none
  was sent.
- **The frame exists now.** An accepted workspace move publishes the new order on
  `workspace/follow`, so a second tab sees the reorder instead of a stale list.

Two deviations are recorded in the ADR and in `docs/limitations.md`: a freshly
accounted session is appended where upstream prepends it, and the order is as
process-local as the registry itself (ADR 0094), so a restart returns to the host's
own directory first and to sessions in accounting order.

Live evidence ran with the console's `workspace/follow` stream open: the baseline
listed three registrations, the move returned the new `workspaceIds` **and** the
stream delivered `{"type": "order", "workspaceIds": [...]}`, an unknown row and
anchor both answered `workspace/not-found`, an unaccounted session answered
`workspace/move-invalid` with its two ids, and an unaccounted anchor answered the
anchor sentence with all three.

The ledger reads **51 served / 4 streams / 17 refused / 37 unserved** of 109.

## Next at the time: the feedback store, the `@` picker, then one refusal sweep

Two reads remain that this host can answer truthfully, and both are already sized
by reconnaissance against the vendored schemas.

**1. The feedback cluster** (`messageFeedback/put`, `list`, `delete`,
`sessionFeedback/record` — 4 rows). The machinery is present: message ids exist and
are published (`msg-<seq>`), the event vocabulary already reserves
`feedback/message-put`, `feedback/message-delete` and `feedback/record`, and
`dshapi/session.go`'s `appendTitle` is the exact pattern for a host-authored,
sequence-numbered session event with version compare-and-set. Two things to verify
from the bundle before writing code, because guessing either would be a wire bug:
the results carry **in-value** failures (`{ok:false, error:{code:
"session-not-found" | "version-conflict"}}`) rather than the method-level error
envelope every other namespace uses, and `ifVersion` is a string-or-null
compare-and-set token whose source (the stored row's `version`) has to be read off
the same schema.

**2. `fileReferences/list`** (1 row) — the console's `@` picker. With it unserved
the whole `@` menu fails, not just its file section, because the source's
`Promise.all` has no catch. `workspaceFileScope` resolves the scope and
`tools/workspace/glob.go`'s walker already bounds a recursive scan with the limits
a picker wants.

**3. Then one refusal sweep, as a single chain and one ADR.** Every remaining row is
a namespace whose capability this host does not have; refusal is the honest answer,
and batching them keeps the ledger's story readable:

- **`terminal/*` (11 rows)** — no attachment layer, no runtime resize (the PTY
  master is not exposed and nothing calls `pty.Setsize`), no screen model for
  `follow` (no VT emulator, and the job buffer is lossy where a gapless sequence is
  required), no shell discovery.
- **`subagents/*` (3 rows)** — no child-session plane: children are one-shot runs
  inside the parent run, streamed off the agent rather than registered, and both
  `session/page` and `session/follow` refuse the `subagent` address arm on purpose.
- **`sessionReferenceResolver/candidates` (1 row)** — candidates are derivable, but
  nothing parses or expands an `@`-mention, so a picked row would reach the model as
  literal text.
- **`fileUploads/upload` (1 row)** — the same missing attachment store as the refused
  `session/attachment`; reuse that sentence verbatim.
- **`officeToPdf/*` (2), `agentTeams/*` (3), `dynamicCordisRunner/*` (12)** — no
  converter or PDF library, no agent-team feature, no dynamic plugin runtime. Note
  for the ledger: these three families' consumer bundles are dropped from the
  console build (`DROP_PLUGINS` in `scripts/build-console.sh`: `ui-sidebar-documentpreview`,
  `experimental/client-ui-agent-team`, `cordis-client-runner`, with `extensions/ui-cordis`
  blocked in the roster), so parts of this group have no live consumer on this page
  at all — the rows should say that, the way `session/workspaceDesktop` says it is
  absent from the pinned bundle.

**Carried debt, still open.** The pending queue remains process state (ADR 0130);
closing it means queue, claim and clear events in the run's log plus a projection
fold over them, so a queued message outlives its run and a restarted host can
restore it.

## Shipped: the skill catalog is one catalog (2026-09-22)

`skills/list` is served (ADR 0131), so the console's skills panel works. The
chain's shape was decided by one fact: `zenforge` had a skill catalog in the
framework (`skill/fs`, `skill.NewBundle`, `Config.Skills`) and **never wired it
up**, so a run advertised no skills and the panel had nothing to show. The chain
therefore ran through both halves, and the rule that keeps them honest is that the
panel and the model read the *same* catalog:

- **The CLI owns the catalog.** `--skills` (default `<workspace>/.zenforge/skills`)
  and `--user-skills` (default `<user config dir>/zenforge/skills`, or
  `skills.userDir`), merged with the workspace layer winning by name -- the rule
  the slash-command catalog already follows. A directory that does not exist is an
  empty layer; one that exists but cannot be scanned fails the read, so an
  unreadable directory is never reported as "no skills installed".
- **One catalog, two readers.** `buildSkillCatalog` feeds both the console
  adapter and the run: the run gets the bundle (`load_skill` plus the catalog
  prompt) only when the catalog is not empty, so a host with no skills claims
  none. A panel that listed a skill the agent could not load would be the lie this
  pairing exists to prevent.
- **Upstream's fields are read.** `skill.Descriptor` gained `WhenToUse`,
  `DisableModelInvocation` and `DisableUserInvocation` (negatives, so the zero
  value keeps upstream's "invocable by both" default), and `skill/fs` parses
  `whenToUse`, `disable-model-invocation` and `user-invocable`, refusing the
  retired camelCase spellings with upstream's own sentence.
- **The bundle is the model-facing view.** `NewBundle` drops a model-hidden skill
  from the prompt, the `load_skill` tool, the allowlist and the fingerprint; the
  panel keeps it with `modelInvocable: false`, and drops the user-hidden one.
- **The method validates the session and answers an array.**
  `session/not-found` uses the reference's sentence, an unreadable catalog answers
  `skill listing failed: …`, a host with no seam answers `unimplemented` naming
  `SkillSource`, and an empty catalog answers `{skills: []}` rather than null.

Two deviations are recorded in the ADR: the catalog is host-wide (the reference
scopes it per session composition; this host has one workspace), and `whenToUse`
travels on the panel's rows rather than in the probe the model receives.

Evidence beyond the unit tests came from a live `zenforge serve` whose provider was
pointed at a local endpoint that records the request body and refuses it: the
recorded run contained `Available skills:\n- review: Review a change for
correctness before it ships`, offered `load_skill`, and contained neither the
`disable-model-invocation` skill nor the `whenToUse` line. The panel answered both
rows, `operator` with `"modelInvocable": false`.

The ledger reads **49 served / 4 streams / 17 refused / 39 unserved** of 109.

## Next at the time: two reads this host can serve, and the namespaces it should refuse

The ledger's remaining 39 rows split cleanly now that both clusters have been
reconnoitred against the vendored schemas and this host's machinery. Three of them
are small reads that this host can answer truthfully; the rest are namespaces whose
capability does not exist, and the honest move is to refuse each **by name** in one
chain and record the missing subsystem (ADR 0128's precedent).

**Serve next, in this order.**

1. **`workspace/insertBefore` + `workspace/insertSessionBefore`** (2 rows). The
   registry, titles, deletion and the archived set are already served (ADR 0101);
   only the manual row order is missing, which is a mutation of the order the
   registry already holds. Smallest remaining chain.
2. **The feedback cluster** — `messageFeedback/put`, `list`, `delete` and
   `sessionFeedback/record` (4 rows). The machinery is genuinely present: message
   ids exist and are already published (`msg-<seq>`, `internal/dshwire`), the event
   vocabulary already reserves `feedback/message-put`, `feedback/message-delete`
   and `feedback/record`, and `dshapi/session.go`'s `appendTitle` is the exact
   pattern for a host-authored, sequence-numbered session event with version
   compare-and-set. One wire subtlety to verify from the bundle before writing code:
   these results carry **in-value** failures (`{ok:false, error:{code:
   "session-not-found" | "version-conflict"}}`) rather than the method-level error
   envelope every other namespace uses, so the shape has to be copied from the
   vendored schema rather than assumed.
3. **`fileReferences/list`** (1 row). This is the console's `@` picker, and with it
   unserved the whole `@` menu fails, not just its file section. The pieces exist:
   `workspaceFileScope` resolves the scope and `tools/workspace/glob.go`'s walker
   already bounds a recursive scan.

**Refuse by name, one ADR, with the capability that is missing.** None of these is
a route; each needs a subsystem this host has never built:

- **`terminal/*` (11 rows)** — the job manager runs a command under a PTY
  (`jobs.Manager.Start` → `pty.StartWithSize`), and that is where it stops: there is
  no attachment layer (`controllerId`/`attachmentId`, retained holds), no runtime
  resize (the master is not exposed and nothing calls `pty.Setsize`), no screen
  model for `follow`'s snapshot (no VT emulator, and the output buffer is lossy
  where a gapless sequence is required), and no shell discovery for `shells/`.
- **`fileUploads/upload` (1 row)** — the same seam as the refused `session/attachment`
  (ADR 0128): no attachment store. Reuse that refusal verbatim so the two halves
  cannot drift.
- **`subagents/list`, `prompt`, `interruptByParent` (3 rows)** — a child-session
  plane. Children here are one-shot *runs inside the parent run*
  (`<parentRunID>_sub_<taskID>`), streamed off the agent rather than registered, so
  there is no child session to list, no continuable child to prompt and no
  parent-addressed interrupt; `session/page` and `session/follow` already refuse the
  `subagent` address arm on purpose.
- **`sessionReferenceResolver/candidates` (1 row)** — candidates are derivable from
  the session and workspace registries, but nothing in this host parses or expands
  an `@`-mention, and no resolve remote is declared, so a picked row would reach the
  model as literal text.
- **`officeToPdf/generation|render` (2 rows)**, **`agentTeams/*` (3 rows)**,
  **`dynamicCordisRunner/*` (12 rows)** — no converter and no PDF library; no
  agent-team feature; no dynamic plugin runtime (the roster already blocks the
  cordis panel and `pluginManager/*` refuses for the same reason).
- **`agentTeams/*` also needs a ledger correction, not a refusal.** The vendored
  client does not declare them at all -- the declaring bundle
  (`experimental/client-ui-agent-team`) is dropped by
  `scripts/build-console.sh`'s `DROP_PLUGINS`, exactly as `ui-sidebar-documentpreview`
  (officeToPdf) and `cordis-client-runner` are dropped and `extensions/ui-cordis` is
  blocked in the roster. So those three families have **no live consumer on this
  page**; their rows should say so, the way `session/workspaceDesktop` already says
  it is absent from the pinned bundle.

**Carried debt, still open.** The pending queue is process state, where upstream
folds a durable inbox out of the session's events (ADR 0130, `docs/limitations.md`).
Closing it means queue, claim and clear events in the run's log plus a projection
fold over them, so a queued message outlives its run and a restarted host can
restore it. It is the last row of real work after the reads and the refusals.

## Shipped: the pending queue is the console's inbox cell (2026-09-22)

`session/updateQueue` is served (ADR 0130), so the queue dock works: the messages
waiting for the running turn are projected as the console's `inbox` cell, and every
row can be edited, dropped or steered. The reference keeps that queue as a durable
inbox folded out of the session's events; here the queue is the live run
controller's, so the chain was about *projecting* it and mapping a row id back onto
it:

- **The queue is read, never consumed.** `RunController` gained `PendingSteers`,
  `ReplaceSteer` and `RemoveSteer`, with `RunManager` and `zenforge.Agent` wrappers.
  A peek hands back a copy, an edit rewrites the message in place and keeps its
  position, a removal drops it before the run is handed it, and both mutations
  answer `false` for an id that is no longer pending -- which is exactly what
  `session/queue-item-not-found` reports.
- **`inbox` is a cell, not a baseline field.** The follow snapshot seeds it
  (`values.inbox`, both lists always present, empty lists included) and later values
  arrive as `projection` frames on the control stream, numbered by the queue's own
  wall-clock-anchored counter. It stays out of the control baseline's projections
  block for the reason the goal cell does (ADR 0125): one watermark cannot order two
  counters. `queues` stays `{}`, as the pinned client never reads it.
- **A row's id is the message's id.** Both the row's `id` and its `source.rpcId`
  are the console's own `requestId` -- the steer id the prompt path queued the
  message under -- and the placement (`next-turn` / `next-step`) is the `mode` the
  console chose, recorded when the prompt was queued. `steer` on a `next-turn` row
  promotes it into `next-step`; on anything else it is
  `session/steer-unavailable`.
- **A mutation republishes the cell.** The console holds rows rather than re-reading
  a snapshot, so an accepted edit or removal derives the cell again and announces
  it. Delivery is noticed from the other side: the follow stream reconciles the run
  queue on every record and once more when the run ends, so a message the run was
  handed -- or one that died with it -- leaves the cell and retires the row.

Every refusal is the reference's own, code, sentence and details: an edit carrying
a non-text block is `session/attachment-invalid` "queue edits accept text content
only" with `{reason: "QUEUE_EDIT_NON_TEXT"}`; an edit with no non-whitespace text is
`gateway/bad-request`; an `itemId` the queue no longer holds is
`session/queue-item-not-found` "queued item is no longer pending"; and a steer of
something that is not a queued turn is `session/steer-unavailable` "current turn no
longer accepts steering".

Two deviations are recorded in the ADR. Both lists are delivered at the same
model-turn boundary here (`session/prompt` already maps both modes onto the same
steer queue, ADR 0111), so promotion changes where a row is *shown* and not when
the run receives it; and the queue is process state rather than a durable fold, so
a message queued for a run that ends before the boundary is dropped with the run
instead of being carried into the session's next turn -- `docs/limitations.md` says
so plainly, and the durable fold is the work named below.

Live on a scratch host whose model endpoint holds the connection open (no
credentials, the turn stays live):

```
$ session/prompt {"requestId":"req-queued","sessionId":"run_…","mode":"queue","content":[text "wait for the tests"]}
{"accepted": true}
$ session/prompt {"requestId":"req-steer",…,"mode":"steer","content":[text "and check the logs"]}
{"accepted": true}
$ session/follow <session/…>            # the opening snapshot
  values.inbox = {"next-turn":[{"id":"req-queued","role":"user","content":[{"type":"text","text":"wait for the tests"}],
                   "source":{"kind":"user","rpcId":"req-queued"}}],
                  "next-step":[{"id":"req-steer",…,"source":{"kind":"user","rpcId":"req-steer"}}]}
  projections.asOfSeq = cursor = 8
$ session/control                        # the baseline keeps the queue out of its block
  type=baseline  queues={}  projections[run_…].values={"modelSelection":{…}}
$ session/updateQueue {"sessionId":"run_…","itemId":"req-queued","action":{"kind":"edit","content":[text "wait for the release instead"]}}
{"accepted": true}
  control frame: projection inbox seq 1790058738804
  inbox.next-turn[0].content[0].text = "wait for the release instead"
$ session/updateQueue {…,"action":{"kind":"steer"}}       # the row moves to the steering list
{"accepted": true}
  inbox = {"next-step":[req-queued, req-steer], "next-turn":[]}
$ session/updateQueue {…,"action":{"kind":"steer"}}       # again
{"…","error":{"code":"session/steer-unavailable","message":"current turn no longer accepts steering","details":{"itemId":"req-queued"}}}
$ session/updateQueue {…,"itemId":"req-gone","action":{"kind":"remove"}}
{"…","error":{"code":"session/queue-item-not-found","message":"queued item is no longer pending","details":{"itemId":"req-gone"}}}
$ session/updateQueue {…,"action":{"kind":"edit","content":[image]}}
{"…","error":{"code":"session/attachment-invalid","message":"queue edits accept text content only","details":{"reason":"QUEUE_EDIT_NON_TEXT"}}}
$ session/updateQueue {…,"action":{"kind":"edit","content":[text "   "]}}
{"…","error":{"code":"gateway/bad-request","message":"queue edit content must include non-whitespace text","details":{}}}
$ session/updateQueue {…,"action":{"kind":"reorder"}}
{"…","error":{"code":"gateway/arguments-invalid","message":"\"action.kind\" must be \"edit\", \"remove\" or \"steer\"","details":{"kind":"reorder"}}}
$ session/updateQueue {…,"itemId":"req-steer","action":{"kind":"remove"}}   # and then the turn is cancelled
{"accepted": true}          →  control frame: projection inbox  inbox = {"next-step":[],"next-turn":[]}
```

The live run earned its keep: the production agent was missing the three new
methods, so every queue operation answered `steer-unavailable` while the unit tests
(which use stub agents) stayed green. `server/harnesshttp` now compiles
`*zenforge.Agent` against the manager's own queue interface, so a missing wrapper is
a build failure rather than a silent degradation.

The ledger reads **48 served / 4 streams / 17 refused / 40 unserved** of 109.

## Next at the time: the skills read, and why the subagent cluster is larger

The ledger's next-up item is now the cluster `skills/list`, `subagents/list`,
`subagents/prompt`, `subagents/interruptByParent`. It is not one chain: the skills
read is a projection of something this host already has, and the subagent methods
describe a subsystem this host has never built. Sizing both, so the next window
does not re-derive them.

**`skills/list` is the small one, and it is closeable.** The console asks
`{sessionId}` and expects `{skills: [{path?, name, description, whenToUse?,
modelInvocable}]}` (the row's only required fields are `name`, `description` and
`modelInvocable`). This host already has the plumbing: `skill.Catalog.List` returns
`[]skill.Descriptor` (`{name, description, license, compatibility, metadata}`), the
filesystem catalog (`skill/fs`) discovers them under a root, `skill.NewBundle`
freezes them with an allowlist and renders the catalog prompt plus the `load_skill`
tool, and the serve command can hold that bundle. Three decisions are the work: what
`modelInvocable` means here (the honest answer is "listed in this session's allowlist
and therefore loadable by the model", which the bundle already computes), where
`path` comes from (the descriptor does not carry one today -- `Content.Provenance`
does, so the catalog would have to expose the package directory it already knows),
and `whenToUse` (this host's frontmatter has no such field, so it is omitted rather
than invented). Nothing about the console's search or badges needs more.

**The subagents cluster needs a child-session plane, not a route.** The console's
three methods describe *live child agents of a parent session*:
`list(parentSessionId) → {entries: [{kind: "child", id, activity: "running" |
"inactive", hasChildren, mode: "one-shot" (+ optional label) | "continuable"}]}`,
`prompt({requestId, parentSessionId, childSessionId, mode: "continuable",
delivery: "queue" | "steer", content}) → {messageId}` (a later message to a
continuable child), and `interruptByParent(childSessionId, parentSessionId,
"continuable") → {accepted: true}` (three bare parameters, not an object). This
host's subagent layer is a task orchestrator: `subagent.Orchestrator.Invoke` runs
`SubAgentSpec`s from a `Registry` as *tasks inside the parent's own run*
(`tools/task`), returning results -- there is no child session id, no activity
state, no continuable child to prompt later, and no parent-addressed interrupt. So
a chain that serves these has to decide first whether a child is a **session** this
host can open and project: `session/page` already refuses the `subagent` address arm
("this host serves top-level runs only"), and a console that lists children will
click them. That is the chain to scope, and it is larger than the skills read.

After those: `terminal/*` (an embedded terminal this host does not claim),
`workspace/insertBefore` + `workspace/insertSessionBefore` (drag-to-reorder only),
and the long tail (`messageFeedback/*`, `sessionFeedback/*`, `fileReferences/list`,
`fileUploads/upload`, `officeToPdf/*`, `agentTeams/*`,
`sessionReferenceResolver/candidates`, `dynamicCordisRunner/*`).

**Carried debt from the queue chain.** The pending queue is process state, where
upstream owns a durable inbox folded out of the session's events
(`inboxProjectionDefinition` applies the splice events, which is why its cell can
sit in the control baseline at all). Closing that here means queue, claim and clear
events in the run's log plus a projection fold over them, so a queued message
outlives its run and a restarted host can restore it. It is worth doing before the
console grows any expectation that a queued message outlives its turn, and it is not
a prerequisite for the skills read.

Kickoff for the next window: read this file's newest section and
`docs/dsh-console-coverage.md`, then build **`skills/list`** as one chain (ADR 0131)
-- it is the item that needs no new plane.

## Shipped: a fork copies the source's completed turns (2026-09-22)

`session/fork` is served (ADR 0129), so both console affordances work: the
sidebar's "fork" on a session row and a message's "fork from here" (which sends
`atSeq`, the console sequence of that message).

The reference seeds a child session with a slice of the source's events
(`agents.create({seed, inheritedEventCount})`); this host has no seeded-session
path, so a fork is a **copy**: the source's turn logs up to the boundary are
written into the child's own run chain, and the child then owns its history. Three
pieces made that possible, and each is reusable:

- **`dshwire.SessionLog.TurnRecords`** (with `TurnContaining`) names the turn a
  projected record belongs to. The served sequence cannot: a turn that contributed
  no records repeats the continuation point of the one before it (ADR 0117). The
  boundary rule needs the turn, because a fork inherits **whole turns** -- turns
  are runs here, which is where the reference's "cut, then advance to the next
  `turn/start`" lands too.
- **`RunManager.Record`** adds a terminal run record for a run this host
  materialized instead of executed. The console's session list is built from run
  records, and a conversation the host holds but does not list is one the sidebar
  cannot show; the record uses the same claim-and-release the boot adoption of
  stored runs uses, so it is durable in the registry (proved by a restart test).
- **The lineage travels in the log.** The reference keeps `parentSessionId` in
  session metadata; this host has no such plane, so the child's first turn opens by
  naming its source, `SessionLog.ParentSessionID` reads it, and `session/list`
  serves it -- the field the console's `flattenLineage` nests a child under its
  source with.

The boundary rule and its refusals are the reference's, word for word: the first
`turn/end` at or after `atSeq`, else the last `turn/end`; no boundary is
`session/fork-unavailable` with either "has not completed the turn containing
event N" or "has no completed turn to fork from"; an unknown session is
`session/not-found`; a bad `atSeq` is `gateway/bad-request` "atSeq must be a
non-negative safe integer"; and a workspace attach failure is
`session/workspace-attach-failed` carrying the child's id, which the console reads
out of the error to open the child anyway. Two deviations are in the ADR: the child
gets an ordinary `run_<nanos>` id (collision-probed) rather than
`session-<uuid>`, and the fork is a snapshot whose copied records keep their
original times while the child's own record is stamped with the fork's moment.

Live on a scratch host with no model credentials (the prompt is recorded and the
failed turn still ends, so a fork is provable without a provider):

```
$ session/prompt A "the quarterly report discusses the harbor crane budget"
$ session/fork {"sessionId":"run_…643975000"}          # the source
{"…","result":{"ok":true,"value":{"sessionId":"run_…719713319000"}}}
$ session/page child
  seq 1 turn/start   seq 2 user/message "the quarterly report …"   seq 9 turn/end
$ session/list
   run_…719713319000 updatedAt=… parent=run_…643975000
   run_…643975000    updatedAt=… parent=None
$ session/prompt child "a follow-up question"          # continues the inherited conversation
$ session/page child
  seq 1 turn/start … seq 9 turn/end | seq 10 turn/start seq 11 user/message "a follow-up" seq 18 turn/end
$ session/fork {"sessionId":child,"atSeq":2}           # a two-turn source, cut inside turn 1
  → a child whose page holds turn 1 alone (9 records, no follow-up)
$ session/fork {"sessionId":<draft>}
{"…","error":{"code":"session/fork-unavailable","message":"session \"run_…\" has no completed turn to fork from"}}
$ session/fork {"sessionId":child,"atSeq":-1}
{"…","error":{"code":"gateway/bad-request","message":"atSeq must be a non-negative safe integer","details":{}}}
$ session/fork {"sessionId":"run-missing"}
{"…","error":{"code":"session/not-found","message":"session \"run-missing\" not found","details":{"sessionId":"run-missing"}}}
```

The ledger reads **47 served / 4 streams / 17 refused / 41 unserved** of 109, and
the next-up list is down to one item: `session/updateQueue`.

## Next at the time: the pending queue (shipped as ADR 0130, above)

**`session/updateQueue`** edits the messages the console has queued for a
conversation's next turn: `{sessionId, itemId, action}`, where the action union
carries at least `{kind: "edit", content: [...]}` and `{kind: "steer"}`, answering
`{accepted: true}`; the client calls it from `ui-conversation`'s `steerQueue`,
which reads the rows from the `inbox` face's `next-turn` list, and expects the
host's own `session/steer-unavailable` and `session/queue-item-not-found` codes.

What is missing is the projection, not the route. This host's control baseline
publishes an empty `queues` map **by design** -- `sessionQueuedItem`'s comment says
"no queue mirror yet" -- and the baseline's `Queues` key must stay present even
while empty, because the client's `replaceControlBaseline` throws on an absent key
and discards the whole baseline (ADR 0119). The queue's existing half is the prompt
path: a `queue` or `steer` prompt for an active run goes through
`RunManager.Steer`, whose message is already projected (ADR 0111). A chain that
serves this method therefore has to (a) mirror the pending items into the control
baseline and the follow snapshot, with the `id` and `placement` the client renders,
(b) map an `itemId` back to the queue entry, and (c) refuse an item it cannot find
with the client's own code rather than editing the wrong message.

## Shipped: the attachment read is refused (2026-09-22)

`session/attachment` is routed and refused by name (ADR 0128). The console's
read half asks for an attachment's image metadata **and** its `data` bytes, and
this host can never have one: `session/prompt` already refuses a prompt whose
content carries an image or file part ("prompt content part \"image\" is not
supported: this host accepts text parts only"), the command surface refuses
submitted attachments, and `fileUploads/upload` is not served, so no id can come
into existence. The refusal names the missing store and the working substitute --
put the file in the workspace and ask the agent's own file tools to read it --
while the method's declared `sessionId`/`attachmentId` are still validated first,
so a typo is reported as a typo. Live:

```
$ session/attachment {"sessionId":"run_…","attachmentId":"att-1"}
{"…","error":{"code":"unimplemented","message":"this host has no attachment store: a prompt's
image and file parts are refused when they are submitted, so there is no attachment to read;
put the file in the workspace and ask the agent to read it","details":{"capability":"an
attachment store"}}}}
$ session/attachment {"sessionId":"run_…","attachmentId":"att-1","mediaType":"image/png"}
{"…","error":{"code":"gateway/arguments-invalid","message":"unexpected argument \"mediaType\""}}
$ session/prompt … content [text, {type:image}] …
{"…","error":{"code":"session/unsupported-content","message":"prompt content part \"image\" is
not supported: this host accepts text parts only"}}
$ curl -o /dev/null -w '%{http_code}' -X POST …/api/fileUploads/upload
404
```

The ledger now reads **46 served / 4 streams / 17 refused / 42 unserved** of 109,
and `docs/limitations.md` says plainly that the composer's attach affordance
cannot work here and why (its older bullet listing "search, attachments" among the
bare-404 namespaces was corrected: one is served, the other refused by name).

## Next at the time: the two session-management methods left, sized (both shipped)

The ledger's next-up item is now `session/fork` then `session/updateQueue`, and
both were surveyed while landing the attachment refusal, so the next window does
not have to re-derive them.

**`session/fork` is the larger one** and needs a mechanism this host does not
have. The reference (`@deepseek-ai/dsh-api-session-controller`, `ApiSessionList.fork`)
does exactly this:

- validate `atSeq` as a non-negative safe integer, else `gateway/bad-request`
  "atSeq must be a non-negative safe integer";
- observe the source session; a missing one is `session/not-found` with
  `{sessionId}`;
- find the boundary: the first `turn/end` at or after `atSeq`, or -- when `atSeq`
  is absent or past the log -- the **last** `turn/end`; no boundary is
  `session/fork-unavailable` with either "has not completed the turn containing
  event N" or "has no completed turn to fork from";
- cut at `boundary + 1`, advanced forward to the next `turn/start`;
- mint a child id and create it with the parent's events `[0, cut)` as a **seed**
  (`agents.create({sessionId, seed, inheritedEventCount: cut, meta: {cwd,
  parentSession, isSeeded, agentPreset}})`), attach it to the source's workspace
  (failure is `session/workspace-attach-failed` with `{sessionId, workspaceId}`),
  and answer `{sessionId}`.

This host's sessions are runs in an event store with no seeded-log creation path,
so forking is a chain of its own: a durable fork descriptor plus a way to
materialize the child's prefix (the child's first turn would have to start from
the parent's prefix messages, which is what `session/prompt`'s continuation path
already builds from a log). `atSeq` here is the **console** sequence the page
serves, not the durable event sequence, so the boundary rule has to be applied to
the projected records.

**`session/updateQueue`** edits the pending queue: `{sessionId, itemId, action}`
where the action union carries at least `{kind: "edit", content: [...]}` and
`{kind: "steer"}`, answering `{accepted: true}`; the client calls it from
`ui-conversation`'s `steerQueue` (which reads the rows from the `inbox` face's
`next-turn` list) and expects the host's own `session/steer-unavailable` and
`session/queue-item-not-found` codes. This host's control baseline publishes an
empty `queues` map by design -- `sessionQueuedItem`'s comment says "no queue
mirror yet" -- so the queue projection has to be fed before the mutation means
anything; `session/prompt`'s steer path is the existing half of that model.

## Shipped: the sidebar's search reads the logs the list shows (2026-09-22)

`session/search` is served (ADR 0127), so the workspace browser's search box has a
host answer. The plugin calls it with one literal phrase and a signal
(`client/ui-workspace`'s `searchSessions`, via the session manager's "Search
visible session message content"), and what the sidebar renders is the contract:
one row per conversation, the reference's twenty-result cap with `hasMore`, and a
240-code-point excerpt.

This host mounts no query provider -- the reference answers from
`@deepseek-ai/dsh-session-query`, whose refusal is literally "this deployment does
not mount ..." -- and it does not need one: it already reads and projects exactly
these conversations for `session/list`, `session/page` and `session/follow`. Two
reusable pieces came out of that:

- **`visibleSessions`** is now the one enumeration `session/list` and
  `session/search` share (the grouping of a conversation's turns, the blank
  filter, the newest-first order, and the projected log each row was built from).
  A session is therefore searchable exactly when it is listed, and a conversation
  whose log cannot be read is skipped rather than answered with an invented hit.
- **The reference's own rules are mirrored where the client can see them**: the
  matcher is its "literal case-insensitive, whitespace-flexible" filter (the
  phrase is data, its metacharacters literal, its words separable by any
  whitespace), and the refusals are its own words -- `gateway/bad-request` for a
  blank, over-500-UTF-16-unit or NUL-bearing query, `gateway/arguments-invalid`
  for a missing or non-string `query`, which its strict schema rejects before the
  handler runs.

Three deviations are consequences of having no index, and they are in the ADR
rather than hidden: a row quotes the conversation's **newest** matching message
(the reference's `bestMatch` is rank-ordered), rows keep the list's newest-first
order rather than a global relevance order, and the `signal` the client passes has
nothing to interrupt because this host's unary transport does not carry one. The
excerpt is ours too: a window starting up to 60 code points before the match with
a leading ellipsis, then the reference's longest-prefix cut to 240 code points.
Only `user/message` and `assistant/message` records are searched -- a tool result
is not message content -- which is the reference's event filter, and our sessions
are linear, so its `surface: current` filter is a no-op here.

Live evidence on a scratch host (`--addr 127.0.0.1:8803`, throwaway
`--checkpoint-dir`/`--settings-file`), with no model credentials at all: the
prompt is recorded and the conversation is listed before the model is called, so
the search is provable without a provider.

```
$ session/prompt "the quarterly report discusses the harbor crane budget"
$ session/search {"query":"harbor crane"}
{"…","result":{"ok":true,"value":{"hasMore":false,"items":[{"sessionId":"run_…297714000",
"snippet":"the quarterly report discusses the harbor crane budget"}]}}}
$ session/search {"query":"QUARTERLY   REPORT"}          # multi-space, case-folded
{"…","items":[{"sessionId":"run_…297714000","snippet":"the quarterly report discusses the harbor crane budget"}]}
$ session/prompt (second conversation) "the harbor crane quote arrived late"
$ session/search {"query":"harbor crane"}
{"…","items":[{"sessionId":"run_…321548000","snippet":"the harbor crane quote arrived late"},
              {"sessionId":"run_…297714000","snippet":"the quarterly report discusses the harbor crane budget"}]}
$ session/search {"query":"  "}
{"…","error":{"code":"gateway/bad-request","message":"session search query must not be empty","details":{}}}
$ session/search {"query":"report","limit":3}
{"…","error":{"code":"gateway/arguments-invalid","message":"unexpected argument \"limit\""}}
```

The row order is exactly `session/list`'s (`run_…321548000` then
`run_…297714000`), which is the point of sharing the enumeration. The ledger now
reads **46 served / 4 streams / 16 refused / 43 unserved** of 109, and next-up
item 1 narrows to `session/fork`, `session/attachment` and `session/updateQueue`.

## Shipped: the desktop question is answered, the open refused (2026-09-22)

`session/canOpenWorkspacePath` and `session/openWorkspacePath` are routed
(ADR 0126), which closes the ledger's next-up item 1 -- but not the way that item
described it. The prose read the pair as "opening a workspace into a session, the
other half of selection", and the reference says they are about the **machine
serving the console**: `canOpenWorkspacePath()` is "Report whether this deployment
can hand a Session workspace path to a native desktop", and `openWorkspacePath`
opens a path *on the Host desktop*, with `action: "reveal"` for a file-manager
reveal and omission for the default application. The caller upstream is the
desktop carrier (`dshDesktopBoot`), which the protocol recon had already listed as
an explicit non-goal, and no bundle in our pinned client calls either method. So
the honest answer is split: a capability **question** is answered, and an
**operation** this host cannot perform is refused by name.

The probe answers `false` as a bare JSON boolean. The declared result is
`boolean()`, not an object, and a caller branches on it -- an `unimplemented`
error would read as "this host is broken" instead of "this host has no desktop",
which is the difference between a disabled affordance and a failed page. The
operation answers `unimplemented` with the missing capability in its details and
one sentence naming the reason *and* the substitute:
`workspaceFiles/list`/`workspaceFiles/read` show a file inside the session. Its
declared request fields (`path`, `action`) are validated *before* the refusal so a
typo is still reported as a typo, while a **valid** request is refused too: what is
missing is the desktop, not an argument.

No opener was built, and that is the decision rather than an unfinished edge.
Serving it would spawn a native file manager (`open -R`, `explorer /select,`,
`xdg-open`) from the host process on behalf of any page that can reach the
loopback API -- a side effect on the operator's own machine, for a method with no
caller in the pinned bundle and no desktop carrier in this deployment. If a
desktop carrier is ever shipped, the probe flips to `true` and the operation is
implemented in the same change.

Live evidence on a scratch host (`--addr 127.0.0.1:8801`, throwaway
`--checkpoint-dir`/`--settings-file`):

```
$ curl -s -X POST http://127.0.0.1:8801/api/session/canOpenWorkspacePath -d …args {}…
{"type":"server-response","rpcId":"p","result":{"ok":true,"value":false}}
$ curl -s -X POST http://127.0.0.1:8801/api/session/openWorkspacePath -d …args {"path":"…/ws","action":"reveal"}…
{"…","result":{"ok":false,"error":{"code":"unimplemented","message":"this host serves the
console in a browser and has no desktop carrier to open a path on; workspaceFiles/list and
workspaceFiles/read show a file inside the session instead",
"details":{"capability":"a desktop carrier to open a path on"}}}}
$ curl -s -X POST http://127.0.0.1:8801/api/session/openWorkspacePath -d …args {"path":"…/ws","reveal":true}…
{"…","error":{"code":"gateway/arguments-invalid","message":"unexpected argument \"reveal\""…}}
```

The request splice (`{"request":{…}}`) and the flat form both reach the same
refusal, because `unwrapRequestArguments` flattens the object and drops the
wrapper. The ledger now reads **45 served / 4 streams / 16 refused / 44 unserved**
of 109, and its next-up list starts at `session/search`, `session/fork`,
`session/attachment`, `session/updateQueue`.

One piece of machinery was generalized while pinning these envelopes: the
bundle-truth readers that used to live in the goal tests are now a
`vendoredBundle` (source, package, namespace), so both families read the same
generated remote map in `webui/dsh/plugins/api/remotes/client.js` instead of two
hand-copied parsers. What the goal tests assert did not change.

## Shipped: the goal dock reads the framework's goal state (2026-09-22)

The seven `goals/*` methods, the `goal` projection cell and
`goal/activation-changed` are served (ADR 0125), so the composer's goal bar is
live: it renders the phase and objective and pauses, resumes, edits and clears
the current goal. The rules are the framework's own -- `goals.Create/Edit/Pause/
Resume/Complete` over the durable state in `<checkpoint-dir>/goals`, the same
directory `zenforge goal` and the goal tools use -- and the console's half is the
envelope vocabulary, which the store adapter translates in `cli/consolergoals.go`
plus `internal/dshapi/goals.go`.

Two things the earlier survey had wrong, worth recording so the next window does
not repeat them:

- **The projection carries no activation.** Upstream's `GoalProjection` is
  `{goal, roundsStarted, createdAt, updatedAt}` ("activation is process-local
  and deliberately absent"), and `useProjection("goal")` is `GoalProjection |
  null`. Activation arrives separately, from `goals.get` and from the
  `goal/activation-changed` emit, and the dock matches it **per `{id, revision}`**
  -- so an activation for a superseded revision renders as none and the bar loses
  its pause/resume buttons. Both carriers are therefore load-bearing, and they
  ride one change feed so they cannot disagree.
- **`clear` is a tombstone in the *checkpoint state*, not in the projection.**
  Upstream's state is `{current, seenGoalIds, failure}`: the cell goes to `null`
  while the identities are retained to refuse reuse. This host deletes the state
  document instead, so id reuse and a pre-clear ref both report
  `GOAL_NOT_FOUND`. The dock cannot tell, and the deviation is in ADR 0125.

The projection needed a sequence of its own and that is the subtle part. A
baseline block in `session/control` and the history seed in `session/follow` each
carry **one** watermark for every cell in the block, and the client discards a
value numbered at or below the watermark it already holds
(`api/session-controller`, `ProjectionValueStore.apply`/`seed`). The
model-selection cell is numbered by its own store's sequence, so folding a goal
into the control baseline would raise that block's watermark above the model
picker's own frames and freeze it. The goal cell therefore travels with the
session's own follow snapshot -- which also keeps the client's seed from clearing
it -- and later values arrive as control frames numbered by a wall-clock-anchored
monotone counter that outranks any session cursor. Had the frames been numbered
below the cursor, a `resume` would be discarded and `activeRef` would keep seeing
a paused projection: the dock would never re-read activation and the goal would
look stuck. The live probe confirms the shape:
`{"type":"projection","key":"goal",…,"seq":1790044141036}` with no activation in
the cell, and `{"type":"emit","event":"goal/activation-changed","args":[
{"sessionId":…,"goal":{"id":…,"revision":3,"activation":"armed"}}]}`; a cleared
goal emits `{"sessionId":…}` with no goal at all.

Live RPC evidence on a scratch host (`--addr 127.0.0.1:8799`, throwaway
`--checkpoint-dir` and `--settings-file`), one session, walking the whole
lifecycle:

```
$ curl -s -X POST http://127.0.0.1:8799/api/goals/get -d …args {"agentId":"run_…"}…
{"type":"server-response","rpcId":"p","result":{"ok":true}}
$ curl -s … /api/goals/create … {"agentId":"run_…","objective":"document the goal dock","maxGoalRounds":4}
…"value":{"ref":{"id":"goal-1790044153281347000","revision":1}}}
$ curl -s … /api/goals/pause … {"agentId":"run_…","ref":{"id":"goal-1790044153281347000","revision":1}}
…"ok":false,"error":{"code":"GOAL_STALE_REVISION","message":"goals/pause: goal goal-… is at revision 2, not 1"…
$ curl -s … /api/goals/clear … ref revision 1 of the second goal
…"value":{"id":"goal-1790044153364217000","revision":1}}
$ curl -s … /api/goals/get …
{"type":"server-response","rpcId":"p","result":{"ok":true}}
```

The no-goal read answers **no `value` key at all**, which is the reference's own
`undefined` arm and the only safe shape: the client's transport passes
`result.value` through and the activation source tests `goal === void 0`, so a
JSON `null` would reach `goal.id` and throw inside its `.then`. Live evidence also
caught a bug in id minting -- the first version suffixed every goal after the
first one (`goal-<nanos>-1`) because it tested the suffixed candidate and never
the free base -- now fixed and pinned by
`TestConsoleGoalIdentifiersSuffixOnlyOnCollision`.

**What is still open in this panel** (also in `docs/limitations.md`): the
composer's `/goal` command is a built-in *host* command
(`@deepseek-ai/dsh-command-goal`) this host does not register, so the dock cannot
create; a goal is created host-side (`serve --goals` and the `create_goal` tool,
the command line, or the state document). `goals/complete` is served but the
shipped dock never calls it. Activation is derived from durable state
(`armed` = active with budget left) rather than from a live scheduler, because
`zenforge serve` does not run the framework's goal-continuation loop.

## Shipped: the stream chunks and the interrupted tool call (2026-09-21)

Three things the console renders were missing from the served stream (ADR 0124). The usage
pill reads its counts from the **compacted stream** (`lastAssistantStreamChunk(stream,
"usage")`, then `normalizeUsage`, which needs `inputTokens` and `outputTokens`), and this
host only ever put them on the settlement record -- so the counts were on the wire and the
pill still never appeared. There was no `finish` chunk, which is what tells a finished
answer from one waiting on tools. And a run cancelled while a tool was running left that
call unanswered, so the console showed the card as running forever.

The live attempt now streams a `usage` chunk (the console's token names) and a `finish`
chunk whose reason is `stop` or `tool-calls` from the durable `toolCallCount`, and both are
appended to the compacted stream as verbatim chunk records -- the shape the client's own
expander passes through, so a loaded session reads the same pill as a live one. A cancelled
or failed run answers each outstanding call with `isError: true` and
`error: {name: "Interrupted", code: "interrupted"}`, upstream's own pair, which the console
renders as "stopped".

Live: a real qwen-plus turn that ran a shell tool served a stream the bundle's own
`expandAssistantStream` expands to `text-delta ×7, usage, finish` with `normalizeUsage`
reporting the pill renders, and a turn cancelled during `sleep 45` served the interrupted
tool result; `validateSessionEventData` accepted all twelve records.

Not done: a failed tool keeps only `isError: true` (upstream has no execution-failure code to
mirror), and tool-call deltas are still not streamed.

## Shipped: the prompt cards (2026-09-21)

The chat flow's two prompt cards were empty. `system/message` is surface-eligible and was
never emitted, and `request/header` had no anchor at all -- while the assembled prompt
existed only as **messages in the checkpoint**, which a projection cannot read.

A run now writes one durable `system.prompt` at the step that first sends the prompt, and
the projector turns its sections into one appended `system/message` per section, stamped
with the turn and step the harness assembled them for (ADR 0123). Each attempt that opens a
request emits `request/header` with `{header:{config:{provider,model}}}` and its reason --
omitting `header.system`, which the console's validator rejects outright, and empty `tools`.
A second step on the same route mints no second card, matching upstream's "only when the
request changed" rule.

Live: one real qwen-plus turn served 11 records including both `system/message` sections
(one persona, one environment context) and the header, and the console bundle's own
`validateSessionEventData` accepted all eleven.

Not done, and said so in the ADR: `request/context` has no consumer in the shipped console
and this host records no per-request context window.

## Shipped: the built-in provider routes are editable (2026-09-21)

The Models page could not edit the routes this host is built to serve. The console
picks a provider's editor by the *name* of the settings namespace the directory entry
points at -- it knows `llm-deepseek` and `llm-pi-ai`, and reports anything else as
`unknown`, which renders a card with no fields and a disabled save. This host advertised
OpenAI and Anthropic under namespaces of its own invention, so both were uneditable
hints while a hand-declared route (already pointing at `llm-pi-ai`) edited fine.

Every configurable route is now a pi-ai card at `providers.<route>` (ADR 0122), and the
two built-ins are seeded with what the host is actually configured with: the live route's
own base URL and model, the protocol from the schema's union, and -- for a route the host
is not running on -- its own credential variable, so it cannot borrow a key for a
different service. The live route stays one group in the model picker: its seeded profile
supplies that group's models instead of adding a second entry for the same endpoint.

## Shipped: the turn and step boundaries (2026-09-21)

The console anchors its turn-process row on `turn/start` — `match: (event) => event.type
=== "turn/start" ? { id: String(event.data.turn), role: "start" } : …` — and this host
never sent it, so a turn had no start for its step timeline or its token row to hang
off. It also never sent `step/end`, for a reason the projection hid: the mapping from a
durable `step.done` had always been there, but nothing in `harness/runner.go` ever wrote
one, so the console's attempt reducer kept the step open for the rest of the run.

Both are served now (ADR 0121): the runner closes a step once its model call settles and
every tool it asked for has resolved, and the projector opens a turn with
`turn/start {turn}` before the question. Live-verified against qwen-plus:
`turn/start#1 {turn:1}, user/message#2, session/title#3, session/title#4,
step/start#5 {step:1,turn:1}, assistant/message#6, step/end#7 {step:1,turn:1},
turn/end#8 {reason:{kind:"completed"},turn:1}`.

That change made one durable event produce two records for the first time, which exposed
a real bug in the live tail: it sent only the last record of an append while the
projection counted both, so every later frame was numbered one past the cursor the
console held — the reconnect rule it enforces. The tail now sends the whole group, and
the socket test pins both frames of a new turn.

## Shipped: the file surfaces and the forwarded-event answers (2026-09-21)

The fifth audit slice found three more deviations, two of them operator-visible.
`workspaceFiles/changes` had no stream route, and the console's file provider opens
that subscription *before* it stats the path — so every `@file` reference opened a
tab that stayed `loading` forever. `workspaceFiles/readAll` returned a text arm
where upstream returns bytes, which the document preview base64-decodes. And a
failed `$events/result` call — for an unknown `eventId`, a delegating
`{kind:"next"}`, a listener failure `{kind:"rejected"}` — tore down the whole
forwarded-event stream and re-delivered every pending waterfall; the shipped
approval panel sends `next` whenever it cannot scope the owning session, so this
was reachable on every background approval.

All three are fixed (ADR 0120): the watch stream answers `ready` (and no `change`
frames, because this host watches no files), `readAll` is the bytes arm, and an
unusable forwarded-event answer is a success that decides nothing.

The same audit proved a live defect in ADR 0118's code by running upstream's own
`expandAssistantStream` over this host's output: the reconnect baseline's compact
prefix marshalled `dt: null` for a block with exactly one delta, and the validator
throws `TypeError: text-chunks dt must contain safe integers` — so a mid-answer
reconnect whose prefix held a short reasoning block died terminally instead of
resuming. The gap list is now always an array, and a socket test asserts it.

## Shipped: the console's baselines are complete (2026-09-21)

A file-by-file audit of the host against the client the console actually ships
(`ddefc45` / `0.1.6-alpha.2`, not the `0.1.5-rc.2` npm artifacts — the recon now
records that distinction, and five result schemas differ between them) found three
opening baselines the console was silently discarding. The control baseline omitted
the **required** `queues` key, whose absence makes the client's
`replaceControlBaseline` throw before it seeds any projection. The follow
snapshot's `assistantStream.revision` was hardcoded to 0 even after ADR 0118 made
the stream replay a turn's log and number its frames, so the first live frame was
rejected as a skipped revision — measured, the stream delivered **zero** frames
after a reload. And the conversation's name was served as a field of
`session/list`, which nothing renders: the client reads the `title`
**projection**.

All three are fixed (ADR 0119). Measured after the fix: reloading mid-answer
produced a baseline with `revision: 22` and `activeAttempt.nextIndex: 21`, and the
stream then delivered 21 more chunk frames plus the settlement, with no second
`start`; every `session/list` row carries
`projections.values.title` with its watermark; the durable title now reaches the
wire as `session/title` in the shape the projection fold expects.

## Shipped: a reconnect mid-answer resumes the attempt (2026-09-21)

Since ADR 0117 the console renders live prose from the dense frames alone, so a page
reload in the middle of an answer had nothing to repaint from: measured live, the
snapshot's baseline carried `activeAttempt: null`, the next delta announced a *new*
attempt with a `start` frame, and only the tokens after the reload rendered — the
partial answer appeared to restart from the middle. The follow stream now rebuilds
its tracker by replaying the newest turn's durable log through the same projection
the live tail uses, and hands the still-open attempt to the snapshot as upstream's
`assistantStream.activeAttempt` (attempt id, `startedAfterSeq`, turn, step,
`nextIndex`, and the compact prefix of its chunks). The live tail continues that
same tracker, so no second start follows (ADR 0118).

Measured after the fix, reloading mid-answer: the baseline carried `nextIndex: 10`
with a two-record compact prefix, **no `start` frame followed**, and the partial
answer was on screen immediately. Two traps are worth remembering: the baseline's
compact `stream` must expand to exactly the frames `nextIndex` counts (the client
stops there, and a block-end counted twice made it expand to one too many), and
`startTurn` must be inert when the tracker is already on that turn — otherwise the
follow path's own turn bookkeeping discards the attempt the replay just restored.

## Shipped: the session log is the console's log (2026-09-21)

The console offered `Load earlier` after two short questions, and the trajectory
view's chunk list was empty. Measuring the served window answered both: one prompt,
one tool call and one answer arrived as **46 records**, of which 40 were this host's
own durability -- 22 `checkpoint.created`, 16 `model.delta`, plus the model
lifecycle. Upstream's session log contains none of that; its durable vocabulary *is*
the console's, and tokens travel only on the dense channel.

`dshwire` now serves a record only for an event whose type the console knows, and
**numbers records rather than durable events**, so the session sequence stays one
contiguous line with no number spent on something the console would skip. The wire
no longer carries `ignorable` at all: a record this host serves is one the console
must read. The deltas are not lost -- they were never the console's event model --
they are compacted into the settled message's `stream` (`text-chunks` with
`time0`/`dt`/`texts`), which is exactly where upstream keeps them and where the
trajectory view reads them from. The dense frames also now mirror a provider's:
`block-start` opens a block, its deltas carry that block's index, and `block-end`
finalizes it before the next one opens (ADR 0117).

The same turn now serves ten records instead of forty-six. Read
`docs/dsh-console-protocol-recon.md`'s correction before touching the projection or
the sequence: the "log seq on the wire" assumption is exactly what this replaces,
and a console page left open across the change may legitimately report that its
stream resumed behind what it applied (ADR 0108's guard) and reload.

## Shipped: an approval names the conversation, and the answer streams (2026-09-21)

Two operator reports, one per layer, both reproduced in a real browser against a
real model.

**"Waiting for approval" with no panel.** A turn sat at `approval.requested` forever
while the console showed `Running / Tool call / shell · go build ./...`. The host was
delivering the waterfall; the panel dropped it, because the client attaches an
approval to a session it has open through the waterfall's `agentId` and this host
wrote the **turn's** run id into it. A first turn is its own session id, so its
approvals had always rendered; a later turn runs under `<session>~<turn>` and its
requests went nowhere. The waterfall now names the conversation
(`dshsession.Base`), the answer still routes by `clientId`/`eventId`, and the
regression is pinned by a test that fails on the old value with
`agentId = "run_…~2", want the session id "run_…"` (ADR 0115). Verified live: turn 1's
panel appears and "Allow once" runs the command; turn 2's panel appears too and
answering it finishes the turn (`2 turns 3 steps`).

**"Why isn't it streaming?"** The model's tokens were already durable as
`model.delta`, but their console records are marked ignorable and the surface skips
them, and the channel the console renders live prose from — `assistant-stream`
items (start/chunk/end) — was never sent. The follow stream now mints those frames
from the same durable events (ADR 0116). The trap that cost the most time: the
client requires `revision` to increment on **every frame**, not once per attempt,
and a mismatch is a *carrier failure* — the stream is torn down and reopened, so the
first implementation produced a silent ~100 ms restart loop with a fresh generation
per chunk. Measured after the fix, one answer:
`{start: 1, chunk: 16, end: 1}`, revisions `1…18` in order, one committed end naming
the settlement's sequence, and the page's rendered text growing in six visible
steps. Both corrections are recorded in the recon, which is where a future window
should read them before touching these frames.

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
> Stop reaches the turn that is running (ADR 0113), a follow stream follows the conversation
> instead of one turn so "Load earlier" actually loads (ADR 0114), an approval names the
> conversation so a later turn's prompt is answerable (ADR 0115), the answer streams as
> assistant-stream frames (ADR 0116), the served log is the console's own vocabulary and
> numbering rather than the host's durable log (ADR 0117), and a reconnect mid-answer
> resumes the attempt instead of restarting it (ADR 0118), the console's baselines are complete
> (ADR 0119), the file surfaces the console reads are served (ADR 0120), the turn and step
> boundaries are projected (ADR 0121), the built-in provider routes are editable cards
> (ADR 0122), the prompt cards render (ADR 0123), the stream carries the usage and finish
> chunks and answers a cancelled run's open tool calls (ADR 0124), and the goal dock reads
> and mutates the framework's durable goal state (ADR 0125), and the console's
> desktop question is answered `false` while the desktop open is refused by name because
> this host implements no desktop carrier (ADR 0126), and the sidebar's search reads the
> conversations the list already shows, with the reference's own cap, excerpt bound and
> query refusals (ADR 0127), and the attachment read is refused by name because this host
> has no attachment store and no upload path to fill one (ADR 0128), and a fork copies the
> source's completed turns into a child of its own, with the reference's boundary rule and
> refusals and the lineage the sidebar nests it by (ADR 0129). The next chain is "Next up"
> 1 in the ledger, and it is the last one there: `session/updateQueue` -- the pending queue,
> whose projection the control baseline currently publishes empty on purpose. The handover
> section above says what that chain has to build. If a scratch host is
> needed, give it `ZENFORGE_CONFIG_DIR` or `--settings-file` under a throwaway directory so
> it cannot rewrite the operator's document, and its own `--checkpoint-dir` too, because the
> event store and the run registry are derived from it: a scratch host started in `/tmp`
> writes its runs and its registry into the operator's own state directory. Never
> `git add -A`.

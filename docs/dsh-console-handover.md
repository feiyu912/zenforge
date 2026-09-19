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

## Multi-turn: shipped, with one known gap

The seam described below is now implemented (ADR 0086): a session's turns are
named by the deterministic chain in `internal/dshsession`, `session/prompt`
starts the next turn with `InitialMessages` rebuilt from the durable logs,
`session/list` groups the turns under one session, and `session/follow` and
`session/page` resolve a session to its newest turn. The remaining gap is that a
session's turns are **not** merged into one paged log -- see `docs/limitations.md`
-- so an earlier turn's transcript is not reachable through `session/page`.

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

## Operator's one remaining step

The host at `127.0.0.1:8787` has to be **restarted onto this checkout** for ADR 0103 to
take effect: the running binary predates it. Restarting costs nothing now -- the document
at `~/.config/zenforge/console-settings.json` holds the credential, the declared `qwen`
profile, the revision numbers and the welcome-notice acknowledgement -- with one
exception: a model picked in the composer **before** this chain is not in that file,
because the binary that recorded it did not write selections. Pick it once more after the
restart and it stays. The OpenAI card will also come back empty rather than showing the
`--model qwen-plus` flag as a saved setting; that is the fix, not a loss: the resolved
configuration still serves `qwen-plus`, and the card shows only what is written on it.

## Kickoff prompt for the next window

> Read `docs/dsh-console-handover.md` end to end, then
> `docs/dsh-console-coverage.md`. Continue the console-attachment objective: keep the
> three layers separate (deep API -> harness core -> adapters, ADR 0099), land every
> chain with an ADR + docs + tests + commit/push + green CI and docs, and fill the
> ledger's gaps in its order. The settings document shipped (ADR 0102) and so did the
> user-layer/model-selection fix (ADR 0103); the next item is "Next up" 1 in the ledger
> -- re-testing the withheld `ui-directory-picker-browse` plugin, which has to run on a
> scratch port because a plugin that fails activation is a fatal boot page, and the
> operator's host on `127.0.0.1:8787` is the one that must not be taken down by the
> experiment. Since the settings document is now shared state, give a scratch host
> `ZENFORGE_CONFIG_DIR` or `--settings-file` under a throwaway directory so it cannot
> rewrite the operator's document. Never `git add -A`.

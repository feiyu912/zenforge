# DSH Console Attachment: State and Remaining Work

Internal planning artifact; excluded from the published site (see `exclude_docs`
in `mkdocs.yml`). It records where the "attach the upstream console" work stands
so a fresh session can continue without re-deriving anything.

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
3. **The console's identity reads `zenforge`** as of ADR 0092, in the page
   title, the installable-app name, the favicon's accessible name and the brand
   plugins' copy, with the capital form asserted absent from browser-visible
   bytes.
4. **The console's slash commands run** as of ADR 0090
   (`commands/list|execute`: the menu lists the host's catalog, a submitted line
   expands with the host's own rules and starts its run, an unknown line is
   answered without a value). The one honest difference from upstream is that no
   `command/run`/`command/done` flow node is logged.
5. **The console's plugin inventory is answered** as of ADR 0091
   (`pluginInventory/list` from the mount's roster, `managementAvailable: false`,
   every `pluginManager` write refused with its reason). With it, every namespace
   a shipped client panel calls is served or refused by name.
6. **The console's preset selectors are answered** as of ADR 0088
   (`permissionPresets/catalog`, `agentPresets/list|read`, with authoring and
   selection refused by name). These were found by reading the host's own
   "endpoint is not served" log lines rather than by guessing, which is the
   instrument to keep using for the remaining namespaces.
7. **The console's own model/settings panel** (historical note). Its edits go to a host-owned settings document
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

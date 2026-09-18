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
   + the WS mux, serve `webui/dsh` as the console (keeping the ADR 0078 console
   as the fallback), then verify in a browser end to end.

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

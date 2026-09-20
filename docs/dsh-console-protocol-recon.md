# DSH Console Host Protocol: Reconnaissance

Internal planning artifact; excluded from the published site (see
`exclude_docs` in `mkdocs.yml`). It is the evidence base for ADR 0079 and the
host implementation that follows it: every claim below cites a `path:line` in
the upstream sources.

- Upstream: `deepseek-ai/deepseek-harness`, branch `master`, revision
  `ddefc45` ("0.1.6-alpha.2"), MIT.
- Cross-checked against the published artifacts `@deepseek-ai/*@0.1.5-rc.2`.
- Recorded because the route it justifies (implement the host side of the
  protocol rather than fork the interface) depends on details — boot ordering,
  envelope shapes, exact-key stream frames, the fatal-vs-degraded plugin rule —
  that are easy to get wrong from memory and expensive to get wrong in code.

# DSH Web Console — Minimum Host Protocol (evidence-based recon)

Target: let the **zenforge** Go harness (`/Users/kaicheng/coding/zenmind-develop/zenforge`)
serve the upstream DSH web console under its own product name.

**Sources read**

- Public MIT repo `https://github.com/deepseek-ai/deepseek-harness`, branch `master`,
  cloned shallow to `/tmp/dsh-src` at commit **`ddefc45fbc7f8e46dd73185e68295696d1297887`**
  ("release-dsh-0.1.6-alpha.2", 2026-09-17). All `packages/...`, `apps/...`, `scripts/...`
  paths below are relative to that checkout.
- Installed published artifacts (read-only, for confirming what a shipped package
  actually contains): `/Users/kaicheng/.npm/_npx/1e7f6d9597241db0/node_modules/@deepseek-ai/`
  — **241 packages**, frontend shell `dsh-web-frontend@0.1.5-rc.2`, client plugins
  `dsh-*@0.1.5-rc.2`.

No file under `/Users/kaicheng/coding/zenmind-develop/zenforge` was modified.

Every claim below carries a `path:line` citation. Anything I could not establish from the
source is called out in §8 rather than guessed.

---

## 0. Executive summary (the shape of the answer)

1. The console is **two independent layers**:
   - a **statically built React shell** (Vite bundle, package `@deepseek-ai/dsh-web-frontend`,
     source `apps/web`) which only needs to be served as files, plus a
     `window.__DSH_BOOT__` global injected into `index.html`; and
   - **client plugin bundles** (`<pkg>/lib/client.js` from ~53 separate npm packages) that are
     **classic scripts registering lazy CJS factories** into `window.__ModuleLoader__`. They are
     *not* ESM and are *not* bundled into the shell — the browser fetches them at runtime from
     host-controlled URLs.
2. There is **no version handshake**. The only contracts are: the boot-graph JSON shape,
   the HTTP RPC envelope, the WebSocket mux framing, and the session-event envelope. A host that
   satisfies those shapes is indistinguishable from DSH to the console. All URLs are
   **same-origin relative to the served page**; the unary RPC base is hard-wired to
   `location.origin` and cannot be reconfigured at runtime.
3. The **minimum live chat console** needs: static shell + `/plugins/...` bundle route +
   `POST /api/<ns>/<method>` unary RPC + `WS /api/remote.mux` with three logical streams
   (`$events`, `session/control`, `session/follow`) + one forwarded-event answer path
   (`POST /api/$events/result`). Exact shapes in §3.
4. **Reusing the published frontend is viable** for boot + chat, but the *published* artifacts
   bake in the DeepSeek brand: official wordmark registration is unconditionally compiled in,
   `ui-layout` hard-codes the product title string, the shell hard-codes a `HARNESS` wordmark
   and `<title>DeepSeek Harness</title>`, and the sidebar fallback art is the DeepSeek whale SVG.
   Presenting zenforge's own name therefore requires a **fork build** of at least the shell and
   3–4 client packages (details in §6/§7). The published bundles themselves (plugin logic) do
   not need rebuilding for protocol reasons.

---

## 1. Boot path

### 1.1 What the browser requests, in order

| # | Request | Served by | Notes |
|---|---------|-----------|-------|
| 1 | `GET /` (or `/index.html`) | host static layer | `index.html` with **injection rows spliced in** (see 1.2) |
| 2 | `GET /plugins/??<id1>/client.js,<id2>/client.js&rev=<rev>` (`<link rel=preload as=script>`) | host bundle route | application batches; advisory preload (`packages/host/webserver/src/injections.ts:26-27,62-63`) |
| 3 | `GET /plugins/??@deepseek-ai/dsh-client-modules/client.js&rev=<rev>` | host bundle route | **parser-blocking `<script src>`** in `<head>`; must execute before the shell module |
| 4 | `GET ./assets/index-<hash>.js` (`<script type="module">`) + `./assets/vendor-<hash>.js` (`modulepreload`) + 2 CSS | host static layer | the Vite shell; in the published dist `apps/web/package.json` `files` excludes maps/preview |
| 5 | `GET /plugins/??...&rev=...` again (cached) or `<script async src>` appends | host bundle route | module system loads each batch URL once via `loadBundle`; see 1.4 |
| 6 | `GET /api/remote.mux` **WebSocket upgrade** | host WS layer | stream mux; see §2 |
| 7 | `POST /api/session/list`, `WS open $events`, `WS open session/control`, then per-session `session/follow` | host RPC/WS | what the activated plugins ask for; see §3 |

Boot order proof: `apps/web/index.html:11-13` has `<div id="root">` and one module entry;
`packages/host/frontend-static/src/index.ts:120-123` renders injections and prepends
`<base href="/">`; `packages/host/webserver/src/injections.ts:104-117` places head rows
immediately after the opening `<head>` — i.e. **before** the dist's own `<script type="module">`.

The published dist exactly matches this expectation — `dsh-web-frontend/dist/index.html` is:

```html
<title>DeepSeek Harness</title>
<script type="module" crossorigin src="./assets/index-BKQ_L1z6.js"></script>
<link rel="modulepreload" crossorigin href="./assets/vendor-CCJJTK99.js">
<link rel="stylesheet" crossorigin href="./assets/vendor-BNsW4eBh.css">
<link rel="stylesheet" crossorigin href="./assets/index-DPX2bQLO.css">
...
<div id="root"></div>
```

No `__DSH_BOOT__`, no `__ModuleLoader__` — **the host must inject both**; without them the shell
throws (`packages/client/web/src/boot.ts:58-59`, `packages/client/modules/src/client/manifest.ts:215-217`).

### 1.2 The served `index.html`: injection rows

`IndexInjection` is a closed union (`packages/host/webserver/src/injections.ts:15-31`):
`{kind:'global',name,value}` | `{kind:'script',placement,text}` | `{kind:'script-src',placement,src}`
| `{kind:'script-preload',src}` | `{kind:'style',text}` | `{kind:'html',placement,html}`.

`renderIndexInjections()` (`:96-118`) renders head rows after `<head>`, body rows after `<body>`,
and always appends the readiness tail:

```
<script>(globalThis.__DSH_BOOT_READY__ ??= Promise.withResolvers()).resolve()</script>
```
(`packages/host/webserver/src/injections.ts:86`)

The DSH host contributes exactly these rows (`packages/client/modules/src/index.ts:511-546`):

1. an **inline classic queue script** that installs
   `window.__ModuleLoader__ = { mode:"queue", pendingQueue, load(reg){...}, create(options){...} }`.
   Its `create` finds the registration whose `id === "@deepseek-ai/dsh-client-modules"`,
   materializes that factory with a `require` that throws, and delegates to
   `exports.createClientModuleSystem(this, {id, exports}, options)`. If that registration is
   absent it throws `"client-modules: HTML did not preload …/client.js"`.
2. one `script-preload` row per **application** batch.
3. one `script-src` (head, parser-blocking) row per **bootstrap** batch
   (only `["@deepseek-ai/dsh-client-modules"]`, `:494-498`).
4. `{kind:'global', name:'__DSH_BOOT__', value:<graph>}`.

Plus from `dsh-client-connection` (`packages/client/connection/src/index.ts:136-138`):
`{kind:'global', name:'__DSH_CONNECTION_RECOVERY__', value:{backoffBaseMs,…}}` — **optional**;
the client parser defaults every field (`packages/client/connection/src/recovery-config.ts:25-31`).

**Globals the shell reads** (all on `globalThis`, resolved in `packages/client/web/src/boot.ts:55-74`):

| global | required | consumer |
|---|---|---|
| `__ModuleLoader__` | **yes** — missing ⇒ `Error("web boot: window.__ModuleLoader__ bootstrap facade is missing")` | `boot.ts:57-60` |
| `__DSH_BOOT__` | **yes** — missing/malformed ⇒ boot failure | `manifest.ts:214-227` |
| `__DSH_BOOT_READY__` | optional (`?.promise`) | `boot.ts:55` |
| `__DSH_TRANSPORT__` | optional; `{loadBundle?, streamBaseUrl?, ownsHost?, rpc?, fetch?, openStream?}` | `boot.ts:66-72`, `connection/src/client/index.ts:318-325`, `gateway/src/client/stream-client.ts:304-311` |
| `__DSH_CONNECTION_RECOVERY__` | optional | `connection/src/client/index.ts:323` |
| `dshDesktopBoot` | absent in browser; when present takes the Electron path | `apps/web/src/main.ts:4-34` |

### 1.3 The boot graph (`window.__DSH_BOOT__`)

Wire type `WebBootGraph` (`packages/client/modules/src/client/manifest.ts:71-94`):

```ts
{
  rev: string,                       // consistency anchor, opaque
  entries: [ {                       // module-graph order: a requested package precedes its consumers
      id: string,                    // == npm package name, e.g. "@deepseek-ai/dsh-api-gateway"
      url: string,                   // revisioned single-resource combo endpoint
      rev: string,                   // opaque artifact revision (DSH uses `<nonce>-<n>` initially, index.ts:907-909)
      inject?: string[],             // package rows whose factories must arrive first
      immediately?: boolean,         // stage-one prefetch tier
      external?: string[]            // non-seed module specifiers this row requests
  } ],
  batches: [ {                       // initial combo descriptors; EVERY entry in exactly one batch
      phase: 'bootstrap' | 'application',
      url: string, rev: string, entries: string[]
  } ]
}
```

Validation rules enforced client-side (`manifest.ts:214-303`) — a host must satisfy all:
`rev` string; `entries`/`batches` arrays; per-entry string `id`/`url`/`rev`; unique entry ids;
`inject`/`external` string arrays; `phase` ∈ {bootstrap, application}; unique batch URLs;
each batch `entries` non-empty, all naming known entries; **each entry in exactly one batch**
(`:289-292, 296-299`).

Canonical URL shapes (`packages/client/modules/src/index.ts:217-225`):

```
combo:  /plugins/??<id1>/client.js,<id2>/client.js&rev=<rev>
combo map: /plugins/??<id1>/client.js.map,<id2>/client.js.map&rev=<rev>
chunk:  /plugins/<id>/<fileName>?rev=<rev>          // fileName must match /^client\.[A-Za-z0-9][A-Za-z0-9._-]*\.js$/
```

The client requires `rev=` to be present (`packages/client/modules/src/client/system.ts:32-37`
throws `"bundle URL … has no revision"`), and derives sibling chunks only from the exact combo
shape (`system.ts:47-59`) — no published package uses package-local chunks today
(0 `lib/client.*.js` files and no `require.async` in the installed 0.1.5-rc.2 set), so a
single-package URL form is sufficient in practice, but the canonical combo form is the safe
target. The DSH host serves **only advertised URLs**; anything else under `/plugins` is 404
(`index.ts:1091-1112`), with `content-type: text/javascript; charset=utf-8` and
`cache-control: public, max-age=31536000, immutable` (`index.ts:173,1105`).

### 1.4 Are plugins ESM or classic scripts? — classic, lazy CJS factories

Every published plugin bundle begins with:

```js
window.__ModuleLoader__.load({
  id: "@deepseek-ai/dsh-client-modules",
  factory: (require) => { var module={exports:{}}; var exports=module.exports; … return module.exports }
});
```

(verified in `…/@deepseek-ai/dsh-client-modules/lib/client.js`, and identically in
`dsh-client-ui-chat`, `dsh-client-ui-layout`, `dsh-api-session-controller`, …)

Consequences:

- **No ESM/bundler is required on the host.** The host concatenates the `client.js` bytes for a
  batch (DSH joins with `;\n` and appends `//# sourceMappingURL=…`, `index.ts:366-370,282-284`).
- Executing a bundle only **registers** its factory; module side effects (including CSS
  injection) run at materialization, memoized in a load cache
  (`packages/client/modules/src/client/system.ts`, docblock at `manifest.ts:1-30`).
- Default arrival transport is a **classic script element appended to `<head>` with `async=true`**
  (`system.ts:16-29`). `arrive(row)` loads `row.initialUrl` — the **batch** URL, not the row URL
  (`system.ts:165-172`) — so the first materialization pulls the whole combo and registers every
  factory in the batch at once. Preload rows warm that fetch.
- The module table is **seeded** with exactly 9 platform singletons
  (`packages/client/web/src/seed.ts:24-39` / `platform.ts`): `react`, `react/jsx-runtime`,
  `react-dom`, `react-dom/client`, `@deepseek-ai/cordis`, `@deepseek-ai/dsh-client-store`,
  `@deepseek-ai/dsh-client-ui-slots`, `@deepseek-ai/dsh-client-ui-primitives`,
  `@deepseek-ai/dsh-client-ui-dockkit`. A `require()` outside that set must resolve to a graph
  row (`stripClientSuffix` maps `<pkg>/client` → `<pkg>`, `manifest.ts:203-205`); otherwise
  import fails and the boot audit rejects startup.

### 1.5 Boot completion and failure semantics

`AppWebEntry.run` (`packages/client/web/src/boot.ts:47-96`):
await readiness gate → require `__ModuleLoader__` → `moduleLoader.create({boot, staticModules, loadBundle?})`
→ `parseBootManifest` → prefetch `immediately` rows → `bootClient()` → `mountClient()`.

`bootClient` (`packages/client/web/src/boot-client.ts:35-56`) mounts the vendored Cordis
`Loader`, sets `loader.internal = modules`, creates one loader entry per manifest row
(entry name = row id), awaits quiescence, then calls `assertEntriesActive` (`:63-83`) which
**throws** listing every non-active entry, with `pending (waiting for services: …)` for entries
awaiting missing services. On throw the framework-free boot page renders
`Failed to load plugins` plus the entry ids and the error (`packages/client/web/src/boot-page.ts:72-96`),
and `main.ts` reports failure through `dshDesktopBoot.failed` when a desktop carrier exists
(`apps/web/src/main.ts:11-14`).

The app tree is exactly one slot render: `ctx.slots.renderSlot('root', {})`
(`packages/client/ui-renderer/src/client/app.tsx:19-21`); mounting is via a dependency fiber on
`uiRenderer` (`packages/client/web/src/mount.ts:19-24`). If no plugin occupies `root` (i.e. no
`ui-layout`), React mounts an **empty page** — the console "boots" and shows nothing. This is
the core reason a missing plugin degrades instead of crashing (§4).

---

## 2. Wire protocol

### 2.1 Carriers

| Carrier | Path | Framing | Source |
|---|---|---|---|
| unary RPC | `POST /api/<namespace>/<method>` | JSON body, one envelope per request | `connection/src/api-path.ts:7`, `rpc-host.ts:236-284` |
| stream multiplex | `WS /api/remote.mux` | one JSON object per WS text message | `api/gateway/src/stream-protocol.ts:6`, `stream-server.ts:48-58` |
| plugin bundles | `GET /plugins/...` | concatenated classic-script bytes | `client/modules/src/index.ts:217-225,1091-1112` |
| HMR (optional) | `GET /plugins/events` | SSE `message` frames | `client/hmr/src/events.ts:44`, `client/hmr/src/client/index.ts:29` |
| index / static | `GET /` | HTML | §1 |

No long-poll, no fetch-streaming RPC, no SSE for chat. Live assistant text, tool calls/results
and approvals all ride the **WebSocket mux** as logical streams.

### 2.2 Unary RPC envelope

Browser → host (`packages/client/connection/src/client/rpc.ts:34-51`):

```jsonc
POST /api/session/list
content-type: application/json
{ "type": "client-request", "rpcId": "<uuid>", "method": "session/list", "payload": { "args": { … } } }
```

Host → browser (`packages/client/connection/src/rpc.ts:69-73`, `rpc-host.ts:277-284`):

```jsonc
{ "type": "server-response", "rpcId": "<same uuid>", "result": { "ok": true,  "value": { … } } }
{ "type": "server-response", "rpcId": "<same uuid>", "result": { "ok": false, "error": { "code": "…", "message": "…", "details": {} } } }
```

Client-side validation (`client/rpc.ts:73-102`) — all mandatory:

- `response.ok` must be true (an HTTP 4xx/5xx makes `call()` throw
  `transport failure for /api/<ep>: HTTP <status>`, `:52-54`);
- body must be an object with `type === 'server-response'` and string `rpcId`
  (**`rpcId` must be echoed exactly** or the client throws `rpcId mismatch`, `:56-58`);
- `result` must be `{ok:true,value}` or `{ok:false,error:{code:string,message:string,details:object}}`.

`payload` must contain **exactly one** plain-object field `args`
(`packages/api/gateway/src/index.ts:936-951`); `args` are the named method parameters.
Endpoint names are `<namespace>/<method>` with segments matching `[A-Za-z0-9_$.-]+`
(`client/rpc.ts:113-119`; `$` in segments is what allows `$events/result`).

**Unknown endpoint behaviour (important for partial hosts):** the shared `/api` handler returns
HTTP 404 `"not found"` when no interceptor claims the endpoint
(`packages/client/connection/src/rpc-host.ts:236-262`); anything else under `/api` is a 404 at the
Go-host level. On the client that surfaces as a thrown `transport failure … HTTP 404`, which most
callers convert into a rejected/`gateway/internal` result — degradation, not a fatal boot error.

### 2.3 WebSocket mux framing

Endpoint constant: `REMOTE_STREAM_MUX_PATH = '/api/remote.mux'`
(`packages/api/gateway/src/stream-protocol.ts:6`).

Browser → host (`:242-284`), exact keys only (extra fields are rejected):

```jsonc
{ "type": "open",   "streamId": "<uuid>", "endpoint": "<ns>/<method>" | "$events", "payload": { "args": { … } } }
{ "type": "cancel", "streamId": "<uuid>" }
```

Host → browser (`:259-313`), exact keys only:

```jsonc
{ "type": "item",  "streamId": "<uuid>", "value": <stream item> }   // "value" may be omitted
{ "type": "error", "streamId": "<uuid>", "error": { "code": "…", "message": "…", "details": {} } }
{ "type": "end",   "streamId": "<uuid>" }
```

Heartbeat: server-initiated **WebSocket Ping** every `websocketHeartbeatIntervalMs`
(default **2000 ms**, `api/gateway/src/index.ts:116,171-174`); a client missing 2 consecutive pings
is `terminate()`d (`stream-server.ts:22,75-94`). Browsers answer pings automatically; a Go host
using `gorilla/websocket`/`nhooyr` must implement `pong` handling (or just not enforce it).
Closing code 4000 is used for a client-requested reconnect (`client/stream-client.ts:65`).

### 2.4 The forwarded-event stream (`$events`) — approvals and list mutations

Constants (`packages/api/gateway/src/stream-protocol.ts:9-18`):
`REMOTE_EVENT_STREAM_ENDPOINT = '$events'`, `REMOTE_EVENT_RESULT_ENDPOINT = '$events/result'`,
`REMOTE_EVENT_STREAM_PAYLOAD = { args: {} }`, `REMOTE_EVENT_STREAM_READY = { type: 'ready' }`.

Opening: `{"type":"open","streamId":S,"endpoint":"$events","payload":{"args":{}}}` — `args` must be
an **empty** object or the host returns a `gateway/arguments-invalid` error
(`api/gateway/src/index.ts:391-403`). The host's **first** item must be
(`index.ts:413-427`, `stream-protocol.ts:32-38`):

```jsonc
{ "type": "item", "streamId": S,
  "value": { "type": "ready", "clientId": "<uuid>", "host": { "home": "/home/user" } } }
```

Then downlink frames (`stream-protocol.ts:44-70`):

```jsonc
{ "type": "item", "streamId": S, "value": { "type": "emit",  "event": "api-session/added", "args": [ … ] } }
{ "type": "item", "streamId": S, "value": { "type": "waterfall", "event": "approval/request",
    "eventId": "<uuid>", "agentId": "<session-id>", "request": { … } } }
{ "type": "item", "streamId": S, "value": { "type": "cancel", "eventId": "<uuid>" } }
```

Client answer to a **waterfall** (approval / user-questions) goes back as a normal unary RPC
(`packages/api/gateway/src/index.ts:357-369`, `client/remote-events.ts`):

```jsonc
POST /api/$events/result
{ "type":"client-request", "rpcId":"<uuid>", "method":"$events/result",
  "payload": { "args": { "clientId": "<from ready>", "eventId": "<from waterfall>",
                         "outcome": { "kind": "result", "value": "allowed-once" } } } }
```

`outcome.kind` ∈ `"next"` | `"result"` | `"rejected"` (`stream-protocol.ts:101-137`); exact keys.

Forwarded event allow-list (host side, `packages/api/remotes/src/remote-events.ts:18-42`) includes
`approval/request` and `user-questions/request` as **waterfall**, and as **emit**:
`api-session/added|removed|status|activity|error`, `agent-preset/selected`, `commands/change`,
`credentials/reference-updated`, `goal/activation-changed`, `llm/adapters-updated`,
`permission-presets/catalog-changed`, `plugin-manager/changed|install-log|install-state`,
`settings/document-updated`, plus the `cordis/*` inspection events. A minimal host can emit only
the `api-session/*` family plus `approval/request`.

### 2.5 Reconnect / resume

Browser side (`packages/client/connection/src/client/connection.ts`, `recovery-config.ts:25-31`):
each logical generation must report *ready* within `generationReadyTimeoutMs` (default **15000 ms**;
a warning at 3000 ms); on loss it retries with exponential backoff base 500 ms, factor 2, cap 10000 ms,
forever, and emits `connection/reset` so wire-derived caches repull
(`connection/src/client/index.ts:16-25`). There is **no resume token / replay**: a reconnect opens
fresh streams and the host must re-send each stream's opening frame (`$events` ready frame,
`session/control` baseline, `session/follow` snapshot). Streams have no sequence numbers at the
mux layer — ordering/dedup lives inside `session/follow` (`seq` + `surfaceOp`, §3).

### 2.6 Browser trust + authentication (host-side policy, not client protocol)

The DSH host fences every `/api` request and the WS upgrade
(`packages/client/connection/src/api-request-trust.ts:91-118`): `Host` must be loopback, a derived
LAN literal, or a configured `trustedHosts` authority; `Sec-Fetch-Site: cross-site` is refused;
a present `Origin` must equal the `Host` authority. Then authentication:
`requestRejection` returns `403` for a trust failure and `401` for a missing/!invalid browser
session (`rpc-host.ts:96-100`), and index responses go through `authorizeIndex`
(`frontend-static/src/index.ts:88-91`, `browser-auth.ts`).

The browser cookie protocol (`packages/client/connection/src/browser-auth.ts`): launch token in the
query string `?token=<base64url>` (constant `TOKEN_QUERY`), cookie name `dsh-auth-<canonical
authority>`, payload `v1.<base64url(body)>.<hmac-sha256>`, default lifetime 30 days
(`connection/src/index.ts:99-110`). 401 body: `dsh web authentication required; reopen the URL
printed by dsh web.` (`browser-auth.ts:304-312`).

**Crucially: none of this is client-side.** The console sends no credentials and never inspects the
response for auth; the shell just won't be served and `/api` won't answer if the host rejects.
A zenforge host may implement a simpler (or no) auth policy without breaking the protocol — but
`/api` and the WS are the process's whole attack surface, so loopback-only binding plus the
same Host/Origin fence is the minimum sane substitute.

---

## 3. Minimum capability set

All methods below are Remote methods of the host `session` namespace
(`packages/api/session-controller/src/index.ts:87-99,121,400-413`) unless stated otherwise.
`args` = the `payload.args` object of a `POST /api/<endpoint>` request or of a WS `open` frame.

### (a) List and create sessions

**`POST /api/session/list`** — args `{ "cursor"?: string }` (`types.ts:245-252`)
result value:
```jsonc
{ "items": [ { "sessionId": "…", "updatedAt": 1712345678901, "running": false, "blank": true,
               "title"?: "…", "parentSessionId"?: "…", "origin"?: "subagent" } ] }
```
(`SessionSummary`, `types.ts:163-175`; remaining optional fields per the same interface.)
Client caller: `packages/api/session-controller/src/client/sessions/manager.ts:409`.

**`POST /api/session/create`** — args `{ "workspaceId"?: string, "cwd"?: string, "sessionId"?: string,
"agentPreset"?: string }` (`types.ts:266-271`)
result `{ "sessionId": "…", "agentPreset"?: "…" }` (`types.ts:274-277`). Caller:
`manager.ts:498`. `sessionId` present means "adopt this explicit id"
(`SessionCreateRequest` doc).

Pushing new/removed sessions live is optional if the console is asked to re-list; the
`api-session/added|removed|status|activity` forwarded events are how DSH avoids that.

### (b) Accept a prompt

**`POST /api/session/prompt`** — args (`SessionPromptRequest`, `types.ts:313-321`):

```jsonc
{ "requestId": "<client-minted uuid>",      // SessionRequestId
  "sessionId": "…",
  "mode": "queue" | "steer",
  "content": [ { "type": "text", "text": "…" }
             | { "type": "image", "mediaType": "image/png"|"image/jpeg"|"image/webp"|"image/gif",
                 "data": "<base64>", "name"?: "…" }
             | { "type": "file", "receiptId": "…" } ],
  "clientTimeZone"?: "Europe/Berlin" }
```
result `{ "accepted": true }` (`types.ts:324-326`). Caller:
`packages/api/session-controller/src/client/sessions/session.ts:249`.

### (c) Stream assistant text, tool calls, tool results live

**WS `open` `endpoint: "session/follow"`** — payload args (`SessionFollowRequest`, `types.ts:450-455`):

```jsonc
{ "address": { "sessionId": "…" },  // SessionAddress; types.ts:386
  "maxMessages"?: 50,
  "assistantStream": true }         // required for live token deltas
```

Stream items (`SessionFollowFrame`, `types.ts:516-527`):

```jsonc
// 1. exactly one opening snapshot
{ "type": "snapshot", "header": { "version": 3, "id": "…", "createdAt": 1712345678901,
    "cwd"?: "…", "parentSession"?: "…", "isSeeded": false, "origin"?: "subagent",
    "delegationDepth"?: 0, "agentPreset"?: "…" },
  "cursor": 42, "records": [ { "type":"event", "event": <SessionWireEvent> } ],
  "hasMore": false,
  "projections": { "asOfSeq": 42, "values": {} },
  "assistantStream"?: { "revision": 1, "activeAttempt"?: { … } } }

// 2. durable events, in seq order
{ "type": "event", "event": { "type": "assistant/message", "seq": 43, "time": 1712345678999,
                              "data": { … }, "surfaceOp": "append" } }   // surfaceOp only on the four
                                             // surface-eligible types (ADR 0105)

// 3. cursorless process-local token stream (when assistantStream: true)
{ "type": "assistant-stream", "frame": { "type":"chunk", "attemptId":"…", "revision":1,
    "index":0, "time":1712345678900,
    "chunk": { "type":"text-delta", "index":0, "text":"Hel" } } }
```

- `SessionWireEvent` envelope (`types.ts:428-438`): `{type:string, seq:number, time:number,
  data:JsonValue, ignorable?:true, sourceEventSeqs?:JsonValue, surfaceOp?:JsonValue}`.
  The client rejects **unexpected fields** and requires `seq`/`time` safe integers
  (`client/session-wire-event.ts:14-46`). Event names are merge-extensible; only
  `request/header` and `tool/result` payloads have local validation rules
  (`packages/core/session/src/surface.ts:172-195`).
- Event names the chat UI consumes (`packages/core/session/src/types.ts:269+`):
  `turn/start`, `turn/end`, `step/start`, `step/end`, `user/message`, `system/message`,
  `assistant/message`, `assistant/attempt`, `tool/call`, `tool/result`, `request/header`,
  `request/context`, `session/end-seed`. A host may also emit custom names (rendered opaquely).
- Live text deltas use `StreamChunk` (`packages/llm/llm/src/types.ts:426-437`):
  `block-start`, `text-delta`, `reasoning-delta`, `tool-call-delta`, `block-end`, `usage`, `finish`.
- Tool calls/results are ordinary durable events (`tool/call` with `callId,name,arguments` string;
  `tool/result` with `message`/optional `error`) — no separate channel. The `follow` opening
  snapshot is what makes reconnect resume work: the host must send the full window + cursor.

**`POST /api/session/page`** — backwards history paging, args
`{address, throughSeq, beforeSeq?, maxMessages?}` → `{records, hasMore}` (`types.ts:441-447,510-513`).
Caller `client/transport.ts:228`. Needed only for scrolling past the opening window.

**Corrected 2026-09-20 (ADR 0108):** the snapshot's `cursor` is not per run but per
**session**: `client/journal-stream.ts` keeps one last-applied cursor for the whole
conversation (`follows(left, right) => right === left + 1`), requires a snapshot window's
last record to end exactly at the cursor it cites (`assertPageThrough`), and throws
`<name> resumed at a cursor behind the last applied entry` when a resumed generation cites
a lower one (`opening(item, resumed)`). A host whose runs each number their log from one —
which is what this harness does for a session's turns — must therefore serve a session's
turns in one sequence, or the second prompt fails with
`Failed to load history: session event stream resumed at a cursor behind the last applied
entry (gateway/internal)`, which is exactly what this host did before ADR 0108.

**`POST /api/session/cancel`** — args `{ "sessionId": "…" }` → `{ "accepted": true }`
(`types.ts:353-360`). Caller `sessions/session.ts:340`.

**`POST /api/session/rename`** — args `{sessionId, title}` → `{title, seq}` (`types.ts:290-299`);
needed for the sidebar title to change after the first prompt.

**`POST /api/session/modelCatalog`** — no args → `ModelCatalog`
(`types.ts:144-150`); called by the model selector at activation
(`packages/client/ui-model-selection/src/client/catalog.ts:45`). Without it the model picker shows
its failure state; chat still works.

### (d) Approval request and approve/deny

There is **no `approval` RPC namespace**. It is the forwarded waterfall
`approval/request` (`packages/interaction/user-approval/src/types.ts:76-90`):

- Host → browser, on the `$events` stream:
  ```jsonc
  { "type":"item", "streamId": S,
    "value": { "type":"waterfall", "event":"approval/request", "eventId":"<uuid>",
               "agentId":"<session id>", "request": { "agent": <projected agent identity>,
                 "toolName":"bash", "callId"?: "<ToolCallId>", "reason"?: "…" } } }
  ```
  (`projectRemoteEventRequest` strips the live `agent`/`signal` and requires the request to carry
  the scoped Agent, `stream-protocol.ts:139-172`; the wire `request` is JSON-only.)
- Browser → host: `POST /api/$events/result` with `outcome.kind = "result"` and
  **`value ∈ {"allowed-once","rejected"}** (`ApprovalDecision`,
  `packages/client/ui-approval/src/client/contract/slots.ts:64`; the panel's two buttons are exactly
  these, `ApprovalPanel.tsx:26,45,48`). The full outcome vocabulary is
  `allowed-once | rejected | cancelled | unavailable` (`user-approval/src/types.ts:32`); a host that
  receives `next`/`rejected` outcome kinds must fail closed.
- The client only answers when the request is scoped to a session it knows
  (`ui-approval/src/client/index.ts:42-43`); `next()` delegates onward.

The zenforge side already has `approval.PendingBroker` / `EventApprovalRequested` /
`EventApprovalResolved` (`events.go:57-59`, `server/harnesshttp/handler.go:503,565`), so this maps
directly.

### (e) Cancel a run

`POST /api/session/cancel` (§c). Additionally the **WS `cancel` frame** cancels one *logical stream*
only. Cancelling the run is the RPC, not the stream cancel.

### (f) The host-wide control stream (needed for a populated sidebar)

**WS `open` `endpoint: "session/control"`** (no args; `types.ts:411`, `session-controller/src/index.ts:410-413`).
First item is the baseline (`SessionControlFrame`, `types.ts:541-558`):

```jsonc
{ "type":"baseline", "value": { "jobs": { "<sessionId>": [ { "id":"…","kind":"…","label":"…",
      "status":"running|stopping|completed|killed|failed","startedAt":1,"finishedAt"?:2,"detail"?:"" } ] },
    "projections": { "<sessionId>": { "asOfSeq": 42, "values": {} } } } }
```
then `{type:"jobs",sessionId,jobs}` / `{type:"projection",sessionId,key,value,seq}`.
`{jobs:{},projections:{}}` is a legal, minimal baseline.

### (g) Session event subscription needed at boot

The session list is seeded by `session/list`; the sidebar's live updates come from the forwarded
`api-session/*` emit frames. Nothing else is strictly required to boot.

### Summary table (minimum viable host)

| # | Endpoint | Kind | Needed for |
|---|---|---|---|
| 1 | `GET /` + assets | static | shell |
| 2 | `GET /plugins/...` | static | plugin bundles |
| 3 | `POST /api/session/list` | unary | sidebar |
| 4 | `POST /api/session/create` | unary | new session |
| 5 | `POST /api/session/prompt` | unary | send a prompt |
| 6 | `WS session/follow` | stream | transcript + live text/tools |
| 7 | `WS session/control` | stream | session/job badges |
| 8 | `WS $events` | stream | approvals + list mutations |
| 9 | `POST /api/$events/result` | unary | approve/deny answers |
| 10 | `POST /api/session/cancel` | unary | stop a run |
| 11 | `POST /api/session/rename` | unary | sidebar title |
| 12 | `POST /api/session/page` | unary | scrollback |

---

## 4. Host vs plugin split

### 4.1 Who owns what

| Concern | Owner | Evidence |
|---|---|---|
| Static shell files, `index.html` rendering, injection table | **host** (`dsh-host-frontend-static`, `dsh-host-webserver`) | `host/frontend-static/src/index.ts:113-142`, `host/webserver/src/index.ts:347-361` |
| Boot-graph composition from package manifests, bundle route, `__ModuleLoader__` queue script | **host** (`dsh-client-modules` node half) | `client/modules/src/index.ts:511-546,600-620` |
| `/api` route, auth, trust fence, Host↔fetch bridge | **host** (`dsh-client-connection` host half) | `client/connection/src/index.ts:134-154` |
| Remote dispatch, WS mux, forwarded events | **host** (`dsh-api-gateway` host half) | `api/gateway/src/index.ts:198-232` |
| All business methods (`session`, `workspace`, `settings`, `terminal`, `skills`, `fileReferences`, `directoryPicker`) | **host** services | namespaces at `api/*/src/index.ts` (grep `namespace: '`) |
| Module system, Cordis loader, boot page, React mount | **client** plugin `dsh-client-modules` + shell | `client/web/src/boot.ts`, `boot-client.ts` |
| Transport client, connection lifecycle | **client** `dsh-client-connection` | `client/connection/src/client/index.ts:317-326` |
| Remote client projection, streams, `/api` caller | **client** `dsh-api-gateway`, `dsh-api-remotes` | `api/gateway/src/client/*` |
| Session client model, list, transcripts | **client** `dsh-api-session-controller` + `dsh-client-ui-session` | `api/session-controller/src/client/*` |
| Layout, sidebar, chat composer, tool rendering, approval panel, theme, locale | **client** `dsh-client-ui-*` | packages |

### 4.2 Can it boot with a handful of plugins?

**Yes — but the handful is ~23 packages, and which ones is dictated by service injection, not by
which panel you want.** Every client plugin declares an exported `inject` of *Cordis service
names*; an entry whose service provider is absent stays `pending`, and `assertEntriesActive`
then **fails the whole boot** (`client/web/src/boot-client.ts:63-83`). Conversely, a plugin simply
**absent from the graph** is never an error: its slot occupant is missing, so the region renders
`undefined`/its fallback and the panel is hidden (`ui-slots` `specOf` → "outlets render empty",
`client/ui-slots/src/renderer.ts:178`).

Minimum chat-capable roster, derived from the published `dsh.client.inject` edges + the bundles'
exported `inject` arrays (closure computed over all 55 published client packages):

```
dsh-client-modules            (bootstrap; provides `modules`)
dsh-typert-registry           (provides `typert`)
dsh-client-connection         (provides `connection`)
dsh-api-gateway               (provides `remote`)
dsh-api-remotes               (provides `remote.session`, `remote.subagents`, `remote.workspace`,
                               `remote.directoryPicker`, `remote.settings`)
dsh-client-locale             (locale)
dsh-client-ui-renderer        (slots, uiRenderer)
dsh-client-ui-theme           (theme)
dsh-client-ui-settings        (settingsScope, settingsSchema)
dsh-api-session-controller    (sessions + client session model)
dsh-api-workspace-controller  (workspaces)
dsh-client-resources          (resources)
dsh-client-file-upload        (fileUpload)
dsh-client-ui-session         (uiSession)
dsh-client-ui-layout          (layout  → occupies slot `root`)
dsh-client-ui-sidebar         (occupies slot `sidebar`)
dsh-client-ui-sidebar-right   (sidebarRight; injected by ui-chat)
dsh-client-ui-conversation    (conversation/uiConversation; occupies main key `conversation`)
dsh-client-ui-workspace       (uiWorkspace; injected by ui-conversation/ui-chat/ui-sidebar)
dsh-client-ui-input-trigger   (inputTriggers; injected by ui-chat)
dsh-client-ui-chat            (chat composer)
dsh-client-ui-tool            (tool-call rendering)
dsh-client-ui-approval        (approval panel)
```

Adding panels is additive: `ui-settings*`, `ui-jobs`, `ui-plan`, `ui-skill`, `ui-goal`,
`ui-subagent`, `ui-trajectory`, `ui-commands`, `ui-permission-presets`, `ui-model-selection`,
`ui-sidebar-files`, `ui-sidebar-terminal`, `ui-user-questions`, … each independently pulls its own
service closure. The **full DSH web roster is 53 client packages** (counted from the `dsh.client`
rows referenced by `packages/bundle/base/cordis.patch.yml` and
`packages/bundle/web-app/cordis.patch.yml`, cross-checked against the installed packages; the patch
inserts the web roster at `packages/bundle/web-app/cordis.patch.yml:44+`, including
`ui-brand-official` at `:278-280`).

Failure modes, precisely:

| situation | result |
|---|---|
| plugin in graph, bundle 404 or `require` unresolvable | entry import fails → boot audit throws → boot page: `Failed to load plugins` + id |
| plugin in graph, its injected service has no provider | entry `pending` → boot audit throws with `pending (waiting for services: …)` |
| plugin absent from graph | no error; its slot stays empty (or shows the declaring component's fallback) |
| plugin absent, but another plugin injects one of its services | that other plugin is `pending` → **boot fails** |
| host method missing (404) | `transport failure … HTTP 404` thrown in that feature only |
| stream endpoint missing | mux `error` frame → `RemoteError` in that feature only |
| `$events` ready frame never arrives (< 15 s) | generation fails, retries forever; UI shows disconnected; boot itself succeeded |

---

## 5. Compatibility

### 5.1 Version / handshake checks

**There are none between host and client.** No `protocolVersion`, no version endpoint, no
`serverInfo`. Exhaustive list of things that can be "incompatible":

1. **Boot-graph shape** — strictly validated (`manifest.ts:214-303`); failure is fatal and loud.
2. **HTTP envelope** — `rpcId` echo, `server-response`/`result.ok` shape
   (`client/rpc.ts:73-102`).
3. **WS frames** — exact-key validation (`stream-protocol.ts:270-313`); extra fields are an error.
4. **Session event envelope** — unknown fields rejected, `seq`/`time` safe integers, plus
   per-event-name payload rules (`client/session-wire-event.ts:14-46`).
5. **Module/plugin bundle expectations** — a bundle built against a newer `dsh-client-store` /
   `ui-slots` / `ui-primitives` than the shell exports breaks at `require`;
   `packages/boot/app-boot/tests/loader-shape.compat.spec.ts` exists for the loader shape but there
   is no runtime probe. In practice: **shell and plugin packages must come from the same release**.
6. `packages/boot/plugin-manager/src/index.ts:52` carries a name list (`dsh-host-frontend-static`,
   `dsh-tools`, …) used by the in-app plugin manager — a host that omits those packages just loses
   the manager page.

### 5.2 What a partial implementation breaks

- Missing `/plugins` route but valid graph → every plugin import fails → **total boot failure**.
- Missing `__DSH_BOOT__` / `__ModuleLoader__` → **total boot failure** (shell throw).
- Missing `/api` entirely → plugins activate, then every RPC fails; UI renders but cannot load.
- Missing `$events` ready frame → console shows disconnected and retries; approval/list-mutation
  features dead.
- Missing `session/control` → sidebar list still works from `session/list`; job badges do not.
- Missing `session/follow` → transcript area empty/error per session.
- Wrong `rpcId` echo → every call throws `rpcId mismatch`.
- Malformed `session/follow` snapshot/events → the *stream* throws; only that session's transcript
  breaks (the error surfaces through the stream's `failed` sink).

### 5.3 Can the console point at a non-DSH host?

Partly, and this matters for deployment:

- **Unary RPC base is hard-wired to the page origin**:
  `resolveBase()` returns `location.origin` (`client/connection/src/client/rpc.ts:108-111`).
  No config, no global, no query parameter. The console must be served from the same origin as
  `/api`.
- **The WS base is overridable** by `globalThis.__DSH_TRANSPORT__.streamBaseUrl`
  (`api/gateway/src/client/stream-client.ts:304-311`) — and since the host writes the served
  `index.html`, it *can* set that global in an injection row and proxy the socket elsewhere.
  Likewise `__DSH_TRANSPORT__.loadBundle`/`rpc`/`fetch`/`openStream` exist but are functions and
  therefore not expressible in served HTML; only the Electron/worker boot path (`main.ts:23-32`)
  uses them.
- **Shell assets and plugin bundles are same-origin relative** (`./assets/...` and `/plugins/...`),
  so cross-origin hosting would additionally need CORS or a proxy.
- Therefore: **a third-party host must implement the DSH host protocol at the same origin.** It
  cannot point the stock console at its own pre-existing API shape, and it cannot use the console
  as a thin client for a remote zenforge server without an origin-level proxy.

---

## 6. Branding — every location in the boot path

### 6.1 Baked into the built artifacts (runtime-unchangeable)

| # | Location | What | Evidence |
|---|---|---|---|
| 1 | shell `index.html` `<title>` | `DeepSeek Harness` (published) / `DSH Local Build` (bare `vite build`) | installed `dsh-web-frontend/dist/index.html`; `apps/web/vite.config.ts:14,22-30` (`DSH_CLIENT_TITLE`, default `'DSH Local Build'`) |
| 2 | shell `manifest.webmanifest` | `"name": "DeepSeek Harness"`, `"short_name": "DSH"`, `"id": "/"`, `"start_url": "/"` | `apps/web/public/manifest.webmanifest` |
| 3 | shell `favicon.svg` (+ `<link rel=icon>`) | DeepSeek whale SVG | `apps/web/public/favicon.svg`; injected by the dist `index.html` |
| 4 | shell boot page wordmark | literal `HARNESS` div, plus `Loading plugins…` / `Failed to load plugins` | `packages/client/web/src/boot-page.ts:37,40,92`; verified present in the published `dist/assets/index-*.js` |
| 5 | `ui-layout` browser title | `productTitle = process.env.DSH_CLIENT_TITLE ?? t('brand.localBuild')`, applied by `DocumentTitle` | `client/ui-layout/src/client/AppFrame.tsx:195`, `client/ui-layout/src/client/DocumentTitle.tsx:24-27`; published bundle contains the literal `const productTitle = "DeepSeek Harness"` |
| 6 | sidebar fallback mark | `renderSlot('sidebar.brand.mark', …, { fallback: <FishLogo size={24}/> })` — the whale | `client/ui-sidebar/src/client/SidebarRoot.tsx:181,220`; `client/ui-primitives/src/FishLogo.tsx` |
| 7 | sidebar fallback name | `t('brand.localBuild')` → `DSH Local Build` (en) / `DSH 本地构建` (zh) | `client/ui-sidebar/src/client/SidebarRoot.tsx:223-230`; `client/locale/src/locales/en.ts:33`, `zh.ts:31` |
| 8 | official brand plugin | `OfficialBrandMark` = `FishLogo`, `OfficialBrandName` = `BrandWordmark` | `client/ui-brand-official/src/client/Brand.tsx:9-19`; `ui-primitives/src/BrandWordmark.tsx` |
| 9 | conversation empty-state hero | animated `HeroFish` fallback in `conversation.hero.brand.mark` | `client/ui-conversation/src/client/skeleton/EmptyHero.tsx:100,149` |
| 10 | local-build version badge | `DSH_CLIENT_VERSION` + `DSH_CLIENT_COMMIT_HASH` shown under the sidebar brand | `client/ui-sidebar/src/client/SidebarRoot.tsx:43-48` |
| 11 | settings/about and fixture copy | e.g. `"DeepSeek Harness — plugin-based agent harness"`, fixture prompts | `client/connection/lib/client.js` (source: `packages/client/connection/src/...`) |

### 6.2 The official-brand gate is compiled out in published packages

Source (`client/ui-brand-official/src/client/index.ts:16-22`) has
`if (process.env.DSH_CLIENT_BUILD_PROFILE !== 'official') return`. In the **published**
`dsh-client-ui-brand-official/lib/client.js` that guard is **gone** — `apply()` unconditionally
registers both slots (verified by reading the published file). So the npm artifacts are the
*official* profile, and a third-party host that includes `ui-brand-official` in its roster ships the
DeepSeek wordmark.

The build orchestration behind this (`scripts/client-build-environment.ts`):
`OFFICIAL_CLIENT_BUILD_ENVIRONMENT = { DSH_CLIENT_BUILD_PROFILE: 'official', DSH_CLIENT_TITLE:
'DeepSeek Harness' }` (`:18-23`); `pnpm run build:official` = `tsx scripts/build.ts --profile official`
(root `package.json`); a default `pnpm run build` inlines whatever `DSH_CLIENT_*` values the
environment carries and, with no profile selector, leaves `DSH_CLIENT_BUILD_PROFILE` as
`undefined` → the brand plugin returns early.

### 6.3 What a host can and cannot change

- **Can**, without rebuilding anything: `index.html` bytes (title tag, manifest link, favicon link,
  `<style>`/`<meta>` injections), the served `favicon.svg` / `manifest.webmanifest` files, the
  `/plugins` roster (omit `ui-brand-official`), and it can *register its own occupants* into
  `sidebar.brand.mark` / `sidebar.brand.name` by serving one extra client bundle of its own
  (a ~10-line classic script that calls `window.__ModuleLoader__.load({id, factory})` and from the
  factory does `ctx.slots.inject('sidebar.brand.mark', …)`).
- **Cannot**, at runtime: `HARNESS` on the boot page (#4), `productTitle` (#5), the whale fallbacks
  (#6, #8, #9), or the `DSH Local Build` locale strings (#7).
- So a *fully* zenforge-branded console needs a fork build with
  `DSH_CLIENT_TITLE=ZenForge DSH_CLIENT_BUILD_PROFILE=local pnpm run build` (or `build:official`
  minus the brand plugin), plus token edits to `boot-page.ts` (`'HARNESS'`), `FishLogo`/
  `BrandWordmark` (or replacing the two fallbacks), and `EmptyHero`'s `HeroFish`.

---

## 7. Plan for zenforge (`/Users/kaicheng/coding/zenmind-develop/zenforge`)

Current state (read-only inspection): `cli/serve.go` already builds an `http.ServeMux` with
`/runs/*`, `/approvals`, `/approval`, `/api/settings`, `/api/server`, and mounts
`webui.Handler()` at `/assets/` and `/` (`cli/serve.go:216-244`); `webui/webui.go` serves three
embedded hand-written assets (index.html/app.js/style.css). `server/harnesshttp` exposes
`NewRuntime`, `RunManager` (Start/Resume/Steer/Get/List/Attach/Cancel/Forget) and
`Handler.ServeDetached*` + approvals (`server/harnesshttp/runtime.go:39-87`,
`handler.go:168-392,503-565`). Event vocabulary is `run.started`/`step.*`/`model.delta`/
`tool.call`/`tool.result`/`approval.requested|resolved` (`events.go:15-80`).

The DSH console does not consume any of that shape; the plan is therefore to add a
**DSH-protocol adapter layer** beside the existing API, leaving `/runs/*` untouched.

### Step 0 — decide the frontend strategy (blocking, ~0.5 day, small)

Two options; the report's recommendation is (B).

**A. Serve the published artifacts as-is.** Viable for boot + chat. Reuse
`@deepseek-ai/dsh-web-frontend/dist` (shell, MIT, 4 files) + the `lib/client.js` of the minimum
roster (§4.2) from `node_modules`/a vendored copy. Zero JS build. Cost: DeepSeek branding
(title, whale, `HARNESS`, product title) — §6.

**B. Fork-build for zenforge branding.** Vendor the MIT monorepo (or the needed packages) at a
pinned release, run `DSH_CLIENT_TITLE=ZenForge DSH_CLIENT_BUILD_PROFILE=local pnpm run build`
(default profile, so no official brand registration), then apply the small brand edits in §6.3,
and vendor the resulting `apps/web/dist` + `packages/*/*/lib/client.js` into the Go binary.
This is the only path to a console that says "ZenForge" everywhere. Effort: medium once the
toolchain works (pnpm + Node 22 + the repo's own build scripts), large if the build must be
made reproducible in CI.

**Viability answer, explicitly:** serving the *published* `@deepseek-ai/dsh-web-frontend` dist is
**technically viable** for a third-party host — the dist is a plain static SPA and the host supplies
all runtime globals. It is **not sufficient for rebranding**, because the product strings and brand
art are compiled into the shell and into `ui-layout`/`ui-sidebar`/`ui-conversation`/
`ui-brand-official`. Rebuilding from the monorepo is not required for *protocol* reasons, only for
*branding*.

### Step 1 — static shell + boot graph (small–medium)

New files (suggested):

- `webui/dsh/` — embedded assets: `index.html` template, `assets/*` from the chosen dist, plus a
  vendored `plugins/<id>/client.js` tree (or read from a configurable directory in dev).
- `internal/dshboot/graph.go` — build `WebBootGraph` in Go: a table of
  `{id, rev, inject, external, immediately, bytes}`; compute `rev` as a content hash (any opaque
  string works — DSH uses a nonce, `client/modules/src/index.ts:907-909`); emit
  `/plugins/??…&rev=…` URLs; partition into one bootstrap batch
  (`@deepseek-ai/dsh-client-modules`) and one-or-more application batches.
- `internal/dshboot/inject.go` — render the injected `index.html`: the inline
  `window.__ModuleLoader__` queue script (copy `client/modules/src/index.ts:511-534` verbatim),
  the application `<link rel=preload as=script>` rows, the bootstrap `<script src>` row, the
  `__DSH_BOOT__` global row, and the `__DSH_BOOT_READY__` tail
  (`host/webserver/src/injections.ts:86,96-118`), plus `<base href="/">` if any non-root path can
  render the index.
- Routes: `GET /plugins/...` (exact advertised URLs, `text/javascript`, immutable cache),
  `GET /` + assets.

**Risk:** getting the graph wrong is a hard boot failure; start by mirroring the emitters
byte-for-byte. Verify with a headless browser check that `#root` gets the React tree.

### Step 2 — `/api` unary RPC + auth fence (small)

- `POST /api/{ns}/{method}` handler that parses `{type,rpcId,method,payload.args}`, dispatches,
  and replies `{type:"server-response",rpcId,result:{ok,value}}`; echo `rpcId` exactly; 404 for
  unknown endpoints.
- Loopback-only bind (already the `serve.go` default direction — `isLoopbackListenAddr`,
  `cli/serve.go:253`) plus the Host/Origin fence (`api-request-trust.ts:91-118`) as cheap
  insurance. Auth cookie: optional; if omitted, keep the bind loopback-only.
- Implement namespaces: `session` (§3), and a stub `settings`/`workspace` if the chosen roster
  needs them (see Step 5).

### Step 3 — WS mux + streams (medium)

- `GET /api/remote.mux` upgrade; parse `{type:open|cancel}`; maintain `streamId → producer`.
- Implement three endpoints:
  - `$events`: first item = `ready` frame; then emit `api-session/*` and `approval/request`
    waterfalls; accept answers on `POST /api/$events/result`.
  - `session/control`: one `baseline` item, then optional `jobs`/`projection` frames.
  - `session/follow`: `snapshot` from `RunManager`/`eventlog` history, then live `event` frames
    (mapping `zenforge.Event` → `SessionWireEvent`), plus optional `assistant-stream` chunk frames
    from `model.delta` for token-level streaming.
- Ping/pong heartbeat (2 s) or skip enforcement.

**Risk (highest):** the `session/follow` snapshot/`surfaceOp`/`assistant-stream` semantics.
**Corrected 2026-09-20 (ADR 0105):** the "simplest legal encoding" below was wrong, and
the console said so on the first non-empty history it read —
`session event "run.started" is not surface-eligible and cannot carry surfaceOp`.
`surfaceOp` is legal on exactly the four surface-eligible types (`system/message`,
`user/message`, `assistant/message`, `tool/result`; `SURFACE_EVENT_TYPES` in
`session-controller/client.js`), and an unknown event name needs the envelope's
`ignorable: true` marker. The paragraph is kept as the measurement it was.
The Go host can adopt the simplest legal encoding: every durable event appended with
`surfaceOp:"append"`, monotone `seq`, `data` = the zenforge event payload flattened to JSON,
custom event `type` names allowed (`validateSessionEventData` only constrains `request/header` and
`tool/result`). Unknown-to-client event names render opaquely rather than fail — but they also
render *nothing*, so mapped names (`user/message`, `assistant/message`, `tool/call`, `tool/result`)
are what actually produce a transcript.

### Step 4 — approvals wiring (small)

`approval.PendingBroker` → `$events` `waterfall` frame with a fresh `eventId`; on
`$events/result` with `value ∈ {allowed-once, rejected}` resolve the pending request; emit
`approval/asked`/`approval/decided`-style durable events for the audit trail. The existing
`ApprovalInbox` and `SerializeApproval` paths (`server/harnesshttp/handler.go:503-590`) already
carry the same information.

### Step 5 — roster tiers (small each)

Enable the roster in tiers so nothing is `pending`:
T0 = §4.2 minimum (chat + approvals); T1 = + `ui-settings*`/`ui-model-selection`;
T2 = + `ui-jobs`/`ui-plan`/`ui-skill`/`ui-sidebar-files`; T3 = full 53. Each tier needs the
matching *host* namespaces (`settings`, `workspace`, `fileReferences`, `skills`, `directoryPicker`,
`terminal`) or those panels show error states. Removing a panel package is safe **only** if no other
enabled plugin injects its service.

### Step 6 — branding (small if fork-built, small either way for static bits)

Replace favicon/manifest/title in the served HTML; register zenforge occupants for
`sidebar.brand.mark` / `sidebar.brand.name` (one extra client bundle in the graph); if fork-building,
also patch `boot-page.ts` `'HARNESS'`, `FishLogo`/`BrandWordmark` fallbacks, `EmptyHero` `HeroFish`,
and build with `DSH_CLIENT_TITLE=ZenForge`.

### Effort and risk summary

| Step | Effort | Main risk |
|---|---|---|
| 0 frontend strategy (fork build vs published) | S (decide) / M–L (fork build) | build toolchain reproducibility |
| 1 shell + boot graph | M | graph shape errors are fatal; exact injection ordering |
| 2 `/api` unary RPC | S | `rpcId` echo, exact envelope keys |
| 3 WS mux + 3 streams | **L** | `session/follow` snapshot/seq/`surfaceOp` semantics; live chunk framing |
| 4 approvals | S | eventId/clientId correlation; fail-closed mapping |
| 5 roster tiers | S each (M total) | service-closure discipline: a missing provider fails the whole boot |
| 6 branding | S static / M fork build | baked strings in shell + 3 client packages |

**Riskiest unknowns for zenforge, ranked**

1. `session/follow` — the exact client acceptance path for durable frames and the
   `assistant-stream` chunk contract; the code is strict but under-documented outside the type
   declarations cited above.
2. `session/control` projections — the client tolerates `{}`, but which projection keys the chat
   stats strip / turn rail need (e.g. `sessionStats`, `turnOutline`) is not enumerated in this
   report; those come from host projection providers (`dsh-session-stats`,
   `dsh-session-turn-outline`) that zenforge would have to synthesize.
3. Rebranding the published bundles — impossible without a fork build.
4. Version drift: shell and plugin bundles must come from one release; vendoring must pin all
   `@deepseek-ai/*` together.

### Explicit non-goals / can be stubbed

- `settings`, `terminal`, `workspace-files`, `directoryPicker`, `skills`, `fileReferences`,
  `subagents` namespaces — stub with `{ok:false,error:{code:"gateway/service-unavailable",…}}`
  or omit the endpoint (404); panels degrade, boot survives.
- `client-hmr` — omit it entirely (and do not serve `/plugins/events`); the mux route only exists
  when the plugin is in the roster (`api/gateway/src/index.ts:222-228`).
- Desktop/worker carrier (`dshDesktopBoot`, `__DSH_TRANSPORT__.rpc/fetch/loadBundle`) — not needed.
- Auth cookie/token flow — replaceable by loopback-only binding.

---

## 8. What I could NOT determine from the source

1. **The absolute "minimum" roster is not stated anywhere.** §4.2 is a computed closure over the
   published `dsh.client.inject` edges and the bundles' exported `inject` arrays; it is an
   inference, not a documented list. In particular `remote.session`/`remote.workspace`/
   `remote.settings`/`remote.directoryPicker` are *dynamically* registered by
   `dsh-api-remotes`, so a static closure cannot prove the exact set; it may include one or two
   packages that could be dropped.
2. **`SessionProjectionMap` keys** (`types.ts:20-42`) are merge-extensible and contributed by host
   packages I did not exhaustively read; I cannot say which projections the chat stats strip,
   turn rail, or sidebar require in practice.
3. **The `$events` `emit` argument order** for each forwarded event is only implied by the
   `Events` interface declarations (`types.ts:560-596`); I verified shapes for
   `api-session/added|removed|status|activity|error` from those declarations but did not confirm
   that the console's listener for every one of them is present in the minimum roster.
4. **Whether the client tolerates a `session/follow` snapshot with `records: []` and
   `cursor: 0`** as a first frame — the type allows it, but I did not trace the client's
   `ordered-baseline`/journal adapter far enough to guarantee it.
5. **The exact tick/ordering semantics of `assistant-stream` `revision`/`index`** across a
   reconnect (the client enforces "dense position expected for the next live chunk frame",
   `types.ts:465`). A Go host must produce dense, monotone `index` per attempt; I did not find a
   normative statement of what happens when `revision` changes mid-attempt.
6. **`authorizeIndex`'s exact cookie/redirect sequence** (302 vs 303, `Set-Cookie` attributes) —
   only the failure body and the cookie payload format were read; the success path's status codes
   are in `browser-auth.ts` beyond the lines I inspected. Irrelevant if zenforge skips auth.
7. **Published-artifact equivalence to `master`.** The installed packages are `0.1.5-rc.2`; the
   cloned `master` is `0.1.6-alpha.2`. I verified the shipped shapes I quote in both where possible
   (bundle factory format, index.html, brand gate, `inject` arrays), but the repo citations are
   from `master` and the "published" claims are from the installed `0.1.5-rc.2` tarballs.
8. I did **not** run the DSH console against a mock host, so every ordering claim in §1.1 is
   derived from the source (which is explicit about ordering) rather than an observed trace.
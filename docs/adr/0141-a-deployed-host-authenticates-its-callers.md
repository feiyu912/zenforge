# 0141. A deployed host authenticates its callers: one boundary over every route, the browser session is the token, and a token attributes rather than partitions

- Status: Accepted
- Date: 2026-09-24
- Related: 0078 (the console on localhost, and the `--allow-remote` decision this
  makes safe), 0081 (a method error is HTTP 200 and the console's error
  vocabulary is closed), 0099 (the harness owns no auth and the core carries no
  console vocabulary), 0101 (the console's workspace registry is process-local),
  0070 (the signed webhook), 0089 (the console reads workspace files back to the
  browser), 0135 (an unserved surface reaches the console as a transport
  failure), 0137 (the verification recipe), 0140 (the sibling chain that gave a
  run its own route)

## Context

`server/harnesshttp` has served a complete detached-run API since ADR 0078, and
`zenforge serve` has been the application that wires it up. What that host never
had was any idea who its callers were. ADR 0078 kept the listener on loopback by
default for exactly that reason -- the run and settings APIs were unauthenticated,
so the operator was expected to be sitting at the machine that ran the server --
and `--allow-remote` was the one flag that would bind a real network interface
anyway. Setting it exposed every route at once: the console's `/api/*` RPC
surface, the harness's `/runs/*`, `/approvals` and `/approval`, `/api/settings`
(which holds the provider credential), `/api/server`, both stream paths, and the
signed `/webhook/run` trigger.

The only gates in front of any of that were `--allow-remote` plus two
console-shaped trust fences: the Host/Origin/RemoteAddr check in
`internal/dshapi/handler.go` (`fence`) and its shape-identical copy in
`internal/dshstream/fence.go`. Both belong to the console's request envelope, and
both have two holes a deployment cannot close. First, they are the console's own
code, so the harness routes never passed through either of them: a `/runs/start`
on a remotely bound host had no peer check at all. Second, and worse, the check
they make -- "the peer is loopback" -- is not a security property on a machine
that runs a reverse proxy. A proxy on the same host reaches the listener as a
loopback peer, so the one rule the fences relied on is the very rule a proxy
defeats. `--allow-remote` was therefore a remote shell with a stylesheet, and
nothing in the tree recorded which caller a run had been started for.

The harness deliberately owns no authentication (ADR 0099, and the deployment
guide says so): it exposes `server/harnesshttp`'s `AccessController` seam and the
application supplies the policy. Nothing supplied one. This decision is the
policy the shipped `zenforge serve` application installs, built so that an
embedder that already has its own tokens or its own user directory can ignore it
entirely and keep using that seam.

## Decision

### 1. `server/auth` is the served host's caller identity

A new public package, `server/auth`, holds the four things a host needs to answer
"who is calling, and may they": a `Token` (a tenant, a subject, and the hash it is
stored as), a `TokenStore` (`token.go`) that keeps `sha256:` hashes and looks a
presented secret up, an `Authenticator` (`authenticate.go`) that turns a bearer
header or the session cookie into a `Token`, a `Policy` (`policy.go`) that decides
whether a request is served at all, and an `AuditLog` (`audit.go`) that records
the decision. Its package doc states the premise in one sentence, and the
Context above is that sentence's evidence.

It is a public package rather than a `cli`-internal one because the identity it
resolves is the harness's own `approval.Namespace` on the way in and a
`harnesshttp.AccessController` on the way out, so an embedder can read the whole
contract without adopting the CLI. Nothing in it is reached except through
`net/http`: the package takes an `http.Request` and answers one.

### 2. One middleware over the whole route table

`newServeMux` (`cli/serve.go`) now returns an `http.Handler` rather than the
`*http.ServeMux` it assembles, and its last statement is `cfg.auth.wrap(mux)` --
`serveAuth.wrap`, which is `auth.Policy.Middleware`. The return type changed for a
reason and not for tidiness: a caller that assembled a listener by hand could
otherwise forget the boundary, and a gate that a later route can be added behind
is a gate a later route will be added behind.

Because the wrap is around the assembled table, the boundary sees every route that
table registers: the harness routes `/runs/start`, `/runs/resume`, `/runs/status`,
`/runs`, `/runs/attach` and `/runs/cancel`; `/approvals` and `/approval`;
`/api/settings`; `/api/server`; both stream paths (`dshstream.MuxPath` and
`dshstream.EventsResultPath`); the signed `/webhook/run`; the sign-in routes; and
the console's catch-all at `/`, which is where its shell, its staged assets and
its `/api/<namespace>/<method>` surface are served. That is the whole of what a
peer can reach, and it is the only layer that sees all of it.

### 3. The refusal is a real HTTP status, not a 200 envelope

A refused request gets `401` with `WWW-Authenticate: Bearer` and a body of
`{"error":{"code":"unauthorized","message":...}}` (`auth.Refusal`,
`auth.WriteRefusal`). The status is the point, and the reason it is not a 200 RPC
envelope is the console itself. Its vendored connection client throws on any
non-2xx before it parses anything
(`webui/dsh/plugins/client/connection/client.js`: `if (!response.ok) throw new
Error("transport failure for ... HTTP " + status)`), and its declared error-code
vocabulary -- the `RemoteErrorDetailsMap` that
`@deepseek-ai/dsh-api-gateway`'s `RemoteError` names as its source of codes -- has
no code for an unauthenticated caller; the word does not appear anywhere in the
vendored console. A 200 envelope carrying an invented code would be a code the
client does not know, and it would surface as a business error rendered inside a
panel rather than as the sign-in problem it is. HTTP already has the word for
this, and ADR 0081's own rule -- a method error is HTTP 200, a protocol error is
not -- puts a missing credential on the right side of that line. ADR 0135 met the
same fact from the other direction: a surface the console cannot reach arrives as
a transport failure an operator cannot act on.

A browser navigation is the one case that gets something other than a status,
because a person staring at a bare 401 cannot act on it. `auth.WantsHTML` (`GET`
with `Accept: text/html`) together with `auth.ShellPath` (`/` or `/index.html`)
redirects `303` to `/auth`, where the sign-in form is. Only the console's own
document is redirected; an asset or an RPC answered with a `303` would confuse a
client that is not a person. The redirect is decided before the public-path rule,
and the shell's path (`/`) is deliberately not a public one: the console's own
document is host content like any other, so a client that is not a person gets the
refusal rather than the shell. Only `/auth` and `/auth/session` are public -- a
browser with no session has to be able to reach the form that gives it one.

### 4. The identity is `approval.Namespace`, and no new vocabulary was invented

The caller identity is the type the core already has: `approval.Namespace`, "the
host-owned identity used to isolate persistent grants" (`approval/store.go`), a
tenant and a subject. A `Token` carries both and `Token.Namespace` returns them.
Two conductors were added (`approval/identity.go`, `approval.WithNamespace` and
`approval.NamespaceFrom`) so an HTTP middleware can put the caller in the request
context and the code that starts a run can read it back, without every function
in between growing a parameter for it.

That is the whole identity vocabulary. No console word entered the core (ADR 0099):
not session, not panel, not user, not login. What the harness learned is the
sentence it already had -- a run may be started under a tenant and a subject --
and the token's own vocabulary (its id, its hashed secret) appears only in the
audit trail, which is the operator's view of their host rather than the core's.

### 5. The identity reaches the run two ways, and what it buys

`server/harnesshttp` already had an `AccessController` seam with an
`AccessDecision`; no production caller wired it. `AccessDecision` gained
`ApprovalNamespace`, and the places that start a run through the harness routes --
`ServeRun`, `ServeDetachedStart`, and the webhook -- now put it on the
`zenforge.Task` they build. `cli/serve.go` installs the identity-carrying
controller on `harnesshttp.RuntimeOptions.Access` through
`serveAuth.accessController`, which reads the namespace the boundary resolved out
of the request context and returns it as the decision. That controller never
refuses: the boundary already did, and a second opinion here would be a second
policy to keep in step.

The console's own prompt path did not need the controller, because it is not the
harness route: `internal/dshapi/session.go`'s `callerIdentity(ctx)` reads the same
request context and sets `Task.ApprovalNamespace` on both the first turn and the
continuation, so a later turn of a conversation runs under the identity that
started it. `agent.go`'s `approvalNamespace` then prefers the task's namespace
over the agent's configured one and memoizes it into the run's durable Meta
(`zenforge.approval.tenant` and `zenforge.approval.subject`), which is what a
resumed run reads back.

What this buys is the one isolation the core has: a run's persistent approval
grants are recorded under the caller's tenant and subject, so a grant one tenant
recorded never answers for another's call. Before this, `ApprovalNamespace` was
whatever the host was configured with, and every caller shared it.

### 6. `--allow-remote` is fail-closed, and loopback is not an exemption

`resolveServeAuth` (`cli/auth.go`) computes `required := requireAuth ||
(allowRemote && !allowAnonymousRemote)`. `--allow-remote` therefore now implies
`--require-auth` unless the operator also passes the new
`--allow-anonymous-remote`, which is the explicit escape hatch for a network the
operator already trusts (a private bridge, a VPN). Both old behaviours are still
reachable; neither is the default.

A host that is asked to require tokens and holds none refuses to start, naming the
mint command; a host that requires authentication and cannot keep an audit trail
refuses to start as well, naming `--audit-log`. There is deliberately no loopback
exemption when authentication is required. The reason is the one above: a reverse
proxy on the same machine reaches this host as a loopback peer, so "loopback is
trusted" is precisely the hole that made `--allow-remote` unsafe. A loopback
caller presents a token like any other when the requirement is on. Nothing on the
request path asks whether a peer is loopback, which is the difference between this
policy and the fences it replaces.

### 7. The browser session is the token

`GET /auth` serves the sign-in form, a constant document with one field and no
script; it posts to `POST /auth/session`, which verifies the token and sets
`zenforge_session` (`auth.SessionCookieName`). The cookie is `HttpOnly`, `Path=/`,
`SameSite=Strict`, and `Secure` only when `Authenticator.Secure` was configured --
never inferred, because a browser silently drops a `Secure` cookie that arrives
over plain HTTP, so marking one on an HTTP host produces an endless sign-in loop
rather than any hardening. The cookie's value is the bearer token itself,
deliberately, for two reasons.

The first is the shipped console. It is vendored and cannot be extended to send an
`Authorization` header, but its RPC `fetch`, its `EventSource` and its `WebSocket`
are all same-origin and all carry cookies, so a cookie is the one credential a
console that cannot be changed will present. The second is that a second derived
session secret would add a second thing to rotate without adding a boundary.
Because the cookie is the token, revoking the token ends the browser session on its
next request, which is the behaviour wanted; the host keeps no session table and
there is nothing else to revoke.

`SameSite=Strict` is the second lock on that door, beside the console's own
`Sec-Fetch-Site` and Origin fence. That fence is the console's code and holds only
for the routes that run through it; the cookie attribute holds for every route the
cookie reaches.

### 8. The audit trail

`--audit-log` names the file, and whenever authentication is required the default
is `<config-directory>/audit.jsonl` (`cli/auth.go`). It is append-only JSONL, one
line per decision, opened with `O_APPEND` and never truncated, and each record is
one `Write` with no buffer behind it, so a reader that sees a line sees a whole
entry and a host that dies immediately after still leaves a readable line.

A line records the time, the decision, the reason, the method, the path, the
status the caller actually got, the remote address, and the tenant, subject and
token id when an identity resolved. It never records a request body, a header
value or a credential; `server/auth/audit_test.go` pins that by asserting the
exact JSON key set of an allow line and a deny line, so a later edit cannot widen
the schema by accident. The path is recorded without its query string, because a
URL is where a credential could ride and a trail outlives the request that carried
it. The reason distinguishes `missing-token` from `invalid-token`, because a
client that never presented a credential and a client whose token was revoked are
different events to triage.

A failed write is logged and the request is still served. Two failures are being
kept apart: the answer to the caller has already been written and must not change,
and a full disk must not take the host down -- but a trail that quietly went
nowhere is worse than a loud fault, so the loss is reported rather than silent.

### 9. `zenforge token create|list|revoke` is the only minter

`cli/auth.go` implements the three subcommands and `cli/cli.go` dispatches them.
`create` is the only code in the program that produces a plaintext token, and it
prints it exactly once: the store writes `auth.HashToken`'s `sha256:` value and
nothing else, so a minted token is never recoverable and a stolen token file
cannot be replayed as a credential. `list` prints id, tenant, subject, creation
time and note but never a hash or a secret. `revoke` removes a token by id,
rewriting the file before memory so a revoke survives a restart, and reports
`ErrTokenNotFound` rather than silence when the id names nothing.

Tokens do not expire. There is no expiry field on `Token` and no clock on the
lookup path; a credential is invalidated by revoking it, which the audit line's
token id is what makes actionable. A running host re-reads the token file every
two seconds (`serveAuth.watchTokens`), so a mint or a revoke an operator makes in
another process takes effect within that interval without a restart. A revoke that
only took effect at the next restart would be a control an operator believes they
have and does not; a reload that failed keeps the last set it read and says so in
the log, so a transient read error does not lock the host's callers out. A store whose file another user can read is
refused at open with the `chmod 600` command named, rather than repaired: it may
already have been copied, and that is the operator's decision to make, not the
host's.

### 10. Honest boundary: a token authenticates and attributes, it does not partition

This is the subsection a reader must not skip, because the word "tenant" invites a
stronger reading than the code supports. A token proves who is calling and
attributes the run's approval grants to them. It does not partition the console's
data plane, which is host-global by construction. One process serving two tenants
serves one set of everything: the sessions and the durable run registry, the event
log and the checkpoints under `--checkpoint-dir`, the goal store, the attachment
store, the console's workspace registry (one directory, ADR 0101),
`console-settings.json` -- including the single provider credential every tenant
would be using -- and the jobs manager. `MaxActive: 16` (`cli/serve.go`) is one
global semaphore, not a per-tenant one.

The pending-approval plane is un-namespaced too, and it is the sharpest case
because it is interactive: `internal/dshstream/events.go`'s `deliverApprovals`
lists the inbox with an empty filter (`h.inbox.List(ctx, "")`), so every pending
approval is broadcast to every connected client, and an answer is matched by event
id alone. A token therefore does not keep one tenant's approval prompt out of
another tenant's panel.

A deployment that must isolate tenants meanwhile should run one process per tenant,
each with its own `--addr`, `--workspace`, `--checkpoint-dir`, `--settings-file`
and token file. That is not a workaround dressed as a design: process-per-tenant
isolates all of the above at once, precisely because all of it is process state,
and it needs no code this chain did not already write. The next chain is
per-tenant session ownership enforced at the `RunManager` and projection seam -- a
run recorded with its owner's namespace, `session/list` and `session/page`
filtered by it, and the approval inbox queried per run rather than with an empty
filter. Until that exists, a host with two tenants is one tenant's data plane with
two sets of tokens, and this record says so rather than implying otherwise.

### 11. What is also not in this chain

No quotas and no rate limits. Per-tenant concurrency is a separate decision, and
`MaxActive` remains the single global semaphore described above, so one tenant can
consume the host's run slots.

No cost accounting. `harness.UsageState.CostUSD` is still declared and still never
written, so a token does not let an operator say what a tenant spent.

The webhook's one shared secret still authorises every webhook run: any caller
holding `--webhook-secret` can start a run, and that secret carries no tenant, so
the run it starts is not attributed by it. The boundary does now sit in front of
the endpoint, since `/webhook/run` is registered on the same mux, so a token is
required before the secret is even examined when authentication is on -- but the
signature and the token are two independent gates and only one of them carries an
identity.

### 12. The deviations this decision records rather than hides

- `--allow-remote` without `--allow-anonymous-remote` now refuses to start. That
  is a deliberate behaviour change to an existing flag, and the startup error
  names the escape hatch, so an operator whose deployment breaks is told which
  two changes to make rather than left with a port that will not bind.
- The console's in-panel experience when unauthenticated is a transport failure,
  not a rendered error. There is no UI branch for it in the vendored client -- it
  throws before it can render one -- and none will be invented here. A person
  reaches `/auth` by navigation; a panel that was already open fails its call
  until the page is reloaded after signing in.
- Loopback callers are subject to the token requirement when it is on (6). The
  consequence is that enabling authentication on a host the operator also uses
  locally requires that operator to sign in too, which is the intended cost of the
  reverse-proxy reasoning.
- The cookie carries the token itself (7). A page script cannot read it
  (`HttpOnly`), but anything that can read the cookie jar holds the credential.
- The two tests that are environment-bound in the development sandbox are not part
  of this change and do not appear in the runs above: `go test ./tools/jobs/`'s PTY
  test for the reason ADR 0137 recorded, and `sandbox/seatbelt`'s
  `TestSeatbeltActuallyEnforcesTheProfile`, which asserts that a write outside the
  writable root is refused and cannot observe that refusal when the outer harness
  grants full file access. Both fail identically at the commit this chain starts
  from, which is how they were told apart from this change's own runs.

## Consequences

- A host bound to anything but loopback now has a caller identity, and a remote
  peer has to name itself. A loopback peer does too when the requirement is on,
  which is the difference between a network position and a credential.
- An existing `--allow-remote` deployment fails closed on upgrade until it mints a
  token or passes the escape hatch. The failure is a startup error naming both,
  not a silently exposed port.
- A run's persistent approval grants belong to the caller that started it, so a
  grant one tenant recorded cannot answer another tenant's call -- the one
  isolation the core's `approval.Namespace` was built for, now actually connected
  to who called.
- The console is unchanged for a person: they sign in once at `/auth` and the
  cookie carries the session. An API client presents `Authorization: Bearer
  <token>`, which wins over the cookie.
- A host that serves two tenants is still one data plane (10). A deployment that
  must isolate them runs one process per tenant, or waits for the per-tenant
  session chain this record names.
- New secret-bearing files exist on disk: the token file (`0600`, beside the
  console settings) and the audit trail (`0600`, in a `0700` directory). Both are
  refused inside the workspace this host serves, because the console reads
  workspace files back to the browser (ADR 0089).
- An embedder is unaffected: `server/harnesshttp`'s `AccessController` is
  unchanged in shape except for the field it gained, and a host that installs none
  behaves exactly as before.

## Verification

- `go test ./server/auth/` pins the boundary's own halves: a bearer header
  preferred over the cookie and the cookie accepted when no header is present; a
  token resolved to a namespace and an unknown, empty or revoked one refused; the
  sign-in form setting the cookie and redirecting, and a bad token refused without
  a cookie; the cookie `Secure` only when configured; a policy admitting an
  anonymous caller when nothing is required, admitting the public sign-in paths
  when something is, refusing a missing token and an invalid one with different
  reasons, redirecting a shell navigation, carrying the identity into the request
  context, and writing the audit line's path without its query string; the audit
  log's append-only, one-line-per-record, intact-under-concurrency behaviour and
  its exact JSON key set; and the token store's hash-only storage, atomic rewrite,
  mode check, reload, and the fact that a minted secret is never on disk.
- `go test ./cli/` pins the flag decision and the assembled table: a remote host
  without tokens refuses to start; `--allow-anonymous-remote` is the only thing
  that lets it bind anyway; a required host with no token file and no audit path is
  refused with the flag named; the audit trail defaults beside the token file; a
  token file or an audit trail inside the workspace is refused; the boundary serves
  only an authenticated caller and admits everyone when nothing is required; that
  the caller's tenant reaches the handler behind the boundary and the harness
  routes' own access decision, so a run is attributed rather than anonymous; and
  that `token create`, `list` and `revoke` mint, report and invalidate, with a
  mint or a revoke written by another process reaching a running host through the
  reload interval.
- `go test ./approval/ ./server/harnesshttp/ ./internal/dshapi/` pins the
  identity's journey: the context conductors round-trip a namespace, refuse a
  foreign value and accept a nil context; the harness routes put the decision's
  namespace on the task they start; and the console's prompt path does the same on
  the first turn and on a continuation, so a run's grants are the caller's.
- `go vet ./...` is the recipe's static half, `gofmt` its formatting half -- both
  enforced by `docs/format_test.go` rather than by a workflow step (ADR 0137) --
  and `mkdocs build --strict` its documentation half, which is why this page has
  its `docs/adr/index.md` row and its `mkdocs.yml` navigation entry in the same
  change rather than a later one.
- Live on a real host (a real `zenforge serve --addr 0.0.0.0:PORT --allow-remote`
  with a token minted by `zenforge token create`: an anonymous `curl` to
  `/api/server` gets `401` with `WWW-Authenticate: Bearer`; the same call with
  `Authorization: Bearer <token>` gets `200`; the audit file's line for each names
  the tenant, the subject and the peer it came from; and the console's own
  `/api/<namespace>/<method>` call, made without a credential, is refused the same
  way rather than rendering inside a panel):
  ```
== binary ==
sha256: d24dbc9c4c3450fc426317dce4329a5676c1fd9cdea5f518eeff41b89517dcb9
== 1. --allow-remote without tokens is refused before it binds ==
exit code: 2
error: invalid config or usage: authentication is required but /tmp/zf-auth-live/tokens.json holds no token: mint one with `zenforge token create --tenant <tenant> --subject <subject> --token-file /tmp/zf-auth-live/tokens.json`, or pass --allow-anonymous-remote if this network is already trusted
== 2. zenforge token create prints the plaintext once ==
id:      tok_c82ef0bf0b28
tenant:  acme
subject: ci
token:   $TOKEN
/tmp/zf-auth-live/tokens.json keeps only the hash of this token, so this is the only time it is shown.
Present it as `Authorization: Bearer <token>`, or paste it into a served console's /auth page.
secret length: 67
token file (hashes only):
{
  "version": 1,
  "tokens": [
    {
      "id": "tok_c82ef0bf0b28",
      "tenant": "acme",
      "subject": "ci",
      "hash": "sha256:1e9e11ef68eba9945516680780aa4f1dfac654ae94097861da8d3a32f0b8d66f",
      "createdAt": "2026-09-24T05:01:24.763046Z",
      "note": "canary"
    }
  ]
}
does the file contain the plaintext? 0 (0 means no)
== 3. start the host on the network with the token file ==
zenforge serve listening on http://[::]:9633
authentication: required; sign in at http://[::]:9633/auth, and present tokens as `Authorization: Bearer <token>`
audit trail: /tmp/zf-auth-live/audit.jsonl
== 4. an anonymous API call is refused ==
HTTP/1.1 401 Unauthorized
Content-Type: application/json
Www-Authenticate: Bearer
{"error":{"code":"unauthorized","message":"this host requires a bearer token; mint one with `zenforge token create`, then present it as `Authorization: Bearer \u003ctoken\u003e` or sign in at /auth"}}
== 5. the console's own RPC path is refused the same way ==
status: 401
{"error":{"code":"unauthorized","message":"this host requires a bearer token; mint one with `zenforge token create`, then present it as `Authorization: Bearer \u003ctoken\u003e` or sign in at /auth"}}
== 6. the same calls with the bearer token are served ==
HTTP/1.1 200 OK
{"allowRemote":true,"workspace":"/tmp/zf-auth-live/workspace"}
session/list with the token:
status: 200
{"type":"server-response","rpcId":"live-2","result":{"ok":true,"value":{"items":[]}}}
== 7. the browser path: the sign-in form, then the cookie ==
GET /auth: 200
action="/auth/session"
POST /auth/session: 303
Location: /
Set-Cookie: zenforge_session=$TOKEN; Path=/; Max-Age=43200; HttpOnly; SameSite=Strict
GET /api/server with the cookie: 200
GET / as a browser: 303
Location: /auth
GET / as a JSON client: 401
== 8. who does the host think I am ==
{"subject":"ci","tenant":"acme","tokenId":"tok_c82ef0bf0b28"}
== 9. the audit trail ==
{"time":"2026-09-24T05:01:25.857111Z","decision":"allow-public","method":"GET","path":"/auth","status":200,"remoteAddr":"127.0.0.1:53501"}
{"time":"2026-09-24T05:01:25.876024Z","decision":"deny","reason":"missing-token","method":"GET","path":"/api/server","status":401,"remoteAddr":"127.0.0.1:53502"}
{"time":"2026-09-24T05:01:25.892099Z","decision":"deny","reason":"missing-token","method":"POST","path":"/api/session/list","status":401,"remoteAddr":"127.0.0.1:53503"}
{"time":"2026-09-24T05:01:25.908808Z","decision":"allow","method":"GET","path":"/api/server","status":200,"tenant":"acme","subject":"ci","tokenId":"tok_c82ef0bf0b28","remoteAddr":"127.0.0.1:53504"}
{"time":"2026-09-24T05:01:25.923368Z","decision":"allow","method":"POST","path":"/api/session/list","status":200,"tenant":"acme","subject":"ci","tokenId":"tok_c82ef0bf0b28","remoteAddr":"127.0.0.1:53505"}
{"time":"2026-09-24T05:01:25.937612Z","decision":"allow-public","method":"GET","path":"/auth","status":200,"remoteAddr":"127.0.0.1:53506"}
{"time":"2026-09-24T05:01:25.951492Z","decision":"allow-public","method":"POST","path":"/auth/session","status":303,"remoteAddr":"127.0.0.1:53507"}
{"time":"2026-09-24T05:01:25.965194Z","decision":"allow","method":"GET","path":"/api/server","status":200,"tenant":"acme","subject":"ci","tokenId":"tok_c82ef0bf0b28","remoteAddr":"127.0.0.1:53508"}
{"time":"2026-09-24T05:01:25.97582Z","decision":"deny","reason":"missing-token","method":"GET","path":"/","status":303,"remoteAddr":"127.0.0.1:53509"}
{"time":"2026-09-24T05:01:25.988765Z","decision":"deny","reason":"missing-token","method":"GET","path":"/","status":401,"remoteAddr":"127.0.0.1:53510"}
{"time":"2026-09-24T05:01:25.998211Z","decision":"allow","method":"GET","path":"/auth/session","status":200,"tenant":"acme","subject":"ci","tokenId":"tok_c82ef0bf0b28","remoteAddr":"127.0.0.1:53511"}
lines: 11
does the trail contain the secret? 0 (0 means no)
does the trail contain a query string? 0 (0 means no)
== 10. a mint an operator makes while the host runs is picked up ==
GET /api/server with the token minted after start: 200
{"subject":"ops","tenant":"globex","tokenId":"tok_b565ff73e82d"}
== 11. revocation ends the session, without a restart ==
revoked tok_c82ef0bf0b28
immediately after the revoke:
  bearer: 200
after the reload interval:
  bearer: 401
  cookie minted from it: 401
the token minted in step 10 still works: 200
server stopped

  The run behind that transcript: `go build -o /tmp/zf-auth-live/zenforge
  ./cmd/zenforge` over the working tree this record describes, served on
  `0.0.0.0:9633`, with `--auth-token-file` and `--audit-log` under
  `/tmp/zf-auth-live`. Nothing in it is a real credential: the two minted secrets
  were replaced with `$TOKEN` and `$TOKEN2` after the run, and the trail and the
  transcript were both checked for them.

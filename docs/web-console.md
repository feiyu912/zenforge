# Web Console

`zenforge serve` starts the HTTP harness and a browser console, so a run can be
started, watched, and cancelled from a page instead of a terminal. The console
is served by the same process and the same origin as the API: there is no second
server to start, no CORS to configure, and no build step — the interface is
embedded in the binary.

The page is the DSH console's own client interface, vendored unchanged, and it
is a *consumer* of this host: each control is backed by a remote method the
host answers. This host answers a subset of them, and refuses others by name
rather than answering something empty that would read as success. Which ones —
and what each missing panel needs — is the
[console coverage ledger](dsh-console-coverage.md).

## Quickstart with an OpenAI-compatible endpoint

```bash
zenforge serve --base-url https://dashscope.aliyuncs.com/compatible-mode/v1 \
  --model qwen-plus --api-key "$DASHSCOPE_API_KEY"
```

Then open <http://127.0.0.1:8787>. Start a task from the input box, and click a
run in the left sidebar to watch its events arrive. Any provider that speaks the
OpenAI chat-completions protocol works; `--provider anthropic` selects the
Anthropic shapes instead.

The key can also be given from the page: open the settings panel, fill in the
base URL, model, provider, and key, and save. See
[Keys entered in the page](#keys-entered-in-the-page) for what that does and
does not persist.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--addr` | `127.0.0.1:8787` | Address to bind. A non-loopback address requires `--allow-remote`. |
| `--allow-remote` | off | Permit binding a non-loopback address. Implies `--require-auth` unless `--allow-anonymous-remote` is also given. |
| `--require-auth` | off | Serve only requests that present a valid token. Implied by `--allow-remote`. |
| `--allow-anonymous-remote` | off | Let `--allow-remote` expose the host without tokens, for a network already trusted. |
| `--auth-token-file` | `<config dir>/tokens.json` | File the accepted tokens are kept in (hashes only). |
| `--audit-log` | `<config dir>/audit.jsonl` when authentication is required | File every admission decision is appended to. |
| `--run-timeout` | server default | Bound on a run started from the console. |
| `--webhook-secret` | unset | When set, the signed webhook endpoint (ADR 0070) is served as well. |
| `--settings-file` | `<config dir>/console-settings.json` | The file the console's settings and credential persist to (ADR 0102). |

The usual options apply too: `--config`, `--workspace`, `--model`,
`--base-url`, `--api-key`, `--provider`, `--approve`, and the tool and sandbox
options. `zenforge serve --help` lists them.

## What the console shows

- **Runs**: every recorded run, newest first, with its status and the step it
  reached. Several runs can be in flight at once; the sidebar is a live view of
  the run registry, not a queue.
- **Transcript**: the selected run's streamed events — assistant text, tool
  calls and their results as collapsible cards, and errors. A page refresh
  re-reads what the store recorded, so leaving the page does not end a run.
- **Cancel**: stops the selected run through the same endpoint the CLI uses.
- **Settings**: the model endpoint and credential the server will use for runs
  started afterwards.

## Keys entered in the page

- A page-set key is written to one place: `console-settings.json` in the host's
  own configuration directory, created `0600` and renamed into place (ADR 0102).
  It is never logged, never returned by the API, and never written anywhere else:
  the settings endpoint reports only whether a key is set.
- An empty key field means "keep the key you already have", so a model can be
  changed without re-typing a credential the page was never shown.
- Command-line and environment values seed the host; the settings document
  overrides them for every field it names, and the startup line says which fields
  it overrode (by name, never by value). A key the operator keeps in an
  environment variable is left there: the document stores the inline key, not the
  name of a variable to read.
- Restarting the host costs nothing: the endpoint, the model, the credential, the
  declared provider profiles and the settings revisions are read back before the
  first run is served, and so is the model each session chose in the composer
  (most recent 64, ADR 0103).
- The document records what the console wrote and nothing else. A value the host
  was started with -- `--model`, `--base-url` -- is the configuration a run uses,
  but the Models page does not report it as a saved setting, because an operator
  who never opened a provider card should not be shown one they saved (ADR 0103).
  A field it does record is reported back on the card it was written on.
- A document this host cannot read stops it at startup, naming the file and the
  damage rather than its contents. An unreadable settings file is never quietly
  ignored: the Models page would otherwise come back empty and claim it was a
  fresh host.
- Settings changes are accepted only from a loopback client unless
  `--allow-remote` was passed.

## Why it binds to localhost

The console can start runs in the server's workspace, with the server's tools,
so a console reachable from the network is a remote shell with a stylesheet.
`127.0.0.1` is the default for that reason, and `--allow-remote` makes widening
it a deliberate act. Authentication is optional and off by default on loopback:
a host started without it serves every caller that can reach it.

`--allow-remote` now requires tokens unless the operator also passes
`--allow-anonymous-remote`, so a network-bound host refuses an unauthenticated
caller before any route answers, and a host that must require tokens and holds
none — or cannot keep an audit trail — refuses to start. A deployment mints its
tokens with `zenforge token create`; a person signs in once at `/auth`, and an
API client presents `Authorization: Bearer <token>`. Whenever authentication is
required, every admission decision is appended to the audit trail (`--audit-log`,
by default `audit.jsonl` in the host configuration directory). The console still
has no user accounts of its own, and a token attributes a run rather than
partitioning the console's data plane. See
[ADR 0141](adr/0141-a-deployed-host-authenticates-its-callers.md).

## Related

- [HTTP harness](server-http-guide.md) — the run API the console is one more
  client of, including the detached model that lets a run outlive the page.
- [Checkpoint and resume](checkpoint-resume-guide.md) — why a page refresh
  re-reads history rather than losing it.
- [Signed webhook](adr/0070-a-signed-webhook-starts-a-run.md) — the endpoint
  `--webhook-secret` enables.
- [Approvals](approval-guide.md) — the approval mechanism, for tools that need
  one.

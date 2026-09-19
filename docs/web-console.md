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
| `--allow-remote` | off | Permit binding a non-loopback address. |
| `--run-timeout` | server default | Bound on a run started from the console. |
| `--webhook-secret` | unset | When set, the signed webhook endpoint (ADR 0070) is served as well. |

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

- A page-set key is held in memory for the lifetime of the server process. It is
  never written to disk, never logged, and never returned by the API: the
  settings endpoint reports only whether a key is set.
- An empty key field means "keep the key you already have", so a model can be
  changed without re-typing a credential the page was never shown.
- Command-line and environment values are the defaults; a page-set value
  overrides one until the server restarts.
- Settings changes are accepted only from a loopback client unless
  `--allow-remote` was passed.

## Why it binds to localhost

The console can start runs in the server's workspace, with the server's tools,
so a console reachable from the network is a remote shell with a stylesheet.
`127.0.0.1` is the default for that reason, and `--allow-remote` makes widening
it a deliberate act. The console has no user accounts and no authentication;
it is a local operator's tool.

## Related

- [HTTP harness](server-http-guide.md) — the run API the console is one more
  client of, including the detached model that lets a run outlive the page.
- [Checkpoint and resume](checkpoint-resume-guide.md) — why a page refresh
  re-reads history rather than losing it.
- [Signed webhook](adr/0070-a-signed-webhook-starts-a-run.md) — the endpoint
  `--webhook-secret` enables.
- [Approvals](approval-guide.md) — the approval mechanism, for tools that need
  one.

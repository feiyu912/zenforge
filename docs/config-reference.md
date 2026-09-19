# Config Reference

ZenForge CLI can load JSON config with `--config`.

```bash
zenforge init
zenforge run --config zenforge.json "Analyze this repo"
zenforge resume --config zenforge.json run_123
zenforge events --config zenforge.json run_123
zenforge runs --config zenforge.json
```

`zenforge init` creates `zenforge.json` and `.zenforge/runs`.

## Example

```json
{
  "model": {
    "provider": "openai",
    "name": "gpt-4.1",
    "apiKeyEnv": "OPENAI_API_KEY",
    "retry": {
      "enabled": true,
      "maxRetries": 5,
      "initialDelay": "500ms",
      "maxDelay": "10s",
      "jitter": 0.1,
      "streamIdleTimeout": "5m0s"
    }
  },
  "agent": {
    "instructions": "You are a senior Go backend engineer. Be concise, careful, and use tools when helpful.",
    "maxSteps": 20,
    "mode": "plan_execute",
    "environmentContext": true,
    "projectInstructions": {
      "enabled": true
    }
  },
  "workspace": {
    "root": ".",
    "maxReadBytes": 1000000,
    "maxWriteBytes": 1000000
  },
  "shell": {
    "enabled": true,
    "workingDir": ".",
    "allow": [
      "go test ./...",
      "go vet ./...",
      "grep",
      "find"
    ],
    "timeout": "30s",
    "maxOutputBytes": 256000
  },
  "approval": {
    "mode": "prompt"
  },
  "web": {
    "enabled": false,
    "maxResults": 8,
    "maxQueries": 4,
    "maxBodyChars": 100000
  },
  "checkpoint": {
    "type": "jsonl",
    "path": ".zenforge/runs"
  },
  "mcpServers": {}
}
```

For SQLite local storage:

```json
{
  "checkpoint": {
    "type": "sqlite",
    "path": ".zenforge/runs.db"
  }
}
```

## Fields

- `model.name`: OpenAI-compatible model name.
- `model.provider`: `openai` or `anthropic`. Invalid values make config
  loading fail before API key lookup or runtime setup.
- `model.apiKeyEnv`: environment variable containing the API key.
- `model.baseUrl`: optional provider API base URL. Keep `model.provider` as
  `openai` for OpenAI-compatible Chat Completions endpoints and `anthropic`
  for Anthropic-compatible Messages endpoints. Vendor-specific endpoints such
  as MiniMax should use the matching protocol adapter plus `model.baseUrl`
  instead of a new provider name.
- `model.contextWindow`: model context window in tokens. When positive, the
  agent compacts conversation history before a model call whose estimated
  request exceeds 80% of the window: oversized tool results are pruned
  first, then shadowed older messages are summarized by the configured
  provider and replaced with a summary message. Even when unset, a provider
  context-overflow rejection triggers the same compaction and one retried
  attempt. Also settable with the `--context-window` flag. See the
  [Compaction Guide](compaction-guide.md) for the full lifecycle and event
  vocabulary.
- `model.retry`: model-call retry policy. Retryable failures are rate
  limits (HTTP 429), server errors (5xx), request/stream timeouts,
  transport errors, stalled streams, and empty responses; authentication,
  quota, and malformed-request failures are never retried, and context
  overflow is owned by compaction instead. Backoff is
  `initialDelay * 2^n` capped at `maxDelay` with symmetric `jitter`; a
  provider `Retry-After` header raises the delay (capped internally). Each
  retry supersedes the failed durable attempt and emits a `model.retry`
  event before the wait, so resumed runs keep the full provenance.
  - `model.retry.enabled`: `false` disables retries entirely.
  - `model.retry.maxRetries`: retries after the first attempt; `0` also
    disables retries. Negative values make config loading fail.
  - `model.retry.initialDelay`: Go duration before the first retry
    (default `500ms`).
  - `model.retry.maxDelay`: backoff cap (default `10s`).
  - `model.retry.jitter`: symmetric jitter fraction in `[0, 1)`
    (default `0.1`).
  - `model.retry.streamIdleTimeout`: Go duration a model stream may
    produce no events before it is treated as a retryable timeout
    (default `5m0s`; `0s` disables the watchdog).
- `agent.instructions`: system instructions for the harness. Inserted
  verbatim (no variable interpolation), because discovered and configured
  instruction text is not treated as a template.
- `agent.personaPrefix` and `agent.personaSuffix`: deployment-authored
  sections placed before and after first-party guidance. Both support strict
  `{{variable}}` references; a malformed or unknown reference fails the run at
  its next model boundary before any model request. An empty string omits the
  section.
- `agent.promptVariables`: values for `{{variable}}` references in the persona
  sections. Built-ins `workspace` (working directory) and `platform`
  (`GOOS/GOARCH`) are available unless overridden here. Variable names must
  match `^[a-z][a-z0-9_]*$`.
- `agent.sessionTitle`: explicit session title stored as log-only run
  metadata (the `session.title` event and `zenforge.session_title` run-state
  key). Control, escape, and bidirectional characters are stripped and the
  title is capped at 200 bytes; an explicit title that normalizes to empty
  makes the run fail. When unset, the first eight words of the task input
  become a deterministic fallback title. Titles never enter the model prompt.
- `agent.maxSteps`: maximum model/tool loop steps. Negative values make config
  loading fail.
- `agent.mode`: platform-compatible execution preset: `react`, `oneshot`, or
  `plan_execute`. `oneshot` caps the normal model/tool loop at two rounds and
  then forces a no-tool final answer when needed.
- `agent.planning`: `disabled`, `enabled`, `plan_execute`, or boolean. Invalid
  values make config loading fail instead of disabling planning silently. This
  remains a compatibility field; do not set it together with `agent.mode`.
- `agent.environmentContext`: inject an `<environment_context>` system
  snapshot (working directory, platform, date, execution mode, tool list) at
  run start. The snapshot is frozen into durable run state, so a resumed run
  replays the exact context it started with.
- `agent.projectInstructions`: hierarchical instruction-file discovery,
  modeled on codex AGENTS.md and DSH agent-instructions. From the nearest
  project root marker down to the workspace root, the first existing
  candidate file per directory is merged broad-to-specific under a byte
  budget and injected as a system message; more specific files take
  precedence, and broader files are dropped whole before specific ones are
  truncated.
  - `agent.projectInstructions.enabled`: `false` disables discovery.
  - `agent.projectInstructions.fileNames`: per-directory candidates in
    precedence order (default `AGENTS.override.md`, `AGENTS.md`,
    `ZENFORGE.md`, `CLAUDE.md`). Entries must be plain filenames; path
    syntax makes config loading fail before any filesystem probe.
  - `agent.projectInstructions.rootMarkers`: names marking the project root
    (default `.git`). An empty list disables traversal above the workspace
    root.
  - `agent.projectInstructions.globalPath`: optional user-level instruction
    file, the broadest scope. Defaults to `~/.zenforge/AGENTS.md`; a
    missing global file is skipped silently.
  - `agent.projectInstructions.maxBytes`: merged budget in bytes
    (default 32768).
  Discovery warnings (skipped, truncated, or omitted files) are reported in
  the `instructions.loaded` event payload.
- `workspace.root`: local workspace root.
- CLI workspace writes require a fresh `workspace_read` snapshot before
  overwriting an existing file, and require an observed absence (a
  `workspace_read` that reported not-found) before creating a new one. A
  write to a path the run has never looked at is refused with
  `workspace read snapshot required`.
- `workspace.maxReadBytes` and `workspace.maxWriteBytes`: local workspace byte
  limits. Negative values make config loading fail.
- `workspace.readRoots` and `workspace.writeRoots`: optional
  workspace-relative roots for file tools. Empty lists keep the default
  root-bounded behavior. When roots are configured, file operations outside
  those roots are denied when `approval.mode` is `never`, or returned as
  approval requests when approval is enabled.
- `shell.enabled`: enables the local shell tool.
- `shell.workingDir`: working directory for shell commands.
- `shell.allow`: allowlisted shell command prefixes.
- `shell.timeout`: Go duration string, for example `30s`. Invalid durations
  make config loading fail instead of falling back silently.
- `shell.maxOutputBytes`: output cap for shell command output. Negative values
  make config loading fail.
- `approval.mode`: `prompt`, `always`, or `never`. Invalid values make config
  loading fail before the runtime is built.
- `checkpoint.type`: `jsonl` or `sqlite`. Invalid values make config loading
  fail before opening stores.
- `checkpoint.path`: JSONL event/checkpoint directory, or SQLite database file.
- `mcpServers`: MCP servers this client starts over stdio, keyed by server
  name. Each entry is exposed to the model as `mcp__<name>__<tool>`. The
  section is validated before anything is started: an entry without a
  `command`, a server name with surrounding whitespace or containing `__`
  (the namespace separator), or an environment name that cannot be passed to
  a process makes config loading fail.
  - `mcpServers.<name>.command`: the executable to start. Required.
  - `mcpServers.<name>.args`: argument list, passed through unchanged.
  - `mcpServers.<name>.env`: extra environment entries for the server
    process. The ambient environment is scrubbed of credential-shaped names
    (`KEY`, `PASSWORD`, `SECRET`, `TOKEN`) before it reaches a server, so a
    credential a server genuinely needs belongs here. Values redact in every
    formatting path and stay transparent in JSON.
  - `mcpServers.<name>.deferred`: keep this server's tools out of the model's
    initial tool list until `tool_search` activates them. Any server with
    `deferred: true` is what registers `tool_search`.

  A server that cannot start, or whose handshake does not answer within 30
  seconds, fails the command rather than leaving the run without the tools the
  file asked for. A call to a tool the server did not declare `readOnlyHint`
  goes through the approval channel, and one remote call is bounded by a
  60-second budget. See the [MCP Adapter Guide](mcp-adapter-guide.md).

Flags override values loaded from the config file.

Cross-run approval grant storage is currently a Go SDK configuration surface,
not a CLI JSON field. Embedded hosts configure `Config.ApprovalGrants`,
`Config.ApprovalNamespace`, and `Config.ApprovalGrantTTL`, or override the
namespace per run with `Task.ApprovalNamespace`. See the Approval Guide for the
exact-match and fail-closed semantics.

Shared approval inboxes are also SDK/server configuration. Embedded hosts can
use `approval.NewStoreBroker` with `approval/sqlite.OpenInbox` and pass the
broker as `Config.Approval` or `harnesshttp.RuntimeOptions.ApprovalInbox`. The
inbox store is caller-owned and must be closed by the host; it is not loaded
from CLI JSON.

MiniMax Anthropic-compatible config example:

```json
{
  "model": {
    "provider": "anthropic",
    "name": "MiniMax-M3",
    "apiKeyEnv": "ANTHROPIC_API_KEY",
    "baseUrl": "https://api.minimax.io/anthropic"
  }
}
```

## Layers, profiles, and requirements

Configuration is composed from ordered layers. A leaf key set by a
higher-precedence layer wins; objects merge key by key and every other
value replaces the previous one.

| Precedence | Layer | Path |
| ---: | --- | --- |
| 10 | system | `/etc/zenforge/zenforge.json` |
| 20 | user | `$ZENFORGE_CONFIG_DIR/zenforge.json`, `$XDG_CONFIG_HOME/zenforge/zenforge.json`, or `~/.config/zenforge/zenforge.json` |
| 21 | profile | overrides from `profiles.<name>` in whichever layer defines it, applied directly above that layer |
| 25 | project | `.zenforge/zenforge.json` found by walking up from the workspace |
| 27 | file | the path passed to `--config` (must exist) |
| 30 | flags | command-line flags |

`--ignore-user-config` skips the system and user layers. `--profile <name>`
selects a profile; an unknown name is a usage error that lists the available
profiles instead of silently running with the base configuration.
`--strict-config` rejects any field this version does not recognize and names
the layer that set it.

The layers above are all inputs. `zenforge serve` also *writes* one file beside
them -- `console-settings.json` in the same directory as the user layer, or the
path given to `--settings-file` -- which holds the endpoint, model, inline
credential, declared provider profiles and settings revisions the console wrote
(ADR 0102). It is not a layer: no other command reads it, it does not compose
into the table above, and a field the document names outranks the seed these
layers produce. It is written `0600`, because it holds the key.

A profile is an ordinary config fragment::

```json
{
  "model": { "name": "gpt-5" },
  "profiles": {
    "ci": { "agent": { "maxSteps": 8 }, "approval": { "policy": "never" } }
  }
}
```

```bash
zenforge exec --profile ci "Run the test suite"
```

### Managed requirements

`--requirements <file>` (or the host-wide `/etc/zenforge/requirements.json`
when present) applies a managed constraints document after every other layer:

```json
{
  "allowed": { "model.provider": ["openai"], "approval.policy": ["never", "on_request"] },
  "enforce": { "shell.enabled": false }
}
```

- `allowed` rejects a value outside the permitted set. The error names the key,
  the rejected value, the allowed set, and the requirements file.
- `enforce` overwrites the value regardless of any other layer, so an
  administrator can pin a policy that a user config cannot loosen.

Because a dotted path is used, `shell.enabled` addresses
`{"shell": {"enabled": ...}}`. An explicitly passed `--requirements` file must
exist; the host-wide default path is optional.

### Web tools

`web_search` and `web_fetch` stay unregistered unless the deployment opts
in. Setting `web.enabled` to `true` registers `web_fetch`; setting
`web.searchEndpoint` registers `web_search` as well (and implies
`enabled`).

```json
{
  "web": {
    "enabled": true,
    "searchEndpoint": "https://api.search.example/search?q={query}&count={limit}",
    "searchApiKeyEnv": "SEARCH_API_KEY",
    "maxResults": 8,
    "maxQueries": 4,
    "maxBodyChars": 100000,
    "requireApproval": false
  }
}
```

`searchEndpoint` may contain `{query}` and `{limit}` placeholders; without
them the query is appended as `q` and `count`. The response may be a
generic `{"results": [{"title", "url", "description"}]}` object or Brave's
`{"web": {"results": [...]}}` shape. `searchApiKey` is an inline secret
with the same redaction as `model.apiKey`; `searchApiKeyEnv` is preferred.
The remaining keys bound the tool: `maxResults` caps returned sources
(default 8), `maxQueries` caps queries per call (default 4), and
`maxBodyChars` caps the rendered page text (default 100000).

`web_fetch` refuses to connect to a non-public address: the hostname is
resolved once, every answer must be public unicast, and the connection is
pinned to the validated addresses so a second resolution cannot reach a
private service. Redirects are followed only within the same origin, up to
five hops. `web.allowPrivate` disables that requirement for local
development — it makes loopback, link-local, and private ranges reachable
and should not be enabled in a shared deployment. `web.requireApproval`
routes every search and fetch through the approval broker.

### Sandbox

`shell.sandbox` confines the shell tool. `backend` is `none` (the default),
`seatbelt` (macOS), `bwrap` (Linux), or `docker`; `roots` lists the writable
roots (the shell working directory when empty); `allowNetwork` grants
network access; `restricted` starts bubblewrap from an empty root plus the
approved read roots instead of a read-only host root; `image` selects the
container image; `timeout` bounds one sandboxed command (defaulting to
`shell.timeout`); and `protectedNames` selects the basenames pinned
read-only inside writable roots (`.git` and `.zenforge` by default). Since
the default configuration does not set `shell.sandbox`, the block is
omitted from the default file above and the shell stays local.

## Secrets

`model.apiKey` holds an inline key. Every formatting path (`%v`, `%s`, `%#v`,
slog) renders `<redacted>`, so an accidental log line cannot leak it; JSON
serialization stays transparent so the config file round-trips unchanged. Use
`model.apiKeyEnv` or `--api-key-env` to keep the secret out of the file
entirely; `--api-key` exists for one-off invocations.

## Current Limitations

The CLI accepts JSON config only. `zenforge init` creates `zenforge.json`;
YAML config files are not part of the current interface.

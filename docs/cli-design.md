# CLI Design

ZenForge CLI is the fastest way to feel the runtime.

## Commands

```text
zenforge run
zenforge exec
zenforge code
zenforge resume
zenforge events
zenforge runs
zenforge init
zenforge version
```

## Tools

The default tool set is `workspace_read`, `workspace_list`, `workspace_glob`,
`workspace_grep`, `workspace_write`, `workspace_edit`, `apply_patch` (codex
envelope: add, update, move, delete in one call), `shell` (unless `--no-shell`),
`todo` tools, `ask_user`, `get_context_remaining`, and `present`. `tool_search`
is added only when at least one configured tool defers its definition, and
`web_search`/`web_fetch` only when `web.enabled` is true or
`web.searchEndpoint` is set (a search endpoint also registers
`web_search`).

## Configuration layers

Configuration is composed from ordered layers (system, user, profile, project,
`--config`, flags) with managed requirements applied last; see
[`config-reference.md`](config-reference.md) for the precedence table and the
`allowed`/`enforce` document. The flags that shape layering are:

- `--profile <name>` selects a `profiles.<name>` fragment (unknown names list
  what is available).
- `--requirements <file>` applies a managed constraints document; the
  host-wide `/etc/zenforge/requirements.json` is used when present.
- `--strict-config` rejects fields this version does not recognize, naming the
  layer that set them.
- `--ignore-user-config` skips the system and user layers.

## `zenforge exec`

`exec` is the headless entry point for scripts and editors. It shares every
option with `run` and adds machine output:

```bash
# JSONL event stream on stdout
zenforge exec --json "Summarize the failing tests"

# Constrain the final response to a JSON Schema and keep the last message
zenforge exec --output-schema answer.schema.json -o answer.json "Extract the risks"

# Read the prompt from stdin
echo "Summarize this diff" | zenforge exec -
```

- `--json` prints one `zenforge.Event` JSON object per line, the same record
  `zenforge events --json` prints.
- `--output-schema <file>` requires a non-empty JSON object; the schema is
  forwarded to the provider as a strict `json_schema` response format. A
  provider that cannot enforce a schema (Anthropic Messages API) fails with
  `model.ErrUnsupportedOutputSchema` instead of returning unconstrained text.
- `-o`, `--output-last-message <file>` writes the final assistant message.
- The prompt comes from the arguments, from `-`, or from piped stdin (1 MiB
  cap). Missing or blank input is a usage error (`exit 2`).

## `zenforge run`

```bash
zenforge run "Analyze this repo"
```

Initial implementation:

```bash
OPENAI_API_KEY=... zenforge run \
  --workspace . \
  --checkpoint-dir .zenforge/runs \
  --mode plan_execute \
  --approve prompt \
  "Analyze this repo"
```

`--mode react|oneshot|plan_execute` is the primary execution-preset flag.
`--planning` remains for compatibility with earlier ZenForge configs; callers
must not pass both flags in one command.

Behavior:

- loads config;
- creates run ID;
- starts event stream;
- renders model deltas;
- renders tool calls compactly;
- renders todos;
- prompts for approval if configured;
- writes checkpoints and event log.

## `zenforge code`

```bash
zenforge code ./repo "Analyze failures and implement the fix"
```

`code` uses the same model, mode, approval, tool, event, and checkpoint
assembly as `run`. Its first positional argument is authoritative: ZenForge
resolves it to a real directory and uses that directory as both the local
workspace root and shell working directory. A configured or flagged
`workspace.root` cannot redirect this command away from the selected
repository. Remaining positional arguments form the task input.

## `zenforge resume`

```bash
zenforge resume run_123
```

Initial implementation uses the same model/tool assembly flags as `run` and
loads checkpoint state from `--checkpoint-dir`.

Behavior:

- loads checkpoint;
- prints resume phase;
- continues if phase is supported;
- fails clearly if unsupported.

## `zenforge events`

```bash
zenforge events run_123
```

Initial implementation reads JSONL events from `--checkpoint-dir` and can print
compact timeline text or raw JSON with `--json`.

Behavior:

- reads event log;
- prints compact timeline;
- optional JSON output later.

## `zenforge runs`

```bash
zenforge runs
```

Reads latest checkpoints from `--checkpoint-dir` and prints run ID, phase,
status, step, and save time. Use `--json` for machine-readable summaries.

Behavior:

- lists local durable runs;
- sorts newest checkpoint first;
- skips directories without a valid latest checkpoint.

## `zenforge init`

```bash
zenforge init
```

Creates:

```text
zenforge.json
.zenforge/
```

The generated config is JSON. YAML config files are not accepted.

## Rendering

Tool call:

```text
tool workspace_grep {"pattern":"TODO","path":"."}
```

Todo update:

```text
todos
  [done] Inspect project structure
  [in_progress] Review tool runtime
  [pending] Draft plan
```

Approval:

```text
Approval required: shell command
Risk: high
Command: rm -rf build

1. Reject
2. Approve once
3. Approve for this run
```

## Exit Codes

- `0`: success;
- `1`: runtime error;
- `2`: invalid config/usage;
- `3`: run cancelled;
- `4`: approval rejected;
- `5`: unsupported resume state.

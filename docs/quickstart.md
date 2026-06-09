# Quickstart

This quickstart uses the CLI because it exercises the full harness path:
model adapter, tools, checkpoints, event log, planning, shell policy, and
approval.

## 1. Initialize Config

```bash
go run ./cmd/zenforge init
```

This creates:

```text
zenforge.json
.zenforge/runs/
```

Edit `zenforge.json` if you want to change model, workspace, shell allowlist,
approval mode, or checkpoint path.

The default store is JSONL under `.zenforge/runs`. For one SQLite database file,
set `checkpoint.type` to `sqlite` and `checkpoint.path` to
`.zenforge/runs.db`.

## 2. Set API Key

```bash
export OPENAI_API_KEY=...
```

The default config reads the key from `OPENAI_API_KEY`.

## 3. Run An Agent

```bash
go run ./cmd/zenforge run --config zenforge.json "Analyze this repo"
```

The CLI streams model text, tool calls, todo updates, approval requests, and the
final answer.

Workspace writes are conservative by default. The CLI records file metadata when
`workspace_read` succeeds, and `workspace_write` requires a fresh read snapshot
before overwriting an existing file.

## 4. Inspect Events

List known runs:

```bash
go run ./cmd/zenforge runs --config zenforge.json
```

Copy a run ID, then inspect its events:

```bash
go run ./cmd/zenforge events --config zenforge.json run_123
```

For raw JSON events:

```bash
go run ./cmd/zenforge events --config zenforge.json --json run_123
```

## 5. Resume

```bash
go run ./cmd/zenforge resume --config zenforge.json run_123
```

Resume uses the same checkpoint path and runtime assembly as `run`.

## Approval Modes

```bash
go run ./cmd/zenforge run --approve prompt "Run useful checks"
go run ./cmd/zenforge run --approve always "Run useful checks"
go run ./cmd/zenforge run --approve never "Run useful checks"
```

- `prompt`: ask before risky/non-allowlisted shell commands.
- `always`: approve approval requests automatically.
- `never`: reject approval requests automatically.

## Examples

```bash
go run ./examples/sdk-embedded-agent
OPENAI_API_KEY=... go run ./examples/simple-tool-agent
OPENAI_API_KEY=... go run ./examples/repo-refactor-agent
OPENAI_API_KEY=... go run ./examples/code-review-agent
```

The SDK embedded example uses a local scripted model and runs without an API
key. The other examples use an OpenAI-compatible provider.

Examples honor:

- `OPENAI_MODEL`
- `OPENAI_BASE_URL`
- `ZENFORGE_WORKSPACE`
- `ZENFORGE_RUN_DIR`

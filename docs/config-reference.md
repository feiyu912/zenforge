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
    "apiKeyEnv": "OPENAI_API_KEY"
  },
  "agent": {
    "instructions": "You are a senior Go backend engineer. Be concise, careful, and use tools when helpful.",
    "maxSteps": 20,
    "planning": "plan_execute"
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
  "checkpoint": {
    "type": "jsonl",
    "path": ".zenforge/runs"
  }
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
- `model.baseUrl`: optional provider API base URL.
- `agent.instructions`: system instructions for the harness.
- `agent.maxSteps`: maximum model/tool loop steps. Negative values make config
  loading fail.
- `agent.planning`: `disabled`, `enabled`, `plan_execute`, or boolean. Invalid
  values make config loading fail instead of disabling planning silently.
- `workspace.root`: local workspace root.
- CLI workspace writes require a fresh `workspace_read` snapshot before
  overwriting an existing file.
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

Flags override values loaded from the config file.

## Current Limitations

The MVP CLI config loader accepts JSON. The design docs use YAML as the target
shape, but YAML support is intentionally left for a later pass to avoid adding a
dependency before the public config surface settles.

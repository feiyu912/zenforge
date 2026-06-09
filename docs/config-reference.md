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
- `model.provider`: `openai` or `anthropic`.
- `model.apiKeyEnv`: environment variable containing the API key.
- `model.baseUrl`: optional provider API base URL.
- `agent.instructions`: system instructions for the harness.
- `agent.maxSteps`: maximum model/tool loop steps.
- `agent.planning`: `disabled`, `enabled`, `plan_execute`, or boolean.
- `workspace.root`: local workspace root.
- CLI workspace writes require a fresh `workspace_read` snapshot before
  overwriting an existing file.
- `shell.enabled`: enables the local shell tool.
- `shell.workingDir`: working directory for shell commands.
- `shell.allow`: allowlisted shell command prefixes.
- `shell.timeout`: Go duration string, for example `30s`.
- `shell.maxOutputBytes`: output cap for shell command output.
- `approval.mode`: `prompt`, `always`, or `never`.
- `checkpoint.type`: `jsonl` or `sqlite`.
- `checkpoint.path`: JSONL event/checkpoint directory, or SQLite database file.

Flags override values loaded from the config file.

## Current Limitations

The MVP CLI config loader accepts JSON. The design docs use YAML as the target
shape, but YAML support is intentionally left for a later pass to avoid adding a
dependency before the public config surface settles.

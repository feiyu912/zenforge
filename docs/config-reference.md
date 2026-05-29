# Config Reference

ZenForge CLI can load JSON config with `--config`.

```bash
zenforge init
zenforge run --config zenforge.json "Analyze this repo"
zenforge resume --config zenforge.json run_123
zenforge events --config zenforge.json run_123
```

`zenforge init` creates `zenforge.json` and `.zenforge/runs`.

## Example

```json
{
  "model": {
    "provider": "openai",
    "name": "gpt-4.1",
    "apiKeyEnv": "OPENAI_API_KEY",
    "baseUrl": ""
  },
  "agent": {
    "instructions": "You are a senior Go backend engineer.",
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
    "allow": ["go test ./...", "go vet ./...", "grep", "find"],
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

## Fields

- `model.name`: OpenAI-compatible model name.
- `model.apiKeyEnv`: environment variable containing the API key.
- `model.baseUrl`: optional OpenAI-compatible API base URL.
- `agent.instructions`: system instructions for the harness.
- `agent.maxSteps`: maximum model/tool loop steps.
- `agent.planning`: `disabled`, `enabled`, `plan_execute`, or boolean.
- `workspace.root`: local workspace root.
- `shell.enabled`: enables the local shell tool.
- `shell.workingDir`: working directory for shell commands.
- `shell.allow`: allowlisted shell command prefixes.
- `shell.timeout`: Go duration string, for example `30s`.
- `shell.maxOutputBytes`: output cap for shell command output.
- `approval.mode`: `prompt`, `always`, or `never`.
- `checkpoint.path`: JSONL event/checkpoint directory.

Flags override values loaded from the config file.

## Current Limitations

The MVP CLI config loader accepts JSON. The design docs use YAML as the target
shape, but YAML support is intentionally left for a later pass to avoid adding a
dependency before the public config surface settles.

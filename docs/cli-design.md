# CLI Design

ZenForge CLI is the fastest way to feel the runtime.

## Commands

```text
zenforge run
zenforge resume
zenforge events
zenforge init
zenforge version
```

## `zenforge run`

```bash
zenforge run "Analyze this repo"
```

Behavior:

- loads config;
- creates run ID;
- starts event stream;
- renders model deltas;
- renders tool calls compactly;
- renders todos;
- prompts for approval if configured;
- writes checkpoints and event log.

## `zenforge resume`

```bash
zenforge resume run_123
```

Behavior:

- loads checkpoint;
- prints resume phase;
- continues if phase is supported;
- fails clearly if unsupported.

## `zenforge events`

```bash
zenforge events run_123
```

Behavior:

- reads event log;
- prints compact timeline;
- optional JSON output later.

## `zenforge init`

```bash
zenforge init
```

Creates:

```text
zenforge.yml
.zenforge/
```

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


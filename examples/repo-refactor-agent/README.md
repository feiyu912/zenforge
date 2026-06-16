# Repo Refactor Agent

Flagship MVP example. It uses:

- OpenAI-compatible model adapter
- plan/execute/summary preset
- todo tools
- workspace read/list/grep/write tools
- shell tool with an allowlist
- JSONL events and checkpoints

Run from the repository root:

```bash
OPENAI_API_KEY=... go run ./examples/repo-refactor-agent
```

Optional environment:

```bash
ZENFORGE_WORKSPACE=.
ZENFORGE_RUN_DIR=.zenforge/runs
OPENAI_MODEL=gpt-4.1
OPENAI_BASE_URL=https://api.openai.com/v1
```

The example can read the configured workspace and only allows writes under
`.zenforge/generated`. It also enables read-before-write snapshots and sets
`MaxWriteBytes: 1`, so it can inspect the repo but will not write meaningful
file contents by default.

# Code Review Agent

Focused code-review workflow. It can read and grep the workspace, and can run
allowlisted commands like `go test ./...`.

```bash
OPENAI_API_KEY=... go run ./examples/code-review-agent
```

Optional environment:

```bash
ZENFORGE_WORKSPACE=.
ZENFORGE_RUN_DIR=.zenforge/runs
OPENAI_MODEL=gpt-4.1
OPENAI_BASE_URL=https://api.openai.com/v1
```

The example is read-only by default for file writes.

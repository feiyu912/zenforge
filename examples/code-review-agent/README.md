# Code Review Agent

Focused code-review workflow. It can read and grep the workspace, and can run
allowlisted commands like `go test ./...`.

Unknown shell commands are routed through the CLI approval broker before they
run. File writes are limited to `.zenforge/generated`, require approval outside
the configured root policy, and are also constrained by a one-byte write limit
plus read-before-write snapshots, so the example is effectively read-only for
normal code review use.

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

Approval prompts are printed to stderr and read from stdin. Choose the numbered
option shown by the prompt to approve or reject the requested command.

# Release Checklist

Use this checklist before tagging an MVP or V0.1 release.

## Local Verification

```bash
env GOCACHE=/private/tmp/agent-platform-go-build-cache go test ./...
go test ./examples/...
rg -n "agent-platform|ZenMind" --glob "*.go" .
```

Expected results:

- all tests pass;
- examples compile;
- local Markdown doc links resolve through `docs.TestMarkdownLinksResolve`;
- platform-boundary search returns no Go-source matches.

## CLI Smoke

```bash
go run ./cmd/zenforge init --config /tmp/zenforge.json
OPENAI_API_KEY=... go run ./cmd/zenforge run --config /tmp/zenforge.json "Analyze this repo"
go run ./cmd/zenforge runs --config /tmp/zenforge.json
go run ./cmd/zenforge events --config /tmp/zenforge.json run_123
go run ./cmd/zenforge resume --config /tmp/zenforge.json run_123
```

For approval behavior:

```bash
go run ./cmd/zenforge run --approve prompt "Run a useful shell check"
go run ./cmd/zenforge run --approve always "Run a useful shell check"
go run ./cmd/zenforge run --approve never "Run a useful shell check"
```

## Repository Checks

- README quickstart is current.
- `VERSION`, `cli.Version`, and release notes agree on the chosen version.
- `docs/quickstart.md` is current.
- `docs/config-reference.md` matches `zenforge init` output.
- `docs/limitations.md` mentions known resume, shell, memory, MCP, tracing,
  config, sub-agent, and sandbox limitations.
- Checkpoint/run-state schema versions and the flattened event contract are
  documented.
- `docs/mvp-validation.md` maps acceptance items to evidence.
- `docs/release-notes-v0.1.md` summarizes highlights and limitations.
- GitHub Actions CI is green.

## Tagging

- Choose version.
- Update changelog and release notes.
- Include limitations in release notes.
- Push tag only after CI is green.

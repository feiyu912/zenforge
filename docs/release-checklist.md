# Release Checklist

Use this checklist before tagging an MVP or V0.1 release.

## Local Verification

```bash
env GOTOOLCHAIN=local go test ./...
env GOTOOLCHAIN=local go test ./docs/... ./cli ./adapters/zenmind
env GOTOOLCHAIN=local go test ./examples/...
(cd integration/consumer && env GOTOOLCHAIN=local go test -race ./... && env GOTOOLCHAIN=local go vet ./...)
(cd integration/consumer && ZENFORGE_DOCKER_INTEGRATION=1 env GOTOOLCHAIN=local go test -run '^TestDockerAdapterRunsInsideContainerWithWorkspaceMount$' -v)
rg -n '"[^"[:space:]]*agent-platform[^"[:space:]]*"' --glob "*.go" .
```

Expected results:

- all tests pass;
- examples compile;
- the independent consumer module passes its model/tool/HITL/sandbox contract;
- the independent consumer proves descriptor-only first context, `load_skill`
  body/digest/safe provenance, then typed tool/HITL/sandbox continuation;
- the gated Docker test proves commands execute in Linux with the mounted
  workspace;
- local Markdown doc links resolve through `docs.TestMarkdownLinksResolve`;
- platform-boundary search returns no platform module import strings;
  `adapters/zenmind` comments and fixtures may retain protocol provenance.
- `docs.TestGoSourceKeepsPlatformBoundary` rejects brand coupling outside
  `adapters/zenmind` and rejects platform-module or `internal` imports inside
  that adapter.

ZenMind adapter evidence must include these pinned fixtures:

- `adapters/zenmind/testdata/platform/catalog_agent.json`
- `adapters/zenmind/testdata/platform/query_session.json`
- `adapters/zenmind/testdata/platform/lifecycle_content.jsonl`
- `adapters/zenmind/testdata/platform/lifecycle_tool.jsonl`
- `adapters/zenmind/testdata/platform/approval_roundtrip.jsonl`
- `adapters/zenmind/testdata/platform/chat_event_line.jsonl`
- `adapters/zenmind/testdata/platform/manifest.json`

They are exercised by `env GOTOOLCHAIN=local go test ./adapters/zenmind` and
the manifest verifies source files and SHA-256 hashes from
`agent-platform@1893edb5`. Keep this repository evidence distinct from the
downstream engine/selector/HTTP/SSE/WS/approval/attach/fallback tests on
`agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3`. Confirm that no
release claim treats the unmerged branch as platform `main`, or either suite as
complete Chat Storage V3.1 or real Container Hub acceptance. Use Go 1.26.x only.

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
- `examples/harness-agent` ships a valid `SKILL.md` and documents
  `-skill-root`/`ZENFORGE_SKILL_ROOT`.
- `VERSION`, `cli.Version`, and release notes agree on the chosen version.
- `docs/quickstart.md` is current.
- `docs/config-reference.md` matches `zenforge init` output.
- `docs/limitations.md` mentions known resume, shell, memory, MCP, tracing,
  config, sub-agent, sandbox, and Agent Skills limitations.
- Agent Skills claims stay limited to validated catalogs and progressive
  disclosure; do not claim completed marketplace install/update, entitlement,
  signatures, or platform UX/API work.
- Checkpoint/run-state schema versions and the flattened event contract are
  documented.
- `docs/mvp-validation.md` maps acceptance items to evidence.
- ZenMind platform fixtures retain source commit/file provenance and their
  golden tests pass.
- `docs/release-notes-v0.1.md` summarizes highlights and limitations.
- GitHub Actions CI is green.

## Tagging

- Choose version.
- Update changelog and release notes.
- Include limitations in release notes.
- Push tag only after CI is green.

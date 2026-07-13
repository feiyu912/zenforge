# HTTP Harness Agent

This is a local, production-shaped ZenForge application. It demonstrates the
complete HTTP lifecycle with a provider selected from the environment, a real
Agent Skill catalog, typed tool, Docker shell sandbox, durable HITL inbox,
SQLite events/checkpoints, and a SQLite detached-run registry.

The server intentionally binds to loopback only. It does **not** include
authentication or tenancy. A production host must add those before exposing
these endpoints outside a trusted local machine.

## Run

```sh
export ZENFORGE_PROVIDER=anthropic # or openai
export ZENFORGE_MODEL=your-model
export ZENFORGE_API_KEY=your-key
export ZENFORGE_BASE_URL=https://your-endpoint.example/v1

go run ./examples/http-harness-agent \
  -workspace . \
  -skill-root examples/harness-agent/skills
```

MiniMax is configured through the protocol-compatible BaseURL it provides. The
key must match the selected OpenAI or Anthropic protocol endpoint.

Start a detached run:

```sh
curl -sS -X POST http://127.0.0.1:8080/runs/start \
  -H 'content-type: application/json' \
  -d '{"runId":"review_1","input":"Load the project skill and inspect this workspace."}'
```

Follow the full durable stream, including tool and approval events:

```sh
curl -N 'http://127.0.0.1:8080/runs/attach?runId=review_1'
```

When the shell needs approval, list requests and submit the request ID shown
there:

```sh
curl -sS 'http://127.0.0.1:8080/approvals?runId=review_1'
curl -sS -X POST http://127.0.0.1:8080/approval \
  -H 'content-type: application/json' \
  -d '{"requestId":"approval_id_here","action":"approve","scope":"once"}'
```

Use `GET /runs`, `GET /runs/status?runId=...`, `POST /runs/resume`, and
`POST` or `DELETE /runs/cancel` for the rest of the detached lifecycle. State
defaults to `.zenforge/http-harness`; use `-data-dir` to move it. Pass
`-recover-stale` only when this trusted process should explicitly resume
expired runs after a crash.

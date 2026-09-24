# Examples

The ZenForge repository ships a small set of self-contained Go programs under
[`examples/`](https://github.com/feiyu912/zenforge/tree/main/examples). Each
one is a single `main.go` that wires up a different slice of the framework, so
you can read them top-to-bottom and copy the bits you need into your own
service.

They all follow the same shape: build a workspace, optionally build a toolset,
construct an agent with `zenforge.New(...)`, and then call `agent.Run` or
`agent.Stream`. The examples grow in capability from a typed tool with no
sandbox, through durable runs and event streaming, up to a flagship plan /
execute / summary workflow with approval gating.

!!! tip "Picking a starting point"
    To test the complete user-owned harness first, start with `harness-agent`.
    To test the durable HTTP lifecycle, start with `http-harness-agent`.
    For individual concepts, read `simple`, `sdk-embedded`, `code-review`,
    then `repo-refactor`.

---

## The three scenario examples

`qa-agent`, `long-task-agent` and `coding-agent` are the examples the acceptance
suite runs end to end. They are written the way a deployment is written — the
provider comes from the environment and the model's words arrive over HTTP —
and their tests point that provider at a scripted OpenAI-compatible endpoint on
loopback (`examples/internal/modelstub`). The adapter, the agent loop, the
tools, the approval broker, the skill catalog and the checkpoint store are all
the real ones; only the model is scripted. So `go test ./examples/...`
reproduces each scenario with no credential, no network and, except where noted,
no Docker.

## qa-agent

Question answering with the three things that make an answer trustworthy: a
filesystem Agent Skill the model loads on demand, a shell it can inspect the
workspace with, and an operator who approves every command before it runs.

```bash
export ZENFORGE_PROVIDER=openai
export ZENFORGE_MODEL=...
export ZENFORGE_API_KEY=...
go run ./examples/qa-agent -question "What does this workspace contain?"
```

The default `-sandbox docker` runs the command in `alpine:3.20` with the
workspace mounted read-only at `/workspace`; `-image` selects another image.
`-sandbox local` runs it on the host instead, which is what CI uses. The skill
root defaults to the `skills` directory beside the example, so a checkout needs
no configuration; `-skill-root` or `ZENFORGE_SKILL_ROOT` select an
application-owned catalog.

The shipped skill (`skills/qa-evidence-lookup/SKILL.md`) tells the model to
answer from observed evidence and name the command that produced each fact. The
transcript is one line per observable step, and the approval prompt is the
numbered CLI prompt on stderr:

```text
skill: loaded qa-evidence-lookup
tool: load_skill
tool: shell
Approval required: Approve shell command
1. Approve
2. Reject
> approval: shell approve
answer: the command printed qa-live-ok
```

`go test ./examples/qa-agent/` proves progressive disclosure on the wire: the
first model request carries the skill's name and description and *not* its body,
the second carries the body only after `load_skill` ran, and the third carries
the approved command's stdout. Setting `ZENFORGE_DOCKER_INTEGRATION=1` runs the
same path inside the container; the Docker CI job runs it.

## long-task-agent

A long task that stops durably in the middle of its work and finishes in a later
process. The agent records each completed step in a workspace task log, and the
step that closes the task out needs a human decision — so it pauses there.

```bash
export ZENFORGE_PROVIDER=openai
export ZENFORGE_MODEL=...
export ZENFORGE_API_KEY=...

# First process: work the task until the decision is needed.
go run ./examples/long-task-agent \
  -task "Audit the workspace step by step, then finalize the report."

# It prints the run id and the resume command, then exits 75 (EX_TEMPFAIL):
#   run: incomplete long_1790...
#   run: resume with: long-task-agent -run-dir .zenforge/long-task \
#     -workspace . -resume long_1790...

# Second process: resume from the checkpoint and answer the pending decision.
go run ./examples/long-task-agent -resume long_1790...
```

The interruption is an **approval pause**, not a step limit, and the README says
why: `MaxSteps` exhaustion is a bound, not a boundary — the runner appends its
tool-use-limit instruction, makes one final model call and ends the run
`completed`, so `Resume` on that checkpoint would only replay the terminal
event. A waiting approval is the one public-API stop that leaves a genuinely
resumable run, so the example configures no approval broker in the first process
(`approval.RequiredResult` + `approval.ErrRequired` from `finalize_task`), and
`-max-steps` stays generous so a real run reaches the decision instead of the
finalization path.

State is durable throughout: `-run-dir` holds the JSONL event log and
checkpoints, and the transcript prints one `checkpoint:` line per durable
boundary with the sequence, the phase and the file a resume reads. The second
process is a fresh program over the same store — it answers with the numbered
CLI option and drains the run to `run: done`:

```text
run: incomplete long_live_1
checkpoint: seq=18 phase=approval file=.../latest.json
run: resume with: long-task-agent -run-dir ... -workspace ... -resume long_live_1

run: resumed long_live_1
Approval required: Finalize the long task
1. Approve
2. Reject
> approval: finalize_task approve
tool: finalize_task
run: done long_live_1
answer: The operator signed the report off; the long task is complete.
```

`go test ./examples/long-task-agent/` runs the whole two-process scenario as real
child processes against the scripted endpoint, plus a second case where a
completed checkpoint replays its terminal event without a model call. The
resumed run makes exactly one model call, and that request is asserted to carry
the tool results recorded *before* the pause (`Delivered("record_step")`,
`Delivered("finalize_task")`) and the pre-pause notes as text — which is what
distinguishes a resume from a restart.

## coding-agent

A workspace-editing agent with a human in the loop: it reads a file, edits it,
and runs a command to verify the change, with every write and every
non-allowlisted command gated behind the operator.

```bash
export ZENFORGE_PROVIDER=openai
export ZENFORGE_MODEL=...
export ZENFORGE_API_KEY=...
go run ./examples/coding-agent -workspace . \
  -task "Correct the greeting in greeting.txt and verify the change."
```

`-workspace` is both the agent's root and the hard filesystem boundary, and
`-run-dir` (or `ZENFORGE_RUN_DIR`) holds the JSONL event log and checkpoints, so
a coding run is inspectable after the fact. The policy is deliberately split:
reads inside the workspace need no prompt, while no write root is pre-authorized,
so `workspace_write` and `workspace_edit` always ask, and a path that escapes the
workspace is refused outright rather than offered for approval. The shell
allowlist is the no-prompt tier — `go build ./...` and `go test ./...` run
directly — and anything else is approved per call. The write side keeps the
snapshot guard on: a write to a path this run has not read is refused.

```text
tool: workspace_read
Approval required: Approve workspace write
1. Approve
2. Reject
> approval: workspace_write approve
tool: workspace_write
write: greeting.txt
approval: shell approve
shell: printf 'check-ok\n'
answer: Updated greeting.txt and verified it with printf check-ok.
```

`go test ./examples/coding-agent/` runs two scenarios against the scripted
endpoint. The first asserts the file on disk really changed, that both prompts
appeared, that a read result preceded the write call in the model's requests, and
that the command's output reached the model. The second answers the write prompt
with `2` and asserts the SDK's real behaviour: the agent loop synthesizes the
`approval_rejected` tool error itself, never calls the tool, leaves the file
byte-identical, and lets the run finish.

---

## harness-agent

The full external-application shape: `provider.FromEnv()`, a real filesystem
Agent Skill catalog, a separate typed `inspect_path` tool, numbered CLI
approval, and a Docker-backed shell with a read-only workspace mount.

```bash
export ZENFORGE_PROVIDER=anthropic
export ZENFORGE_MODEL=MiniMax-M3
export ZENFORGE_API_KEY=...
export ZENFORGE_BASE_URL=https://api.minimax.io/anthropic
go run ./examples/harness-agent -question "Inspect this project"
```

MiniMax is configured as an Anthropic- or OpenAI-compatible BaseURL, not as a
third provider protocol. The credential must match the chosen endpoint.
The skill root defaults to `examples/harness-agent/skills`; use `-skill-root`
or `ZENFORGE_SKILL_ROOT` to select an application-owned catalog.

---

## http-harness-agent

A complete local HTTP service assembly. It combines `provider.FromEnv()`, a
filesystem Agent Skill catalog, a typed inspection tool, Docker-backed shell
execution, a durable SQLite HITL inbox, SQLite events/checkpoints, and a SQLite
detached-run registry. It exposes detached start, resume, status, list, attach,
cancel, and approval endpoints through `harnesshttp.NewRuntime`.

**Key thing it demonstrates:** the application, rather than ZenForge core,
owns durable-store selection, HTTP server lifetime, and the provider endpoint.
The example binds only to `127.0.0.1`; production applications must add their
own authentication and tenancy layer before binding externally.

```bash
export ZENFORGE_PROVIDER=anthropic
export ZENFORGE_MODEL=your-model
export ZENFORGE_API_KEY=your-key
export ZENFORGE_BASE_URL=https://your-endpoint.example/v1

go run ./examples/http-harness-agent \
  -workspace . \
  -skill-root examples/harness-agent/skills

curl -sS -X POST http://127.0.0.1:8080/runs/start \
  -H 'content-type: application/json' \
  -d '{"runId":"review_1","input":"Inspect this workspace."}'
curl -N 'http://127.0.0.1:8080/runs/attach?runId=review_1'
```

See [the example README](https://github.com/feiyu912/zenforge/tree/main/examples/http-harness-agent)
for approval and recovery commands.

---

## simple-tool-agent

A minimal end-to-end agent that exposes one typed Go function as a
model-callable tool and runs it against an OpenAI-compatible model.

**Key thing it demonstrates:** the smallest possible `zenforge.New(...)`
configuration: one tool, no events, no checkpoints, no approval, no workspace.

**Rough size:** ~50 lines of Go.

```go
lookup := tools.Must("lookup_project_fact",
    "Look up one hard-coded project fact.",
    func(ctx context.Context, in lookupInput) (lookupOutput, error) {
        return lookupOutput{Result: "ZenForge is a Go agent harness..."}, nil
    })

agent := zenforge.New(zenforge.Config{
    Model: openai.New(openai.Config{
        APIKey:  os.Getenv("OPENAI_API_KEY"),
        Model:   env("OPENAI_MODEL", "gpt-4.1"),
        BaseURL: os.Getenv("OPENAI_BASE_URL"),
    }),
    Instructions: "Use the lookup tool when asked about this project.",
    Tools:        []zenforge.Tool{lookup},
    MaxSteps:     4,
})
```

Run it with:

```bash
OPENAI_API_KEY=... go run ./examples/simple-tool-agent
```

---

## sdk-embedded-agent

The library-only example: no network calls, no API key. A tiny scripted
model emits a tool call and then a final answer, paired with an in-memory
event log, in-memory checkpoints, and a redacted trace sink.

**Key thing it demonstrates:** embedding ZenForge as a Go library and using
the in-memory stores (`eventlog/memory`, `checkpoint/memory`) plus
`trace.Redact(...)` to exercise the harness end-to-end without external
dependencies.

**Rough size:** ~90 lines of Go (about half is the scripted model stub).

```go
events := eventlogmemory.New()
checkpoints := checkpointmemory.New()
traces := trace.NewMemorySink()

agent := zenforge.New(zenforge.Config{
    Model:        &scriptedModel{},
    Instructions: "Use tools when useful and answer briefly.",
    Tools:        []zenforge.Tool{summarize},
    Events:       events,
    Checkpoints:  checkpoints,
    Trace:        trace.Redact(traces),
    MaxSteps:     4,
})
```

Useful as a starting template for unit tests and CI runs where hitting a real
model is undesirable.

---

## code-review-agent

A focused read-mostly code-review workflow. The agent can `read`, `grep`, and
run an allowlist of shell commands such as `go test ./...` and `go vet ./...`
inside the workspace.

**Key thing it demonstrates:** CLI approval gating via
`approval/cli`, plus the "effectively read-only" workspace pattern:
`RequireReadBeforeWrite` snapshots, a one-byte write cap, and a
`WriteRoots` allowlist of `.zenforge/generated` only.

**Rough size:** ~85 lines of Go.

```go
agent := zenforge.New(zenforge.Config{
    Model: openai.New(openai.Config{
        APIKey: os.Getenv("OPENAI_API_KEY"),
        Model:  env("OPENAI_MODEL", "gpt-4.1"),
    }),
    Instructions: "Review code like a senior engineer...",
    Tools:        append(workspaceTools, shellTool),
    Approval:     approvalcli.New(os.Stdin, os.Stderr),
    Events:       eventlogjsonl.New(runDir),
    Checkpoints:  checkpointjsonl.New(runDir),
    MaxSteps:     12,
})
```

Shell commands outside the allowlist are routed through the CLI broker and
prompted on stderr. Read it to see how `policy.FilePolicy` and
`policy.ShellPolicy` compose.

---

## repo-refactor-agent

The flagship example: a multi-step refactor planner with streaming output.
It uses the plan / execute / summary preset
(`Planning: zenforge.PlanningPlanExecute`), the workspace toolset
(read / list / grep / write), an allowlisted shell tool, JSONL events, and
JSONL checkpoints.

**Key thing it demonstrates:** the full loop — `agent.Stream(...)` consumed
event-by-event, the planner preset, todo updates, and durable runs that can
be replayed from the checkpoint store.

**Rough size:** ~110 lines of Go.

```go
agent := zenforge.New(zenforge.Config{
    Model:        openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY"), ...}),
    Instructions: "You are a senior Go backend engineer...",
    Tools:        append(workspaceTools, shellTool),
    Events:       eventlogjsonl.New(runDir),
    Checkpoints:  checkpointjsonl.New(runDir),
    MaxSteps:     20,
    Planning:     zenforge.PlanningPlanExecute,
})

events, err := agent.Stream(ctx, zenforge.Task{Input: input})
for event := range events {
    render(event) // EventModelDelta, EventToolCall, EventTodoUpdated, EventRunDone, ...
}
```

This is the closest example to a production harness: durable, inspectable,
and streaming.

---

## Running the examples

Most examples read their config from environment variables and default to
`gpt-4.1` against the OpenAI API. The common knobs are:

| Variable | Purpose | Default |
| --- | --- | --- |
| `OPENAI_API_KEY` | Model API key | _(required for non-embedded examples)_ |
| `OPENAI_MODEL` | Model name | `gpt-4.1` |
| `OPENAI_BASE_URL` | OpenAI-compatible endpoint | `https://api.openai.com/v1` |
| `ZENFORGE_WORKSPACE` | Root directory the workspace toolset is scoped to | `.` |
| `ZENFORGE_RUN_DIR` | Directory for JSONL events and checkpoints | `.zenforge/runs` |

The `sdk-embedded-agent` example is the only one that does not need an API
key — everything is driven by a local scripted model.

---

## Next steps

For a guided walkthrough that adds a small web UI and incremental tooling on
top of these patterns, see the [Tutorial](tutorial.md) page. For deeper
reference material, the [Tool authoring guide](tool-authoring-guide.md),
[Checkpoint & resume guide](checkpoint-resume-guide.md), and
[Approval guide](approval-guide.md) cover each framework concern in detail.

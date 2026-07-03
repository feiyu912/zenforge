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
    For individual concepts, read `simple`, `sdk-embedded`, `code-review`,
    then `repo-refactor`.

---

## harness-agent

The full external-application shape: `provider.FromEnv()`, a real filesystem
Agent Skill catalog, a separate typed `inspect_path` tool, numbered CLI
approval, and a Docker-backed shell with a read-only workspace mount.

```bash
export ZENFORGE_PROVIDER=anthropic
export ZENFORGE_MODEL=MiniMax-M3
export ZENFORGE_API_KEY=...
export ZENFORGE_BASE_URL=https://api.minimaxi.com/anthropic/v1
go run ./examples/harness-agent -question "Inspect this project"
```

MiniMax is configured as an Anthropic- or OpenAI-compatible BaseURL, not as a
third provider protocol. The credential must match the chosen endpoint.
The skill root defaults to `examples/harness-agent/skills`; use `-skill-root`
or `ZENFORGE_SKILL_ROOT` to select an application-owned catalog.

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

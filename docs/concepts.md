# Concepts

This page is the mental-model layer of the ZenForge documentation. It explains the
moving parts, why each one exists, and how they fit together. It deliberately
stays conceptual — for hands-on setup, see the [Tutorial](tutorial.md); for
task-shaped usage, see the [SDK Guide](sdk-guide.md); for the public event
contract, see [Architecture](architecture.md).

## Mental model

ZenForge is a runtime, not an application. It ships the durable agent loop, the
model adapter contract, the tool registry and invoker, the approval broker, the
sandbox abstraction, and three persistence streams (event log, checkpoint
store, trace sink). What it does **not** ship is a single opinionated binary
that picks a model, parses your CLI flags, manages your filesystem, or
shortcuts your safety policy. The application — the Go service, CLI, desktop
app, or gateway that imports ZenForge — owns those choices. Every adapter
point is a Go interface that you can satisfy, wrap, mock, or leave nil.

This separation is what lets ZenForge run unchanged in a long-lived backend,
inside a one-shot CLI, and inside a test that fakes every external
dependency. The runtime code path is the same in all three; only the values
passed into `zenforge.New(zenforge.Config{...})` differ.

## The agent loop

The ReAct-style loop lives in `harness.Runner`. The root `zenforge.Agent`
wraps it with the configured model, tools, approval broker, sandbox session,
event log, and checkpoint store. Step by step:

```text
                 user input
                    |
                    v
            +---------------+
            | build prompt  |   harness appends system instructions and
            | (state msgs)  |   accumulated run state into model.Request
            +-------+-------+
                    |
                    v
            +---------------+
            | model.Stream  |   model.Model returns a channel of
            |               |   deltas, tool calls, usage, and errors
            +-------+-------+
                    |
            +-------v-------+
            | tool calls?   |--- no ---> terminal: final assistant message
            +-------+-------+              becomes run output
                    | yes
                    v
            +---------------+
            | tool registry |   tool.Invoker looks up the tool by name,
            | + invoker     |   runs middleware, calls tool.Call
            +-------+-------+
                    |
                    v
            +---------------+
            | approval?     |--- need approval --> approval.Broker.Request
            | (tool result  |                     pause until decision,
            |  carries a    |                     resume from checkpoint
            |  Request)     |
            +-------+-------+
                    | approved / not needed
                    v
            +---------------+
            | tool.Call     |   may invoke sandbox.Sandbox.Execute for
            | (sandbox if   |   shell-style tools; otherwise in-process
            |  wired)       |
            +-------+-------+
                    |
                    v
            +---------------+
            | append tool   |   result becomes a tool-role message in
            | message,      |   state.Messages; eventlog.Append and
            | checkpoint    |   checkpoint.Save fire here
            +-------+-------+
                    |
                    +------> back to "build prompt"
```

Two caps protect you from infinite loops:

- **`Config.MaxSteps`** (default `8`) bounds how many model turns the agent
  may take before a forced final no-tool turn.
- The `oneshot` execution mode (`Config.Mode = ModeOneshot`) caps
  `MaxSteps` at 2 to guarantee at most one tool call.

The loop continues until the model returns a turn with no tool calls, the
max-step cap is hit (a final no-tool turn is forced), the context is
cancelled, or a tool returns a non-retryable error. Each transition is
checkpointed before the next decision is made, so a crash at any boundary
leaves a resume point on disk.

## The four interfaces your code touches

ZenForge hides as much as it can behind `zenforge.Agent`, but four interfaces
show up in every real integration: the model, the tool, the approval broker,
and the sandbox. Each one is small enough to mock, and each one has at least
one shipping reference implementation.

### `model.Model`

The model is whatever produces text and tool calls given a chat history.
The interface lives in `model/interface.go` and has two methods: a
non-streaming `Generate` and a streaming `Stream` that the harness always
prefers.

```go
type Model interface {
    Generate(ctx context.Context, req Request) (*Response, error)
    Stream(ctx context.Context, req Request) (<-chan Event, error)
}
```

In plain English: "given a list of messages and the available tool specs,
produce either a single response or a stream of deltas, tool calls, and
usage." ZenForge calls `Stream` during the loop and only falls back to
`Generate` if your code asks for the convenience path.

The simplest possible implementation is a scripted model that returns a
single tool call on the first turn and a canned answer on the second —
this is exactly the pattern used by the SDK embedded example.

```go
type scriptedModel struct{ turn int }

func (m *scriptedModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
    m.turn++
    out := make(chan model.Event, 1)
    go func() {
        defer close(out)
        if m.turn == 1 {
            out <- model.Event{Message: &model.Message{
                ToolCalls: []model.ToolCallSpec{{
                    ID:        "call_1",
                    Name:      "lookup",
                    Arguments: json.RawMessage(`{"q":"zenforge"}`),
                }},
            }}
            return
        }
        out <- model.Event{Delta: "ZenForge is a Go agent runtime."}
    }()
    return out, nil
}

func (m *scriptedModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
    ch, err := m.Stream(ctx, req)
    if err != nil {
        return nil, err
    }
    var resp model.Response
    for ev := range ch {
        if ev.Message != nil { resp.Message = *ev.Message }
        resp.Message.Content += ev.Delta
    }
    return &resp, nil
}
```

The harness calls into the model at the start of every step, after the
pending tool calls from the previous turn have completed. It converts
`model.ToolCallSpec` into the harness's internal `ToolCallState`, appends
them to `state.Tool.Pending`, and proceeds to the tool-invocation phase.

### `tool.Tool`

A tool is something the model can call by name. The interface lives in
`tool/interface.go` and bundles identity, schema, and the actual call. There
is also a separate `tool.Invoker` interface that the harness uses to
dispatch calls — by default the Agent builds one from the configured tools
and any registered middleware.

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any
    Call(ctx context.Context, input json.RawMessage, call Context) (Result, error)
}
```

In plain English: "tell me your name and JSON schema so the model can see
you, then run when the harness hands me raw arguments plus run metadata."

The simplest possible implementation is a one-line lookup using the
`tools.Must` helper, which infers the JSON schema from a Go struct.

```go
lookup := tools.Must("lookup", "Look up an internal fact.",
    func(ctx context.Context, in struct {
        Query string `json:"query" jsonschema:"required"`
    }) (string, error) {
        return "answer for " + in.Query, nil
    })
```

The harness calls into a tool exactly once per pending tool call. The tool
returns a `tool.Result` containing either a text `Output`, a structured
`Structured` payload, or an `Error` (with a non-zero `ExitCode`). A special
pattern lets a tool request approval: it returns a result whose `Error`
equals `"approval_required"` and whose `Structured["approval"]` holds an
`approval.Request`. The harness picks that up, pauses for the broker, and
re-invokes the tool with approval metadata attached.

### `approval.Broker`

The approval broker decides whether a risky operation is allowed to
proceed. The interface lives in `approval/broker.go` and is intentionally
single-method.

```go
type Broker interface {
    Request(ctx context.Context, req Request) (Decision, error)
}
```

In plain English: "given a structured request describing an operation and
its risk, return a decision or block until one arrives."

The simplest possible implementation is the built-in `approval.AlwaysAllow`
or `approval.AlwaysDeny`, but the package also exposes a `BrokerFunc`
adapter so you can write an inline closure without declaring a type.

```go
broker := approval.BrokerFunc(func(ctx context.Context, req approval.Request) (approval.Decision, error) {
    if req.Risk == approval.RiskLow {
        return approval.Decision{
            RequestID: req.ID,
            Action:    approval.DecisionApprove,
            Scope:     approval.ScopeOnce,
            DecidedAt: time.Now().UTC(),
        }, nil
    }
    log.Printf("needs human review: %s", req.Title)
    return approval.Decision{}, approval.NewAbortError("defer to operator")
})
```

The harness calls the broker at most once per approval-bearing tool call
(grants with scope `run` or `rule` are remembered and reused without
re-prompting). If no broker is configured and a tool asks for approval, the
loop pauses durably and `Agent.Run` returns `approval.ErrRequired` so the
caller can resume later via `Agent.Resume`.

### `sandbox.Sandbox`

The sandbox is where tool commands run when they need to leave the process.
The interface lives in `sandbox/interface.go` and is a three-method session
lifecycle.

```go
type Sandbox interface {
    Open(ctx context.Context, req OpenRequest) (*Session, error)
    Execute(ctx context.Context, session *Session, req ExecuteRequest) (ExecuteResult, error)
    Close(ctx context.Context, session *Session) error
}
```

In plain English: "open a session for a run, execute shell-style commands
inside it, close it when the run ends."

The simplest possible implementation is the built-in `sandbox/fake`, which
records every call and returns whatever `ExecuteResult` you pre-loaded —
useful in tests where you want deterministic command output without a
container.

```go
sb := &fake.Sandbox{
    Result: sandbox.ExecuteResult{ExitCode: 0, Stdout: "ok\n"},
}
// tools that route through sb will see Stdout: "ok\n" for every command
```

Local shell tools do **not** require a sandbox: they execute directly in the
configured workspace. The sandbox interface exists for when you want shell
isolation in a container, a remote environment, or a remote-execution
backend. `sandbox/fake` and `sandbox/containerhub` are the two adapters that
ship today; you can add your own by implementing the three-method interface.

The harness does not call the sandbox directly. Instead, tools that need
isolation are wired with a sandbox in their config; they call `Open`,
`Execute`, and `Close` themselves and surface the result back through the
normal `tool.Result` path. The harness only needs the sandbox to be
configurable so that checkpoint `SandboxState` can carry the session
identity forward across resume.

## Persistence trio

ZenForge persists every run through three independent stores. They look
similar at a glance, but each one answers a different question.

### `eventlog.Store` — the chronological stream

```go
type Store interface {
    Append(ctx context.Context, event Event) error
    Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error)
    LatestSeq(ctx context.Context, runID string) (int64, error)
}
```

The event log is the public, append-only, sequence-numbered journal of
harness events: `run.started`, `model.delta`, `tool.call`, `tool.result`,
`approval.requested`, `checkpoint.created`, and so on. It is what
`cmd/zenforge events <runID>` prints and what an HTTP `/events` endpoint
replays. Events are typed (`EventType`) and carry a strictly increasing
`Seq` per run. Writes fail closed: if appending fails the loop emits a
`run.error` instead of advancing with unrecorded progress.

### `checkpoint.Store` — the durable resume state

```go
type Store interface {
    Save(ctx context.Context, checkpoint Checkpoint) error
    Load(ctx context.Context, runID string) (*Checkpoint, error)
    Delete(ctx context.Context, runID string) error
}
```

The checkpoint store is the source of truth for resuming a run. A
checkpoint captures the full `harness.RunState` — every message, every
pending tool call, the active approval, the current step counter, sandbox
session id, and so on — under a schema version (`zenforge.checkpoint.v1`).
Saves happen **before** the next decision boundary, not after, so a crash
never leaves the loop pointing at a state that does not exist on disk.

### `trace.Sink` — the redacted observability feed

```go
type Sink interface {
    Emit(ctx context.Context, event Event) error
}
```

The trace sink is a separate, best-effort, redacted view of the same
execution, intended for OpenTelemetry, JSONL log files, or in-memory
assertions in tests. Unlike the event log, the trace sink:

- is never authoritative (the run result is decided before any trace emit),
- has key-based redaction (`trace.Redact` covers `api_key`, `authorization`,
  `password`, `secret`, `token` by default),
- may be wrapped with `trace.WithFields` to add static metadata to every
  event.

### Why three stores

They look like the same thing because they overlap in time, but they
answer different operational questions:

| Concern | Event log | Checkpoint | Trace |
| --- | --- | --- | --- |
| Purpose | "What happened, in order?" | "What state do I resume from?" | "What do I ship to observability?" |
| Consumers | CLI, HTTP `/events`, replay | `Agent.Resume`, recovery tools | OTel, log aggregators |
| Schema | Versioned event names + payloads | `zenforge.checkpoint.v1` `RunState` | Open conventions, free-form |
| Failure mode | Fail closed (run.error) | Fail closed (run.error) | Best effort, never blocks |
| Retention | Long, append-only | One per run, replaceable | Configurable, redacted |

Collapsing them would force you to choose: either the trace carries
secrets (because the event log does), or the event log loses replay fidelity
(because the checkpoint only stores state, not the public event stream),
or resume becomes fragile (because traces are best effort). Three stores
let each pipeline be optimized for its real consumer.

## The boundary

ZenForge deliberately does not include:

- a CLI flag parser (the `cmd/zenforge` binary uses the standard `flag`
  package and reads a JSON config file; you can replace both),
- a model picker (`Config.Model` is just an interface; you wire the one
  you want),
- a sandbox implementation (`sandbox/fake` is a test stub,
  `sandbox/containerhub` is one optional backend; you provide the rest),
- a workspace implementation (`workspace/local` is one adapter; an S3 or
  SSH workspace is another),
- a planner implementation (the default in-memory manager is one option;
  the plan/execute preset is opt-in),
- auth, tenancy, catalog loading, or routing,
- an HTTP server (the `server/harnesshttp` package is a reference, not a
  mandate).

These omissions are not gaps to be filled by the framework. They are the
boundary that lets ZenForge stay small. Frameworks that ship one
opinionated binary — a fixed CLI, a hardcoded model router, a single
sandbox vendor — are easier to demo and harder to embed. The instant your
backend wants a different auth scheme, your CLI wants a different flag
shape, or your platform wants a different model catalog, the opinionated
binary becomes a fork. ZenForge's answer is to push those choices into the
application code that calls `zenforge.New`.

Concretely: the application-owned harness principle means

- the application picks `Config.Model` (could be OpenAI, Anthropic, a local
  stub, a custom gateway);
- the application picks `Config.Approval` (could be `AlwaysAllow` in tests,
  `approval/cli` in a CLI, `approval.PendingBroker` in an HTTP server);
- the application picks `Config.Checkpoints` and `Config.Events` (could be
  memory for tests, SQLite for production, JSONL for inspection);
- the application picks the tools, the sandbox backend, the workspace, the
  trace sink, and the sub-agent roster.

The framework does the boring, durable, hard-to-get-right part: the
checkpoint boundaries, the resume decision tree, the approval lifecycle,
the redaction, the fail-closed event writes. Everything else is yours.

## Composition

Putting it all together: a minimal but real `zenforge.New(...)` call that
wires up the four interfaces and the persistence trio. This is roughly
the shape of `examples/sdk-embedded-agent/main.go` minus the embedded
scripted model.

```go
agent := zenforge.New(zenforge.Config{
    // The model: any model.Model. OpenAI-compatible, Anthropic, or custom.
    Model: openai.New(openai.Config{
        APIKey: os.Getenv("OPENAI_API_KEY"),
        Model:  "gpt-4.1",
    }),

    // System prompt prepended to every model turn.
    Instructions: "Use tools when useful and answer briefly.",

    // Tools: registered by name; the model sees them via tool specs.
    Tools: []zenforge.Tool{lookup, summarize},

    // Approval: a real broker (or approval.AlwaysAllow in tests).
    Approval: approval.NewPendingBroker(16),

    // Sandbox: optional. Tools that need isolation use it.
    Sandbox: containerhub.New(containerhub.Config{...}),

    // Persistence trio: events (replay), checkpoints (resume), trace (OTel).
    Events:      eventlogsqlite.Open(".zenforge/runs.db"),
    Checkpoints: checkpointsqlite.Open(".zenforge/runs.db"),
    Trace:       trace.Redact(trace.OTLP("otel-collector:4317")),

    // Loop cap. 0 means use the harness default (8).
    MaxSteps: 8,
})
```

Each line maps to one of the concepts above:

- `Model` is the brain. The harness streams prompts into it and streams
  deltas, tool calls, and usage back out.
- `Tools` is the skill set. Names must be unique; schemas are inferred or
  provided; calls are dispatched through the configured invoker (or a
  default one built from the registry).
- `Approval` is the brake. A `PendingBroker` exposes a channel you can
  drain in another goroutine or from an HTTP handler; absent a broker, an
  approval-bearing tool call pauses durably and `Agent.Run` returns
  `approval.ErrRequired`.
- `Sandbox` is the execution room. Tools that have a sandbox wired route
  shell commands through it; tools that do not run in-process.
- `Events` is the read model. Append-only, sequence-numbered, the source
  of truth for "what happened."
- `Checkpoints` is the resume model. Versioned, replaced per run, the
  source of truth for "what state do I resume from."
- `Trace` is the side channel. Redacted, best effort, the source of truth
  for "what do I export to my observability stack."
- `MaxSteps` is the safety cap. After this many model turns the harness
  forces a final no-tool turn.

From there, usage is one of three methods on the returned `*Agent`:

- `agent.Stream(ctx, task)` returns a channel of public events. Use this
  for live UIs, SSE feeds, or test assertions.
- `agent.Run(ctx, task)` drains the stream and returns a final `Result` (or
  an error). Use this when you only care about the terminal answer.
- `agent.Resume(ctx, runID)` re-attaches to a durable run using the
  configured checkpoint store. The loop continues exactly where it left
  off, including any in-flight approval wait.

The Tutorial walks through a working example with each of these in place;
the SDK Guide covers the call-and-response patterns and edge cases. This
page is the map. The Tutorial is the walk.

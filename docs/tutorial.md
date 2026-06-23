# Tutorial — Build Your First ZenForge Agent

> A hands-on walkthrough of the [`zenforge-harness-smoke`](https://github.com/feiyu912/zenforge-harness-smoke)
> companion app, which imports [ZenForge](https://github.com/feiyu912/zenforge)
> as a library and assembles an agent outside the framework core.

!!! note "About this page"
    This tutorial uses the **`zenforge-harness-smoke`** companion project, which
    lives in a separate repo. The ZenForge framework itself is at
    [github.com/feiyu912/zenforge](https://github.com/feiyu912/zenforge). Clone
    both to follow along:

    ```bash
    git clone https://github.com/feiyu912/zenforge
    git clone https://github.com/feiyu912/zenforge-harness-smoke
    ```

This tutorial walks you through `zenforge-harness-smoke`, a small Go program
that imports ZenForge and assembles an agent **outside the framework core** —
the "DeepAgents-style" application-owned harness. By the end you'll know every
function in the project, what it does, why it exists, and how to swap pieces
for your own provider, tool, approval policy, or sandbox backend.

---

## Table of contents

1. [Introduction](#1-introduction)
2. [Prerequisites](#2-prerequisites)
3. [Quick start](#3-quick-start)
4. [Architecture overview](#4-architecture-overview)
5. [Configuration reference](#5-configuration-reference)
6. [Model adapters](#6-model-adapters)
7. [The approval flow](#7-the-approval-flow)
8. [The Docker sandbox](#8-the-docker-sandbox)
9. [The local skill tool](#9-the-local-skill-tool)
10. [Walkthroughs](#10-walkthroughs)
11. [Function reference](#11-function-reference)
12. [Troubleshooting](#12-troubleshooting)
13. [Extending the harness](#13-extending-the-harness)
14. [Further reading](#14-further-reading)

---

## 1. Introduction

### 1.1 What is ZenForge?

ZenForge is a Go library for building AI agents. It supplies a provider-neutral
`model.Model` interface, a tool registry, an approval broker, a sandbox
abstraction, an event log, a checkpoint store, and a streaming runner. You
write a small Go program, plug in your choices, and ZenForge handles the
agent loop, retries, persistence, and tracing.

The library deliberately does **not** ship a CLI flag parser, a model picker,
or a sandbox implementation. Those are *application-owned*: your program
decides which provider to use, which tools to expose, who approves what, and
where commands run.

### 1.2 What is the harness-smoke app?

`zenforge-harness-smoke` is a separate Go module that demonstrates that
boundary. It lives in its own directory, depends on ZenForge as an external
library, and assembles an agent in roughly 400 lines of Go. The program
provides:

- three model adapters (offline scripted, MiniMax via Anthropic-compatible
  API, MiniMax via OpenAI-compatible API);
- two approval modes (auto-allow, interactive prompt);
- two shell backends (local execution, Docker sandbox);
- two tools (a local-skill lookup, a sandboxed shell).

It's a "smoke test" for the harness pattern: small enough to read in one
sitting, rich enough to exercise every major ZenForge surface.

### 1.3 What you'll build

By following this tutorial you'll be able to:

- run the smoke app with the offline scripted model (no API key required);
- run it against a real provider (MiniMax) using both Anthropic and OpenAI
  protocol adapters;
- swap in your own tools, approval policy, or sandbox backend without
  touching ZenForge;
- read and modify any function in the project.

---

## 2. Prerequisites

| Requirement | Minimum | Notes |
|---|---|---|
| Go | 1.22+ | Uses `log/slog`, generic slices, and `errors.Is`/`errors.As` |
| Docker | 20.10+ | Only required for the Docker sandbox path |
| MiniMax API key | n/a | Optional. Without it, the app uses the scripted model |
| Terminal | any | For interactive `--approve prompt` you need a TTY |

Verify your setup:

```bash
go version
docker --version
```

That's all you need for the scripted path. The MiniMax paths additionally
need an API key in your `.env` file (see §3).

---

## 3. Quick start

### 3.1 Clone and build

```bash
git clone https://github.com/feiyu912/zenforge-harness-smoke
cd zenforge-harness-smoke
go build ./...
```

The build should complete silently. If it errors, check your Go version
and that `go.mod` resolves — the module path is
`github.com/feiyu912/zenforge`.

### 3.2 Run with the scripted model (no API key, no Docker)

```bash
go run . --model scripted --docker=false
```

You'll see a `You:` prompt. Type:

```
What does the local skill tool know about the harness?
```

The agent will:

1. Call the `local_skill` tool.
2. Return a canned answer.
3. Print the final assistant reply.

This works offline. The scripted model is a deterministic in-process
sequence (see §6.1).

### 3.3 Run with MiniMax (real provider)

Copy `.env.example` to `.env` and fill in your MiniMax key:

```bash
cp .env.example .env
$EDITOR .env
```

The relevant lines are:

```
ANTHROPIC_API_KEY=<your-key>
ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1
```

Then:

```bash
go run . --model minimax
```

You should get a real streamed response from the model. If you see a
`404` or `401`, jump to §12.

### 3.4 Run with Docker sandbox

```bash
go run . --model scripted --docker=true
```

The shell tool will run commands inside `alpine:3.20` via
`docker run --rm`. The current working directory is mounted at
`/workspace`. Output of the embedded test command (`uname -a`) is returned
through the harness.

---

## 4. Architecture overview

### 4.1 The model–tool–approval–sandbox quartet

Every ZenForge agent is composed from four orthogonal pieces:

```
┌──────────────────────────────────────────────────────────┐
│  Application code (zenforge-harness-smoke/main.go)       │
│                                                          │
│   ┌──────────┐   ┌──────────────┐   ┌────────────────┐   │
│   │  Model   │   │    Tools     │   │    Approval     │  │
│   │ adapter  │   │ (registry)   │   │    broker      │   │
│   └────┬─────┘   └──────┬───────┘   └───────┬────────┘   │
│        │                │                   │            │
│        └──────┬─────────┴─────────┬─────────┘            │
│               ▼                   ▼                      │
│        ┌──────────────────────────────────┐              │
│        │           Agent.Run / Stream     │              │
│        │   (zenforge/agent.Agent)         │              │
│        └────────────────┬─────────────────┘              │
│                         │                                │
│                         ▼                                │
│              ┌────────────────────┐                      │
│              │      Sandbox       │   (used by tools)    │
│              │   (Docker, local)  │                      │
│              └────────────────────┘                      │
└──────────────────────────────────────────────────────────┘
```

- **Model adapter** — implements `model.Model`. Translates between
  ZenForge's provider-neutral `Request`/`Response`/`Event` types and a
  vendor's wire format.
- **Tools** — implement `tool.Tool`. Each tool has a JSON schema for its
  input, a name, a description, and an `Invoke` method.
- **Approval broker** — implements `approval.Broker`. Decides whether a
  tool call may proceed, deny, or be deferred to a human.
- **Sandbox** — implements `sandbox.Sandbox`. Owns the lifecycle of an
  isolated execution environment (open, execute, close).

The harness calls the model, parses tool calls, hands each to the approval
broker, and on approval calls the tool — which may in turn call the
sandbox. The app owns all four pieces; the harness owns the loop.

### 4.2 Where things live in the smoke app

| File | Purpose |
|---|---|
| `main.go` | CLI flags, env loader, agent assembly, scripted model |
| `env.go` | Minimal `.env` parser |
| `docker_sandbox.go` | `DockerSandbox` adapter that shells out to `docker run` |
| `main_test.go` | One end-to-end test of the scripted agent |
| `go.mod` / `go.sum` | Module declaration, depends on `github.com/feiyu912/zenforge` |
| `.env.example` | Template; copy to `.env` and fill in your key |

The module has **no third-party dependencies beyond ZenForge itself**, which
is by design: the smoke app should be auditable in a single sitting.

---

## 5. Configuration reference

Configuration is layered: defaults → `.env` → environment → CLI flags. Later
layers override earlier ones.

### 5.1 CLI flags

```
go run . [flags] [question]

  -model string           Model adapter: auto | scripted | minimax | minimax-openai
                          (default "auto")
  -approve string         Approval mode: auto | prompt (default "auto")
  -docker bool            Run the shell tool in a Docker sandbox (default true)
  -image string           Docker image for the sandbox (default "alpine:3.20")
  -q string               Question to ask (overrides positional arg)
  -workspace string       Host directory to mount at /workspace (default ".")
  -minimax-key-env string Env var holding the MiniMax key
                          (default ANTHROPIC_API_KEY for the anthropic adapter,
                           MINIMAX_API_KEY for the openai adapter)
  -base-url string        MiniMax-compatible base URL
  -verbose bool           Print detailed harness events and tool results
```

### 5.2 Environment variables

| Variable | Default | Description |
|---|---|---|
| `ZENFORGE_MODEL` | `auto` | Same values as `-model`. |
| `ZENFORGE_APPROVE` | `auto` | `auto` or `prompt`. |
| `ZENFORGE_DOCKER` | `true` | `true`/`false`. |
| `ZENFORGE_DOCKER_IMAGE` | `alpine:3.20` | Image used by the Docker sandbox. |
| `ZENFORGE_QUESTION` | *(empty)* | Question to ask without typing. |
| `ZENFORGE_WORKSPACE` | `.` | Host workspace directory. |
| `ZENFORGE_VERBOSE` | `false` | `true`/`false`. |
| `ANTHROPIC_API_KEY` | *(required for `-model minimax`)* | The MiniMax key. |
| `ANTHROPIC_BASE_URL` | `https://api.minimaxi.com/anthropic/v1` | MiniMax endpoint. |
| `MINIMAX_API_KEY` | *(required for `-model minimax-openai`)* | OpenAI-style key. |

### 5.3 The `.env` file

`loadDotEnv` in `env.go` reads a simple `KEY=VALUE` file. Quoted values have
their surrounding quotes stripped. Lines beginning with `#` and blank lines
are ignored. Existing environment variables are **not** overwritten — shell
exports always win.

```
# .env
ANTHROPIC_API_KEY=sk-cp-...
ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1
ZENFORGE_MODEL=auto
ZENFORGE_APPROVE=auto
ZENFORGE_DOCKER=true
```

### 5.4 The `auto` model heuristic

When `-model` is `auto` (the default), the app picks an adapter:

1. If `ANTHROPIC_API_KEY` is set to a *real-looking* key (not a placeholder
   like `your-key`, `...`, or anything containing `填` or `replace`), pick
   `minimax`.
2. Otherwise, pick `scripted`.

This lets the same binary run offline in CI and online in dev without a
flag flip. The placeholder detection lives in `usableAPIKey`.

---

## 6. Model adapters

`modelFor` (`main.go:226`) is a switch on the resolved `-model` value. It
returns a `model.Model` — the only thing ZenForge requires to differ
between providers.

### 6.1 Scripted (`scriptedModel`)

A deterministic in-process model used for offline testing. It runs the
same three-step script on every invocation:

1. **Turn 1** — emit a tool call to `local_skill` with
   `{"topic":"zenforge harness"}`.
2. **Turn 2** — emit a tool call to `shell` with
   `{"command":"uname -a",...}`.
3. **Turn 3+** — emit a fixed final string.

It does not call the network. It always passes through the approval broker
for the shell call (because `shellTool` is configured with
`RequireApproval: true`), which is the easiest way to exercise the HITL
path without a live model.

The scripted model exists so the harness can be tested in environments
with no network, no API keys, and no Docker (with `--docker=false`).

### 6.2 MiniMax via Anthropic-compatible API

```go
return anthropic.New(anthropic.Config{
    APIKey:  apiKey,
    Model:   "MiniMax-M3",
    BaseURL: "https://api.minimaxi.com/anthropic/v1",
}), nil
```

Uses ZenForge's `anthropic` adapter, which speaks the Anthropic Messages
API. The model identifier `MiniMax-M3` is the MiniMax family identifier
for the current M-series model — change it if your account has access to
a different one.

The base URL ends at `/v1` because the adapter appends `/messages` to it
(that's the Anthropic Messages path). A common mistake is to leave the
base URL at `/anthropic` — that 404s on the actual server. See §12.

### 6.3 MiniMax via OpenAI-compatible API

```go
return openai.New(openai.Config{
    APIKey:  apiKey,
    Model:   "MiniMax-M3",
    BaseURL: "https://api.minimax.io/v1",
}), nil
```

Uses ZenForge's `openai` adapter, which speaks the OpenAI Chat Completions
API with streaming and function tools. Note the different host:
`minimax.io` (the OpenAI-compatible endpoint) vs `minimaxi.com` (the
Anthropic-compatible endpoint). The MiniMax company exposes the same
model family on two different protocols.

### 6.4 Adding your own adapter

Implement `model.Model`:

```go
type Model interface {
    Generate(ctx context.Context, req Request) (*Response, error)
    Stream(ctx context.Context, req Request) (<-chan Event, error)
}
```

Add a case to `modelFor`. The harness does not care which protocol you
speak — only the request/response/event shapes matter. See
`zenforge/model/anthropic/client.go` and `zenforge/model/openai/client.go`
for reference implementations.

---

## 7. The approval flow

### 7.1 What the broker does

Every tool call routes through `approval.Broker.Resolve(...)`. The broker
returns an `approval.Decision` of one of:

- `allow` — proceed.
- `deny` — refuse and surface the reason to the model.
- `prompt` — block until a human answers, then `allow` or `deny`.

### 7.2 `auto` mode

`approval.AlwaysAllow()` returns `allow` for every request. This is the
default and is appropriate for trusted code, CI, and offline scripted
runs.

### 7.3 `prompt` mode

`approvalcli.New(os.Stdin, os.Stderr)` reads a one-line `y/n` from the
terminal and writes a brief audit log. It **requires a real TTY** — if
stdin is not a character device, the app prints a warning and falls back
to `auto` for that run. The detection lives in `stdinInteractive`.

```go
info, err := os.Stdin.Stat()
return err == nil && (info.Mode()&os.ModeCharDevice) != 0
```

This is the right behavior for CI: never hang waiting for a human in a
non-interactive shell.

### 7.4 Where the policy comes from

The shell tool's policy is built in `shellTool`:

```go
policy.ShellPolicy{
    WorkingDir:      workingDir,
    AllowCommands:   []string{"go version"},
    RequireApproval: true,
    MaxTimeout:      20 * time.Second,
    MaxOutputBytes:  32_000,
}
```

`AllowCommands` is a whitelist, `RequireApproval` makes every invocation
go through the broker, and the timeouts are enforced inside the sandbox.
You can change the whitelist to broaden or tighten the surface — for
example, `[]string{"go version", "go test ./..."}` to allow tests.

---

## 8. The Docker sandbox

### 8.1 Why a sandbox?

The shell tool gives the model the ability to run commands. Without a
sandbox, those commands run as your user on your machine. A Docker
sandbox confines them to a fresh container, optionally with a restricted
mount, on a clean filesystem.

### 8.2 How `shellTool` wires it

```go
shelltool.New(shelltool.Config{
    Policy:        p,
    Backend:       shelltool.ShellBackendSandbox,
    Sandbox:       DockerSandbox{Image: opts.Image},
    EnvironmentID: opts.Image,
    Mounts: []sandbox.Mount{
        {Source: mustAbs(opts.Workspace), Destination: "/workspace", Mode: "rw"},
    },
})
```

The shell tool calls into the sandbox abstraction. `Sandbox` is an
interface; `DockerSandbox` is one implementation. The same tool works
against any other sandbox backend that implements the interface.

### 8.3 What `DockerSandbox` does

`Open` (`docker_sandbox.go:21`) returns a `*sandbox.Session` describing
the image, working directory, and mount metadata. It does **not** start
a container — that happens lazily on the first `Execute` call.

`Execute` (`docker_sandbox.go:41`) builds a `docker run --rm` command:

```bash
docker run --rm \
  -v <workspace>:/workspace:rw \
  -w <container-cwd> \
  <image> \
  sh -lc <command>
```

It honors the request's timeout via `context.WithTimeout`, captures
stdout/stderr, and maps `context.DeadlineExceeded` to the sentinel
`sandbox.ErrTimeout` with exit code 124 (the conventional `timeout(1)`
code). The image is taken from the session's `EnvironmentID`, falling
back to the configured `Image` field, and finally to `alpine:3.20`.

`Close` is a no-op. Each `docker run --rm` is its own ephemeral
container, so there is no long-lived process to terminate.

### 8.4 `containerCWD` — mapping host paths to the container

When the shell tool runs in a subdirectory of the mounted workspace, we
need to translate the host path to the corresponding path inside the
container. `containerCWD` walks the configured mounts in order, takes
the `filepath.Rel` of the host CWD against the mount source, and joins
that relative path onto the mount destination. If no mount matches, it
falls back to `/workspace`.

`mustAbs` resolves symlinks via `filepath.EvalSymlinks` so the docker
`-v` flag receives a real path.

### 8.5 Disabling the sandbox

`--docker=false` (or `ZENFORGE_DOCKER=false`) swaps to
`shelltool.ShellBackendLocal`. Commands run in the host shell with the
configured policy. The policy's `AllowCommands` and `MaxOutputBytes`
limits still apply.

---

## 9. The local skill tool

`local_skill` is a stub for what would normally be a real tool backed by
a vector store, a database, or a remote skill service. In the smoke app
it returns a fixed string:

```go
return "local skill says: ZenForge is an application-owned Go harness; ..."
```

It's useful for two reasons:

1. The scripted model always calls it first, so you can verify the
   tool-dispatch path works end-to-end without a network.
2. It demonstrates the tool schema: every tool declares a JSON
   `jsonschema` tag on its input struct, which ZenForge forwards to the
   model as the tool's input schema.

To replace it with a real tool, define a function with the same
signature shape, register it with `tools.Must(...)`, and pass it to
`zenforge.New` in the `Tools` slice of `zenforge.Config`.

---

## 10. Walkthroughs

These walkthroughs assume you are in the `zenforge-harness-smoke`
directory. The output shown is representative; your exact output will
differ based on shell, OS, and tool versions.

### 10.1 Walkthrough 1 — Scripted run, local shell

**Goal:** verify the offline path works without Docker.

```bash
go run . --model scripted --docker=false
```

You'll see:

```
You: What does the local skill know?
→ tool: local_skill
→ tool: shell
→ approval: allow
...

Assistant: Yes. The app supplied the model, a local skill tool, ...
```

**What to look for:**

- `→ tool: local_skill` — the model emitted a tool call; the tool was
  invoked.
- `→ tool: shell` — the second scripted turn.
- `→ approval: allow` — the auto-approval broker let it through.
- The final assistant line.

**If you don't see those lines:** check that the scripted model
selected (`auto` resolves to `scripted` when no key is set), and that
the tool is wired in `buildAgent`.

### 10.2 Walkthrough 2 — Scripted run, Docker shell

**Goal:** verify the Docker sandbox path.

```bash
go run . --model scripted --docker=true
```

**What to look for:** the `shell` tool call should produce real output
from `uname -a` running inside `alpine:3.20`. If Docker is not running,
you'll get a `docker: error during connect` message. If the image is
not pulled, Docker will pull it on first run.

To check that the sandbox isolation is real, try changing the
`AllowCommands` list to something that touches the host:

```go
AllowCommands: []string{"cat /etc/passwd"},
```

This will fail inside the container (no host passwd) — proving the
sandbox is in effect.

### 10.3 Walkthrough 3 — MiniMax run, verbose mode

**Goal:** verify the real provider path and observe the event stream.

```bash
go run . --model minimax --verbose "Say hi in one sentence"
```

**What to look for in verbose output:**

- `[tool] local_skill {…}` — if the model decides to call a tool.
- `[tool-result] …` — the tool's return value as the model saw it.
- The streamed text deltas (printed as the model emits them).
- `[done] …` — the final aggregated message.

If the model doesn't call any tool, that's fine — the harness will
return its text reply directly. The scripted model always calls tools;
real models decide.

### 10.4 Walkthrough 4 — Failure modes

These are the three errors you are most likely to hit.

**404 Not Found** — your `ANTHROPIC_BASE_URL` is missing the `/v1`
suffix.

```
error: anthropic messages failed: 404 Not Found: 404 page not found
```

Fix: set `ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1`.

**401 Unauthorized** — your key is wrong, expired, or scoped to a
different account.

```
error: MiniMax rejected the API key. Check ANTHROPIC_API_KEY in .env ...
```

Fix: regenerate the key in the MiniMax console and update `.env`.

**Unknown model / unknown approval** — typo in a flag.

```
error: unknown model "bogus"
error: unknown approval mode "bogus"
```

Fix: see §5.1 for the valid values.

---

## 11. Function reference

This section walks every function in the project. The line numbers refer
to the layout as of the date of this tutorial; the *names* are stable.

### 11.1 `main.go`

#### `func main()` — main.go:47

Entry point. Loads `.env`, parses flags, calls `run`, and prints any
error to stderr with a non-zero exit code. It does not log on success.

#### `func parseFlags() appOptions` — main.go:56

Defines every CLI flag and binds it to the corresponding `appOptions`
field, falling back to `optionsFromEnv` for the defaults. After flag
parsing, if no `-q` was given, it joins the remaining positional
arguments into a single question string. This means `go run . hello
there` is equivalent to `go run . -q "hello there"` (modulo shell
quoting).

#### `func optionsFromEnv() appOptions` — main.go:74

Reads `ZENFORGE_*` and `ANTHROPIC_*` env vars and returns an
`appOptions`. The `auto` model resolution happens here: if
`ZENFORGE_MODEL` is unset and `ANTHROPIC_API_KEY` looks real, the
default becomes `minimax`; otherwise it becomes `scripted`. The key env
var defaults to `ANTHROPIC_API_KEY` for the Anthropic adapter and
`MINIMAX_API_KEY` for the OpenAI one.

#### `func usableAPIKey(value string) bool` — main.go:106

Returns `false` for empty strings and for common placeholder values
like `...`, `your-key`, `your_api_key`, `your-minimax-api-key`, and
anything containing the CJK character `填` (which appears in many
template `.env` files) or the substring `replace`. The goal is to
recognize the difference between "no key set" and "a placeholder the
user hasn't replaced yet" so the `auto` heuristic doesn't pick a paid
adapter by accident.

#### `func envString(key, fallback string) string` — main.go:120

Returns the trimmed env var if non-empty, else `fallback`. Centralized
so every option has the same "trim and fall back" behavior.

#### `func envBool(key string, fallback bool) bool` — main.go:127

Same idea for booleans. Uses `strconv.ParseBool`; on parse error, falls
back rather than panicking. Accepts `1`, `t`, `T`, `TRUE`, `true`,
`True`, `0`, `f`, `F`, `FALSE`, `false`, `False`.

#### `func run(ctx context.Context, opts appOptions) error` — main.go:139

The orchestrator. If the question is empty, prompts for one. If the
model is scripted, prints a notice to stderr explaining the offline
mode. Calls `buildAgent` to assemble the agent, then `agent.Stream` to
run it. Iterates the event channel, prints events via `printEvent`, and
captures the final output. On `EventRunError`, calls
`providerAuthHint` to convert the raw error into a friendlier hint
when applicable.

#### `func promptQuestion() (string, error)` — main.go:179

Prints `You: ` and reads a single line from stdin. Trims whitespace.
Returns an error if the line is empty or if stdin is at EOF. This is
only used when no question was provided via flag, arg, or env.

#### `func buildAgent(opts appOptions) (*zenforge.Agent, error)` — main.go:195

The composition root. Registers the `local_skill` tool via
`tools.Must`, builds the shell tool via `shellTool`, picks a model via
`modelFor`, and picks an approval broker via `approvalBroker`. Then
returns `zenforge.New(...)` with:

- `Model` — the picked adapter.
- `Instructions` — the system prompt; tells the model to use the local
  skill for harness facts and the shell to demonstrate the sandbox.
- `Tools` — `local_skill` and the shell tool.
- `Approval` — the chosen broker.
- `Events` — `eventlogmemory.New()` — an in-memory event log.
- `Checkpoints` — `checkpointmemory.New()` — an in-memory checkpoint
  store.
- `Trace` — `trace.Redact(trace.NewMemorySink())` — an in-memory
  tracing sink with redaction enabled.
- `MaxSteps` — `6` — cap on the agent loop, prevents runaway.
- `Mode` — `zenforge.ModeReact` — the ReAct loop is appropriate for
  tool-calling models.

#### `func modelFor(opts appOptions) (model.Model, error)` — main.go:226

Dispatches on the resolved model name.

- `""` or `"scripted"` → the in-process `scriptedModel`.
- `"minimax"` → if the key env is missing or a placeholder, prints a
  notice and falls back to scripted. Otherwise returns the
  Anthropic-compatible adapter configured with the MiniMax base URL.
- `"minimax-openai"` → if the key env is missing, returns an error
  (no fallback for this adapter — the user explicitly asked for it).
  Otherwise returns the OpenAI-compatible adapter.
- anything else → `unknown model "x"`.

#### `func providerAuthHint(message string) string` — main.go:264

Pattern-matches a 401 / `unauthorized` / `invalid api key` substring
in the raw error message and returns a friendlier hint that points
the user at `.env` and the correct base URL. Other errors pass
through unchanged.

#### `func approvalBroker(opts appOptions) (approval.Broker, error)` — main.go:272

Returns `approval.AlwaysAllow()` for `auto`, `approvalcli.New` for
`prompt` (with a fallback to auto if stdin is not a TTY), and an error
for anything else.

#### `func stdinInteractive() bool` — main.go:287

Returns true iff stdin is a character device (a TTY), detected via
`os.Stdin.Stat()` and `os.ModeCharDevice`. Used to avoid hanging on
`prompt` in CI.

#### `func shellTool(opts appOptions) (tool.Tool, error)` — main.go:292

Builds the shell tool. Always sets `RequireApproval: true` so the
broker is exercised. With `opts.Docker == false`, uses the local
backend. With `opts.Docker == true`, uses the `DockerSandbox` and
mounts `opts.Workspace` at `/workspace`. `AllowCommands` is the
sandbox policy's whitelist — keep it tight.

#### `func printEvent(event zenforge.Event, verbose bool)` — main.go:318

The terminal printer for harness events. In non-verbose mode, only
high-signal events are printed (tool calls, approval decisions, the
final result). In verbose mode, deltas, raw tool results, and the
final payload are also printed. All non-`EventRunDone` output goes to
stderr so the assistant's reply can be the only thing on stdout,
making the program scriptable.

#### `func compact(value any) string` — main.go:347

Stringifies an event payload, escapes newlines, and truncates to 320
characters. Used to keep verbose tool-result prints readable.

#### `type scriptedModel struct{ turn int }` — main.go:356

The in-process model. Holds a monotonic turn counter (not goroutine
safe, which is fine for the smoke app). Implements both `Generate` and
`Stream` — `Generate` simply drains the `Stream` channel and
assembles a `Response`.

#### `func (m *scriptedModel) Generate(...)` and `Stream(...)` — main.go:360/381

`Stream` returns a buffered channel and starts a goroutine that emits
exactly one event per turn (see §6.1). The channel is closed when the
goroutine returns. `Generate` consumes the channel, copies the message
fields into a `Response`, and returns.

### 11.2 `env.go`

#### `func loadDotEnv(path string) error` — env.go:9

A minimal `.env` parser.

- Skips blank lines and lines starting with `#`.
- Splits on the first `=`.
- Trims the key.
- Trims the value and strips surrounding `"` or `'` quotes.
- Skips the line if the key is already set in the environment — the
  shell always wins.
- Calls `os.Setenv` (ignoring its error, which is best-effort).
- Returns the scanner's final error, if any.

There is no support for `export FOO=bar`, multi-line values, or
variable interpolation. That's intentional: keep it small enough to
audit in a few seconds.

### 11.3 `docker_sandbox.go`

#### `type DockerSandbox struct{ Image string }` — docker_sandbox.go:17

The configured image. `Open` and `Execute` both fall back to
`"alpine:3.20"` if `Image` is empty.

#### `func (d DockerSandbox) Open(ctx, req) (*sandbox.Session, error)` — docker_sandbox.go:21

Returns a session describing the environment. Does not start a
container. The session ID is `sandbox.SessionKey(req.RunID,
req.SubtaskID)`, the working directory is `/workspace`, and the
mounts are serialized into `session.Metadata["mounts"]` for later
reconstruction by `Execute`.

#### `func (d DockerSandbox) Execute(ctx, session, req) (sandbox.ExecuteResult, error)` — docker_sandbox.go:41

Builds and runs the `docker run --rm` command (see §8.3). Captures
stdout and stderr separately. Maps `context.DeadlineExceeded` to
`sandbox.ErrTimeout` with exit code 124. Returns the image name in
`Metadata["image"]` for trace logging.

#### `func (d DockerSandbox) Close(ctx, session) error` — docker_sandbox.go:98

A no-op. `docker run --rm` removes the container on exit, so there is
nothing to clean up.

#### `func mountsState(mounts []sandbox.Mount) []map[string]string` — docker_sandbox.go:102

Serializes a `[]sandbox.Mount` into a `[]map[string]string` suitable
for storing in `session.Metadata`. Three keys per mount: `source`,
`destination`, `mode`.

#### `func mountsFromState(value any) []sandbox.Mount` — docker_sandbox.go:114

Inverse of `mountsState`. Type-asserts via `value.([]map[string]string)`
and rebuilds the `[]sandbox.Mount`. A type mismatch yields an empty
slice — the error is intentionally swallowed because the only
producer of this state is the same package.

#### `func containerCWD(hostCWD string, mounts []sandbox.Mount) string` — docker_sandbox.go:127

Maps a host CWD to a container CWD by walking the mounts in order and
taking `filepath.Rel` against the mount source. The first mount whose
relative path doesn't escape (`..` or `../*`) wins. Falls back to
`/workspace` if no mount matches.

#### `func mustAbs(path string) string` — docker_sandbox.go:142

Returns an absolute, symlink-resolved path. Tries `EvalSymlinks` first
(so paths inside a directory whose root is a symlink still resolve to
the real path), then falls back to `filepath.Abs` if symlink
resolution fails, and finally to the original `path` if `Stat` also
fails. The `must` prefix is conventional for "this shouldn't fail, but
if it does, here's a safe default" — there's no panic.

### 11.4 `main_test.go`

#### `func TestBuildAgentWithScriptedModel(t *testing.T)` — main_test.go:10

The only test in the project. Builds an agent with the scripted model
and `--docker=false`, runs `agent.Run` with the input `"hello"`, and
asserts the output is non-empty. This is a smoke test for the whole
assembly: model + tools + approval + event log + checkpoint store +
trace + runner. If this test passes, the wiring is correct.

#### `func zenforgeTask(input string) zenforge.Task` — main_test.go:24

Helper that builds a `zenforge.Task` with a fixed `RunID` and the
given input. The fixed `RunID` makes test output easy to grep.

---

## 12. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `error: anthropic messages failed: 404 Not Found` | Base URL missing `/v1` | Set `ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1` |
| `error: MiniMax rejected the API key. ...` | Key wrong, expired, or placeholder | Regenerate the key in the MiniMax console, update `.env` |
| `error: unknown model "x"` | Typo in `-model` | Use `auto`, `scripted`, `minimax`, or `minimax-openai` |
| `error: unknown approval mode "x"` | Typo in `-approve` | Use `auto` or `prompt` |
| `error: ANTHROPIC_API_KEY is not set to a real MiniMax key; falling back to offline scripted model.` | `.env` not loaded, or placeholder detected | Confirm `.env` exists in the working dir, or pass `ANTHROPIC_API_KEY=...` in the shell |
| `→ approval: prompt` never resolves in non-interactive shell | You used `--approve prompt` without a TTY | Use `--approve auto` in CI; `prompt` auto-falls-back in non-TTY (see `stdinInteractive`) |
| `docker: error during connect: ...` | Docker daemon not running | Start Docker Desktop or `systemctl start docker` |
| `uname -a` output looks wrong | Sandbox didn't mount your workspace | Check `--workspace`; default `.` mounts the project dir |
| `go build` errors after pulling the latest | Module drift | `go mod tidy && go build ./...` |
| The model calls a tool but the tool result is missing from output | Verbose mode off | Re-run with `--verbose` to see `[tool-result]` lines |

---

## 13. Extending the harness

### 13.1 Add a new tool

Define an input struct with `jsonschema` tags, write the function, and
register it:

```go
type myInput struct {
    Path string `json:"path" jsonschema:"required,description=Path to read"`
}

myTool := tools.Must("read_file", "Read a file from the workspace.", func(ctx context.Context, in myInput) (string, error) {
    b, err := os.ReadFile(in.Path)
    if err != nil { return "", err }
    return string(b), nil
})
```

Add it to the `Tools` slice in `buildAgent`.

### 13.2 Add a new model adapter

Implement `model.Model` (see §6.4) and add a case to `modelFor`. The
adapter does not need to know about tools, approval, or the sandbox —
it only translates `Request`/`Response`/`Event`.

### 13.3 Add a new sandbox backend

Implement `sandbox.Sandbox` with `Open`, `Execute`, `Close`. Swap
`DockerSandbox` in `shellTool` for your type. The shell tool does the
rest.

### 13.4 Add a new approval policy

Implement `approval.Broker.Resolve`. The shell tool's `RequireApproval`
setting determines whether the broker is called for a given tool call.

---

## 14. Further reading

- [ZenForge framework](https://github.com/feiyu912/zenforge)
- [ZenForge concepts](concepts.md) — the mental model, the four interfaces, the persistence trio
- [ZenForge provider guide](provider-guide.md)
- [ZenForge config reference](config-reference.md)
- [Anthropic Messages API](https://docs.anthropic.com/en/api/messages)
- [OpenAI Chat Completions](https://platform.openai.com/docs/api-reference/chat)
- [MiniMax platform](https://platform.minimaxi.com/)

---

*Tutorial last verified against the project on the date of publication.
If you find a discrepancy, please open an issue or a PR against the
project repository.*

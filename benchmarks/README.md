# Cross-framework benchmark

This directory compares ZenForge with DeepAgents, LangGraph and Eino on four
things the product brief names: **task success, latency, cost, and recovery**.
It exists because the repository's benchmark story was one Go micro-benchmark
(`BenchmarkAgentRunStaticModel`) and nothing that compares an application-shaped
run with another framework's.

Read this file first: it is the contract every runner implements. The decisions
behind it, the fairness rules, and what the numbers do and do not prove are in
[ADR 0143](../docs/adr/0143-the-cross-framework-benchmark.md).

## What is measured, and why it is the harness

All four frameworks talk to **one endpoint**: a scripted OpenAI-compatible server
in this module (`internal/scripted`). It replays a fixed turn script per request
and records every request body. The model is therefore constant across the
comparison, and the numbers that remain are framework differences:

| Metric | How it is measured | What it means |
| --- | --- | --- |
| **task success** | the harness verifies the task's artifact on disk and the call order the endpoint recorded | whether the framework's loop, tool schemas and approval path actually complete the task |
| **latency** | wall-clock per runner process, minus the model's own service time (the endpoint reports it) | framework overhead: startup, prompt assembly, tool dispatch, checkpointing |
| **cost** | model requests per task, prompt bytes sent upstream, and the share of those bytes spent on tool schemas | what a run spends before the model has answered anything; a real provider bills in proportion to the prompt |
| **recovery** | a task that stops after a durable pause and is finished by a **second, fresh process** | whether the framework can leave a resumable run at all, and what a resume costs |

Cost is reported in bytes rather than dollars on purpose: a dollar figure would
invent a per-token price for a model that is not actually being called, and bytes
are exactly what the endpoint observed. The same bytes bill differently on
different providers; the ratio between frameworks does not.

Nothing here measures model quality. A scripted model answers the same way for
everyone, so a lower token count or a faster loop means the *harness* is leaner,
not that it is smarter. What a real provider would add is a separate, live
measurement that needs a credential; see "Live mode" below.

## The runner protocol

The harness starts each framework as a subprocess. The runner reads its
configuration from the environment and writes its result as JSON, so a runner
needs no shared code and can live in any language.

| Variable | Meaning |
| --- | --- |
| `BENCH_BASE_URL` | OpenAI-compatible base URL of the scripted endpoint (ends in `/v1`) |
| `BENCH_API_KEY` | a placeholder credential the endpoint ignores |
| `BENCH_MODEL` | the model id to request (identical for every framework) |
| `BENCH_TASK` | task id: `edit-file`, `approve-command`, or `durable-task` |
| `BENCH_QUERY` | the frozen task instruction; the runner must send **this text** as the user message |
| `BENCH_WORKSPACE` | absolute path of a fresh workspace the runner may read and write |
| `BENCH_STATE_DIR` | absolute path the runner may use for durable state (checkpoints, threads) |
| `BENCH_PHASE` | `run` for the first process, `resume` for the recovery task's second process |
| `BENCH_APPROVAL` | `approve` or `reject`: how the runner must answer an approval request |
| `BENCH_REQUIRE_PAUSE` | `1` when the harness needs a durable pause after the first process |
| `BENCH_RESULT` | absolute path the runner must write its result JSON to |

`BENCH_QUERY` exists because prompt bytes are a reported cost metric. Without
it every runner would invent its own task sentence, and a runner could win the
cost column by writing a shorter one. A framework's own system prompt and tool
schemas remain its own cost -- that is the difference being measured -- but the
text the user asked for is identical everywhere.

The runner writes exactly this shape to `BENCH_RESULT`:

```json
{
  "task": "edit-file",
  "phase": "run",
  "status": "completed",
  "detail": "free-form, one line",
  "framework": "deepagents 0.7.18"
}
```

`status` is one of:

- `completed` — the runner finished the task (exit 0);
- `paused` — the runner stopped durably with work left, and a later process with
  `BENCH_PHASE=resume` can finish it (exit 75);
- `unsupported` — this framework cannot do what the task requires (for example a
  durable pause); the harness records the cell as *unsupported*, not as a
  failure (exit 78);
- `failed` — the runner tried and did not finish (exit 1, with `detail` naming
  what went wrong).

The runner must not print a report itself. Diagnostics go to stderr; the
harness keeps them when a run fails.

### How the endpoint picks a turn

A script is a list of turns, and the endpoint advances through it by
conversation state rather than by request count, so a framework that asks the
model an extra question is not desynchronized:

- it issues the turn's tool calls with **stable ids** and records which turn
  produced each id;
- a request whose last message is a tool result for one of those ids is served
  the **next** turn;
- any other request is served **the turn it served last** (the script does not
  advance), so a framework that retries the same question gets the same answer;
- once the script is exhausted, the last turn is served again.

A framework that asks the same question twice therefore issues the same tool
call twice and executes the tool twice; the harness records that, because it is
a real cost of the framework's own loop.

## The tasks

Each task is a fixed script for the endpoint plus a fixed tool set, so every
framework is asked for the same behavior. The tools are named identically in all
four runners: `read_file(path)`, `write_file(path, content)`, `run_shell(command)`.

| Task | Script | Success is |
| --- | --- | --- |
| `edit-file` | read `input.txt`, then write `out.txt` | `out.txt` exists with the expected content, and the recorded `read_file` result reached the model before the `write_file` call |
| `approve-command` | call `run_shell` with `echo approved \| tee -a approval.txt`, which needs approval | the command ran exactly once, its stdout reached the model, and the workspace shows its effect; with `BENCH_APPROVAL=reject` the command must **not** have run |
| `durable-task` | write `steps.txt` twice, then call `run_shell` with `echo milestone \| tee milestone.txt`, with `BENCH_REQUIRE_PAUSE=1` | the first process reports `paused` after writing durable state; a second process resumes and finishes the task, and the workspace artifact contains the steps recorded before the pause |

The commands write their output as well as their file on purpose: a runner that
returns the command's real stdout must be able to pass, and an empty-tool-result
verifier would have failed the honest answer.

## Running it

The harness itself is hermetic and needs nothing but Go:

```sh
go test ./...                                  # in this directory
go run ./cmd/bench -tasks all -runners zenforge
go run ./cmd/bench -tasks all -runners all -repeat 3
```

`-repeat N` runs each task N times in fresh workspaces and reports the **median**
wall-clock and overhead, with the sample count. It also asserts that the
deterministic metrics -- request count, prompt bytes, tool-schema bytes -- are
identical across repeats, because the scripts are fixed; a difference there is a
nondeterminism to fix, not something to average. Latency on a scripted endpoint
is almost entirely framework overhead (the model answers in microseconds), which
the report shows as a `model` column near zero rather than pretending otherwise.

The other three runners need their own dependencies, which is why the full
comparison is opt-in:

```sh
# Python runners (DeepAgents, LangGraph)
python3 -m venv .venv && .venv/bin/pip install -r runners/python/requirements.txt

# Eino runner (its own Go module, so Eino's dependencies never enter the SDK's)
(cd runners/eino && go mod download)

go run ./cmd/bench -tasks all -runners zenforge,deepagents,langgraph,eino
```

A missing interpreter or dependency makes the harness report that runner as
*unavailable* and exit non-zero **once**, naming the exact command that installs
it, instead of silently dropping a column from the comparison.

### Live mode

`-live-model <provider>/<model>` replaces the scripted endpoint with the real
provider from the environment, which turns the same task set into a measurement
of a real model's success and cost. That mode needs a credential, so it is not
part of CI; a run of it belongs in a deployment's acceptance record, not here.

## Layout

```text
benchmarks/
  cmd/bench/            the harness: task set, runners, metrics, report
  internal/scripted/    the scripted OpenAI-compatible endpoint
  internal/runner/      the subprocess runner protocol
  runners/eino/         the Eino runner (own module)
  runners/python/       the DeepAgents and LangGraph runners (+ requirements.txt)
  tasks/                the frozen turn scripts
```

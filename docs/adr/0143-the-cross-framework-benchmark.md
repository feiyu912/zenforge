# 0143. A cross-framework benchmark that measures the harness: one scripted endpoint behind four frameworks, and four metrics that are not about model quality

- Status: Accepted
- Date: 2026-09-24
- Related: 0142 (the scripted endpoint and the verified example), 0137 (the
  verification recipe is enforced), 0099 (the console adapter is a layer, not
  the core), 0035 (run time travel)

## Context

The plan asks for a benchmark against DeepAgents, LangGraph and Eino on four
axes: task success, latency, cost, and recovery. The repository had no such
thing. Its "benchmark" was `BenchmarkAgentRunStaticModel`, a Go micro-benchmark
of the agent loop against a static model, plus `TestSQLiteDurableRunSoak` for
durability. Both are useful; neither says anything about an application-shaped
run, and neither compares anything.

A cross-framework comparison is the easiest artifact in this repository to fake.
Four frameworks can be made to look better or worse by choosing the model,
the task, the tool set, the system prompt, the success definition, or the
hardware. The brief's four axes are also not equally measurable: in a real run,
the model dominates success, latency and cost, and it is exactly the thing that
cannot be held constant across frameworks unless it is not a real model.

So the deliverable is a **protocol** first and numbers second. Everything below
exists to make a dishonest number hard to produce by accident.

## Decision

### 1. One scripted endpoint behind all four frameworks

Every runner talks to the same loopback, OpenAI-compatible endpoint, which
replays committed turn scripts and records every request body
(`benchmarks/internal/scripted`). The model is therefore a controlled constant
and the numbers that remain are differences between harnesses.

The cost of that choice is stated up front and in the README: **this measures
the harness, not the intelligence.** A scripted model answers identically for
everyone, so "task success" here means *the framework's loop, tool schemas,
approval path and durability actually complete the task*, and "cost" means
*what the run spends before the model has answered anything* -- prompt bytes,
tool-schema bytes, and the number of requests. Those are the parts a deployment
pays for on every single call, they are fully reproducible, and they are
invisible in every "we scored 84% on SWE-bench" claim. A live measurement with a
real credential conflates them with the model, so it is deliberately a separate,
opt-in mode (`-live-model`), not this.

### 2. Four metrics, each measured by something that cannot be talked into a lie

| Axis | Measurement | Why this and not the obvious alternative |
| --- | --- | --- |
| success | the artifact on disk plus the call order the endpoint recorded | a framework's own "done" message is a claim; the file and the recorded tool results are not |
| latency | wall-clock of the runner process **minus** the endpoint's own reported service time | a raw wall-clock mostly measures the loopback model; the remainder is the framework's startup, prompt assembly, dispatch and checkpointing |
| cost | model requests, total prompt bytes, and tool-schema bytes | a dollar figure would invent a per-token price for a model that is not being called; bytes are what the endpoint observed, and the ratio between frameworks is provider-independent |
| recovery | a task that pauses durably in one process and finishes in a second, fresh process | the only honest test of "recovery capability" is a process that no longer exists |

Cost in bytes rather than tokens is a deliberate refusal to fake precision: a
tokenizer is a property of the provider, and the same request bills differently
on different ones.

### 3. `unsupported` is a first-class result

The runner protocol is environment-in, JSON-out, with four statuses and their
exit codes: `completed` (0), `paused` (75, EX_TEMPFAIL), `unsupported` (78,
EX_CONFIG), `failed` (1). A framework that cannot do what a task requires must
say so, and the harness reports that cell as **unsupported** -- never as a
failure, and never as a success. Both mistakes are common in published
comparisons: scoring "cannot do it" as "failed" overstates a weakness, and
quietly dropping the column understates one.

### 4. Success is verified by the harness, from the artifact and the transcript

Three tasks, one per product scenario: `edit-file` (read then write),
`approve-command` (a command behind an approval, including a reject case), and
`durable-task` (two steps recorded, an approval pause, a resume by a second
process). The turn scripts are committed JSON; the workspace seeds are created
by the harness; and every task's success is computed from what is on disk and
from the order of tool results the endpoint saw. No task is scored by asking the
runner whether it thought it succeeded.

### 5. The fairness rules are part of the contract

- Every runner defines the same three tools with the same names and arguments:
  `read_file(path)`, `write_file(path, content)`, `run_shell(command)`.
- Every runner uses its framework's **mainstream path** -- the SDK's own agent
  loop, its own approval mechanism, its own durable store, its own OpenAI
  client. No runner may hand-roll what the framework ships, and none may
  special-case the benchmark.
- No runner may tune its system prompt to look cheap. Each runner's prompt is
  stated in its README, and the prompt bytes are a measured column rather than a
  hidden one.
- A runner must be a real program that a user could run: it takes its
  configuration from the environment and reports a result, exactly like the
  examples (ADR 0142).

### 6. Layout keeps the dependency graphs apart

`benchmarks/` is its own Go module with a `replace` to the SDK, like
`integration/consumer`. The Eino runner is a further nested module, and the
Python runners are a directory with a pinned `requirements.txt`. None of them
adds a dependency to the SDK's `go.mod`, which the verification below checks.

### 7. CI runs the whole comparison

A dedicated `Benchmark` job installs the pinned Python requirements, downloads
the Eino module, runs the harness's own tests, and then runs all four runners.
The comparison is therefore reproducible by anyone who checks out the commit,
and the harness cannot rot silently behind a README nobody executes. A runner
whose interpreter or dependency is missing is reported as **unavailable** with
the exact install command and makes the harness exit non-zero, instead of
removing a column from the comparison.

### 8. The claims the repository will not make

The numbers below support statements of the form "on this machine, with this
scripted model and these three tasks, framework X sent N requests and M prompt
bytes". They do not support "X is faster than Y" in general, "X is cheaper"
for a workload nobody ran, or any statement about model quality. The README and
`docs/benchmarks.md` are written to that standard, and the raw per-request
transcript is what the numbers are read from.

## Consequences

- The repository gains an executable answer to "what does each framework spend
  before the model answers, and can it resume?" -- which is the question a Go
  service embedding an agent actually asks.
- The comparison costs a CI job that installs roughly 140 MB of Python and a
  nested Go module. That is the price of a reproducible number, and it is
  pinned: an upstream release cannot move the numbers without a commit.
- The Eino runner is a second, independent embedder-shaped program over the
  framework core (the first is `integration/consumer`), so a break in the public
  SDK breaks it too.
- The findings about *what each framework requires for a durable resume* are
  themselves the useful part of the recovery axis; they are recorded per runner
  in `benchmarks/runners/*/README.md` rather than buried in a table cell.

## Verification

The recipe (ADR 0137) plus the benchmark module's own gates, from the repository
root unless a working directory is named:

```text
$ gofmt -l .                                             # in benchmarks/, no output
$ go vet ./...                                           # in benchmarks/ and in
                                                         # benchmarks/runners/eino, clean
$ go test -race ./...                                    # in benchmarks/, all six
                                                         # packages ok; in the Eino
                                                         # module, 29 tests ok
$ go test ./docs/                                        # ok (the format gate now
                                                         # walks the benchmark files)
$ go test -race ./...                                    # at the root, unchanged by
                                                         # this chain (nested modules
                                                         # are skipped), one known
                                                         # sandbox/seatbelt failure
$ cd integration/consumer && go test -race ./... && go vet ./...
ok  github.com/feiyu912/zenforge-consumer-test
$ mkdocs build --strict                                  # green
```

The comparison itself, on the machine this ADR was written on (Apple M5, Darwin
arm64, Go 1.26.2, Python 3.13.13), with every runner installed from the pinned
files:

```text
$ cd benchmarks && go run ./cmd/bench -tasks all \
    -runners zenforge,deepagents,langgraph,eino -repeat 3
```

| Task | Runner | Result | N | Wall (ms, median) | Overhead (ms, median) | Requests | Prompt bytes | Tool-schema bytes | Recovery |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| edit-file | zenforge | success | 3 | 732 | 732 | 3 | 5591 | 3171 | n/a |
| edit-file | deepagents | success | 3 | 1571 | 1571 | 3 | 3904 | 2160 | n/a |
| edit-file | langgraph | success | 3 | 897 | 897 | 3 | 3904 | 2160 | n/a |
| edit-file | eino | success | 3 | 15 | 15 | 3 | 6034 | 3762 | n/a |
| approve-command | zenforge | success | 3 | 1092 | 1092 | 4 | 7152 | 4228 | n/a |
| approve-command | deepagents | success | 3 | 3020 | 3020 | 4 | 4727 | 2880 | n/a |
| approve-command | langgraph | success | 3 | 1776 | 1776 | 4 | 4741 | 2880 | n/a |
| approve-command | eino | success | 3 | 41 | 41 | 4 | 7520 | 5016 | n/a |
| durable-task | zenforge | success | 3 | 1340 | 1340 | 5 | 12091 | 5285 | paused -> completed |
| durable-task | deepagents | success | 3 | 2988 | 2988 | 5 | 8372 | 3600 | paused -> completed |
| durable-task | langgraph | success | 3 | 1791 | 1791 | 5 | 8372 | 3600 | paused -> completed |
| durable-task | eino | success | 3 | 40 | 40 | 5 | 11857 | 6270 | paused -> completed |

The exit status is 0: every cell passed every verifier check, and every cell's
request count and byte counts were identical across its three repeats. The run
was repeated independently by the harness author on the same machine, and the
deterministic columns came out identical while the medians moved by roughly ten
percent, which is the noise a wall-clock median is there to absorb.

### What the table says, including where ZenForge loses

- **Success does not separate these four.** All twelve cells complete, including
  the cross-process durable resume. On three scripted tasks the frameworks agree
  about loops and tools, so the axis that actually differentiates "recovery
  capability" here is what a durable resume *costs to assemble*, not whether it
  is possible (see the second table).
- **ZenForge sends the largest prompts on two of the three tasks** -- 5591 bytes
  against 3904 for both Python frameworks on `edit-file`, and 12091 against 8372
  on `durable-task` -- because its tool schemas are 3171 bytes where
  `langchain-core` builds 2160, and its system prompt is longer. Eino is the
  heaviest on `approve-command` (7520). This is the SDK's own per-call weight and
  it is a real cost; the chain records it rather than tuning the runner's prompt
  to hide it, which is exactly why `BENCH_QUERY` freezes the user text (deviation
  2 below).
- **Eino is the fastest by an order of magnitude** (15-47 ms) because it is the
  only runner that is a compiled binary whose startup the harness measured
  directly. ZenForge follows (732-1340 ms), then LangGraph (897-1791 ms) and
  DeepAgents (1571-3020 ms). The Python numbers are dominated by their own
  interpreter and import cost -- measured by that runner's author at roughly
  800 ms (LangGraph) and 1160 ms (DeepAgents) before the first model call -- plus
  a second process for the durable task. That is a genuine cost of this
  comparison's subprocess-per-run shape and of building an agent per process; it
  is not evidence that the Python loops are slow, and the per-runner READMEs say
  so.
- **No framework wastes model calls.** Requests are 3, 4 and 5 for every runner,
  identical across all four and across repeats. With a scripted model there are
  no provider retries, so this measures the loop's shape rather than its
  efficiency under failure -- the honest limit of the axis.

### What a durable resume cost each ecosystem

| Framework | Durable store | What the runner had to write |
| --- | --- | --- |
| ZenForge | the SDK's `checkpoint/jsonl` | nothing: `Config.Checkpoints` plus `Agent.Resume` |
| LangGraph | ecosystem-shipped `SqliteSaver` (`langgraph-checkpoint-sqlite`, a distribution separate from `langgraph`, which ships only the in-memory saver) | 21 shared lines of `thread_id`/database convention plus 29 lines of pause/resume wiring (14.8% of that runner) |
| DeepAgents | inherits LangGraph's `SqliteSaver`; DeepAgents itself ships no checkpointer | the same 21 shared lines plus 23 lines of wiring (14.6%) |
| Eino | none shipped: v0.9.21 exposes `compose.CheckPointStore` as an alias of an internal interface and has only an unexported in-memory bridge | 161 of 968 non-test lines (about 17%): a file-backed store implementing Eino's own interface, plus the resume sidecar and the pause decision |

Verified by the runners' own modules: `TestDurablePauseThenResumeInFreshProcess`
(Eino) and the Python runners' two-phase runs, whose phase-two artifacts show the
steps written before the pause.

## Deviations

No deviation is left open. The places where the contract had to be pinned down
during implementation, and the boundaries this chain deliberately stops at:

1. **`run_shell` always asks for approval.** The frozen tool signature is
   `run_shell(command)` and carries no per-command "needs approval" flag, so a
   runner cannot distinguish an approval-needing command from any other. Both
   scripts that use it are approval tasks, so every runner interrupts first; the
   Eino runner documented the alternative (add a flag) and rejected it as a
   contract change that would make the runners differ.
2. **`approve-command` is two passes of one row.** The task's success includes
   the reject outcome, so the harness runs the approve pass and the reject pass
   and sums them: 4 requests, not 2. The number is in the table for that reason,
   and a reader who takes it for "one command costs four calls" would be wrong.
3. **The prompt-bytes column needed a frozen user message.** The first iteration
   let each runner write its own task sentence while reporting prompt bytes as a
   cost, which is a metric a runner can win by writing less. `BENCH_QUERY` is now
   the task's exact instruction, every runner must send it as the user message,
   and a verifier fails the cell when no request carried it. A framework's own
   system prompt and tool schemas remain its own cost.
4. **A faithful runner was being failed, and the fix was at the source.** The
   first verifier required a non-empty tool result while the frozen commands
   (`echo approved >> approval.txt`) printed nothing, so the honest answer --
   the command's real, empty stdout -- was a failure, and the Eino runner
   appended `[exit status N]` to pass. The commands are now
   `echo approved | tee -a approval.txt` and `echo milestone | tee milestone.txt`,
   and the verifier requires the command's **stdout** to reach the model. No
   runner decorates its results any more.
5. **`-live-model` is refused with a clear error, not implemented.** A live
   measurement needs a credential and a budget, and it measures the model as much
   as the harness; it belongs to a deployment's acceptance record (the plan's
   item 6), and the flag says so instead of silently running the scripted
   endpoint.
6. **A missing dependency is reported, never dropped.** Without the Python
   virtualenv the harness reports both Python runners as `unavailable`, prints
   the exact install command, and exits non-zero; the comparison never quietly
   becomes a one-horse race.
7. **The endpoint's `usage` is a rough integer and not a metric.** It emits
   `(bytes+3)/4` tokens and only when the request asks for streaming usage, so a
   framework that never reads usage is not penalized. The reported cost figures
   are the bytes the endpoint actually received.
8. **The ZenForge runner's approval request names the SDK's `shell` tool**, not
   the benchmark's `run_shell`, because the wrapper delegates to the SDK tool;
   the benchmark's tool names are what the model sees, and this is cosmetic. It
   is noted in the runner's code rather than papered over with a renamed request.
9. **DeepAgents ships no base prompt for an OpenAI-compatible model**, so its
   prompt bytes are not inflated by one. This affects how the cost column reads
   and is recorded in that runner's README rather than left for the reader to
   guess.
10. **One stale claim in `README.md` was corrected in this chain**: it said
    "Eighty-one architecture decision records" while `docs/adr/` holds 143, and
    the benchmark had no entry on the front door. Both are fixed here because the
    chain edits that file anyway, and a wrong count on the front door is the same
    class of defect these chains exist to remove.

Deliberately **not** done: this chain does not measure a real provider (see 5), it
does not add more tasks, and it does not tune any runner to improve a number.

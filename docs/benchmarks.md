# Cross-framework benchmark

`benchmarks/` compares ZenForge with DeepAgents, LangGraph and Eino on the four
things a team embedding an agent harness actually chooses between: does the task
finish, how much wall-clock does the harness add, how much does a run spend
before the model answers, and can a run survive the process that started it.

The protocol, the fairness rules and the raw layout are in
[`benchmarks/README.md`](https://github.com/feiyu912/zenforge/blob/main/benchmarks/README.md);
the decisions behind them are in
[ADR 0143](https://feiyu912.github.io/zenforge/adr/0143-the-cross-framework-benchmark/).

## What it measures, and what it does not

All four frameworks run the **same three tasks against the same scripted
OpenAI-compatible endpoint**, with the same three tools
(`read_file`, `write_file`, `run_shell`) and the same frozen task instruction.
The model is a constant, so the differences that remain are harness differences.

That is the whole point and also the whole limit:

- **It measures the harness, not the model.** A scripted model does not reason,
  so "success" here means the framework's loop, tool schemas, approval path and
  durability really complete the task — not that the framework would solve a
  hard problem.
- **Cost is bytes, not dollars.** `prompt_b` is the sum of the request bodies the
  endpoint received; `tools_b` is the share of those bytes spent on tool schemas.
  A real provider bills in proportion to this, so the ratio between frameworks is
  the useful part, and no invented per-token price is involved.
- **Latency is overhead, not a service-level promise.** Each runner is a real
  process; the model answers in microseconds, so wall-clock is nearly all
  framework overhead under these conditions, reported as a median of repeated
  runs on one machine.
- **Three tasks are not a benchmark suite.** They cover the three shapes a
  product needs — a read/edit task, a command behind an approval, and a durable
  pause-and-resume — and nothing else.

## Reproduce it

```bash
cd benchmarks
python3 -m venv .venv && .venv/bin/pip install -r runners/python/requirements.txt
(cd runners/eino && go mod download)
go run ./cmd/bench -tasks all -runners all -repeat 3
```

CI runs exactly this in the `Benchmark` job, so the numbers below are
reproducible from the commit they were taken at. The ZenForge runner alone needs
nothing but Go: `go run ./cmd/bench -tasks all -runners zenforge`.

## The comparison

Measured on an Apple M5 (Darwin arm64), Go 1.26.2 and Python 3.13.13, with every
runner installed from the pinned files, and committed to
[ADR 0143](https://feiyu912.github.io/zenforge/adr/0143-the-cross-framework-benchmark/)
with the reasoning behind each column:

| Task | Runner | Result | N | Wall (ms, median) | Overhead (ms, median) | Requests | Prompt bytes | Tool-schema bytes | Recovery |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| edit-file | ZenForge | success | 3 | 732 | 732 | 3 | 5591 | 3171 | n/a |
| edit-file | DeepAgents | success | 3 | 1571 | 1571 | 3 | 3904 | 2160 | n/a |
| edit-file | LangGraph | success | 3 | 897 | 897 | 3 | 3904 | 2160 | n/a |
| edit-file | Eino | success | 3 | 15 | 15 | 3 | 6034 | 3762 | n/a |
| approve-command | ZenForge | success | 3 | 1092 | 1092 | 4 | 7152 | 4228 | n/a |
| approve-command | DeepAgents | success | 3 | 3020 | 3020 | 4 | 4727 | 2880 | n/a |
| approve-command | LangGraph | success | 3 | 1776 | 1776 | 4 | 4741 | 2880 | n/a |
| approve-command | Eino | success | 3 | 41 | 41 | 4 | 7520 | 5016 | n/a |
| durable-task | ZenForge | success | 3 | 1340 | 1340 | 5 | 12091 | 5285 | paused -> completed |
| durable-task | DeepAgents | success | 3 | 2988 | 2988 | 5 | 8372 | 3600 | paused -> completed |
| durable-task | LangGraph | success | 3 | 1791 | 1791 | 5 | 8372 | 3600 | paused -> completed |
| durable-task | Eino | success | 3 | 40 | 40 | 5 | 11857 | 6270 | paused -> completed |

Read it with the limits above in mind, and with three facts the table states
plainly:

- **All four frameworks complete all three tasks**, including the durable pause
  and the resume by a second process. On tasks this deterministic, success does
  not separate the frameworks; it establishes that the comparison is fair.
- **ZenForge sends the largest prompts on two of the three tasks** (5591 bytes
  against 3904 on `edit-file`), because its tool schemas and system prompt are
  heavier than `langchain-core`'s. Eino is heaviest on `approve-command`. These
  are the frameworks' own per-call weight; the benchmark freezes the user message
  (`BENCH_QUERY`) so nobody can win the column by asking in fewer words.
- **Eino's latency is an order of magnitude lower** because it is a compiled
  binary; the Python runners pay their interpreter and import cost in every
  process (roughly 800 ms for LangGraph and 1160 ms for DeepAgents before the
  first model call), and the durable task pays it twice. ZenForge sits between
  them at 732-1340 ms.

## What each framework needed for a durable resume

Recovery capability in this comparison is also a statement about what an
ecosystem hands you:

| Framework | What a cross-process resume took |
| --- | --- |
| ZenForge | nothing beyond the SDK: `checkpoint/jsonl` is a durable store, and `Agent.Resume` reads it |
| DeepAgents | one ecosystem package (`langgraph-checkpoint-sqlite`, installed on top of DeepAgents, which ships no checkpointer) plus a `thread_id` convention: about 44 lines of runner |
| LangGraph | the same ecosystem package, which is where LangGraph's durable `SqliteSaver` lives — `langgraph` itself ships only the in-memory saver: about 50 lines of runner |
| Eino | no durable store ships in `eino` v0.9.21, so the runner implements Eino's own `compose.CheckPointStore` interface: 161 of 968 non-test lines, about 17% of the runner |

None of these runners was allowed to hand-roll what its framework ships, so the
column describes the ecosystem rather than the benchmark's own code.

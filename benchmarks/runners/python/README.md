# Python runners: DeepAgents and LangGraph

Two runners for the cross-framework benchmark, implementing the protocol frozen
in [`benchmarks/README.md`](../../README.md): the `BENCH_*` environment, the
three tools (`read_file(path)`, `write_file(path, content)`, `run_shell(command)`)
confined to `BENCH_WORKSPACE`, the `BENCH_APPROVAL` decision, the `BENCH_RESULT`
JSON, and the four exit codes (0 `completed`, 75 `paused`, 78 `unsupported`,
1 `failed`).

```sh
# install (from benchmarks/)
python3 -m venv .venv && .venv/bin/pip install -r runners/python/requirements.txt

# run every task against both Python runners
go run ./cmd/bench -tasks all -runners deepagents,langgraph
```

| File | Lines | What it is |
| --- | --- | --- |
| `runner_common.py` | 431 | the protocol: env validation, the three tools, the result writer, `ChatOpenAI` construction, the shared system prompt |
| `deepagents_runner.py` | 157 | the DeepAgents runner |
| `langgraph_runner.py` | 196 | the LangGraph runner |
| `requirements.txt` | 36 | the frozen pins plus the two things the runners genuinely needed |

Both runners are started by the harness as
`<python> runners/python/<id>_runner.py`, one process per phase. Neither prints a
report: the outcome line and any traceback go to stderr, and `BENCH_RESULT` is
written on every path, including a bad environment
(`runner_common.run_runner`, `runner_common.py:337`).

## Prompts

The user message is the frozen task instruction from **`BENCH_QUERY`**
(`Config.query`, `runner_common.py:117`). Neither runner invents one: prompt
bytes are a reported cost metric, so a runner-authored sentence would make the
column meaningless. If `BENCH_QUERY` is absent the runner falls back to a
one-line generated instruction and says so on stderr (`runner_common.py:150`) --
loud enough to notice, and identical for both Python runners so the comparison
stays fair.

Both runners send the same authored system prompt (`SYSTEM_PROMPT`,
`runner_common.py:50`). Anything on top of that is the framework's own cost.

## The model

Both runners talk to the scripted endpoint through LangChain's canonical OpenAI
client, `langchain_openai.ChatOpenAI` (`runner_common.build_model`,
`runner_common.py:395`). One setting is not a default: `use_responses_api=False`,
because `langchain-openai` 1.x otherwise sends OpenAI models to `/v1/responses`,
and the frozen endpoint (`internal/scripted`) serves `/v1/chat/completions` only.
Everything else -- message conversion, tool schemas, retries, payload fields --
is ChatOpenAI's own behavior. Neither runner streams; both send `"stream": false`,
which the endpoint serves as a single JSON completion.

## The tools

`runner_common.build_tools` (`runner_common.py:270`) defines the contract's three
tools once, for both runners, so their schemas are identical byte for byte
(2160 bytes of tool schemas for three requests):

* `read_file(path)` returns the file's bytes decoded as UTF-8;
* `write_file(path, content)` writes them and answers with a one-line confirmation
  (its return value is what the `edit-file` and `durable-task` scripts rely on);
* `run_shell(command)` runs a real `/bin/sh -c` subprocess with the workspace as
  its working directory and returns the process's **real** exit code, stdout and
  stderr. Nothing is synthesized: for `echo approved | tee -a approval.txt` the
  result the model sees is `exit code: 0\nstdout:\napproved`.

All three reject a path outside `BENCH_WORKSPACE` (`Workspace.resolve`,
`runner_common.py:221`) and turn a refusal or an `OSError` into an `ERROR:` tool
result rather than a crash.

## DeepAgents runner

`framework`: `deepagents 0.7.18`.

**Which SDK calls carry the task.** One call builds the whole agent:
`deepagents.create_deep_agent` (`deepagents_runner.build_agent`,
`deepagents_runner.py:88`) with

* `model=` the shared `ChatOpenAI`;
* `tools=` the contract's three tools;
* `middleware=[ContractFilesystemMiddleware(...)]`;
* `interrupt_on={ "run_shell": {"allowed_decisions": ["approve", "reject"]} }`;
* `checkpointer=` a `SqliteSaver`;
* `system_prompt=` the shared prompt.

The loop is `agent.invoke(...)`; every model call, tool dispatch, prompt assembly
and message reducer is deepagents'/langchain's own (`create_agent`).

**Approval.** `interrupt_on` is what installs langchain's
`HumanInTheLoopMiddleware`; the runner never intercepts a tool call itself. A
`run_shell` call pauses at LangGraph's `interrupt()` *before* the tool runs. The
operator's answer is delivered with the middleware's documented resume shape,
`Command(resume={"decisions": [{"type": "approve" | "reject", ...}]})`
(`deepagents_runner._decision`, `deepagents_runner.py:105`). On `reject` the
middleware keeps the model's call in the assistant message, adds an error
`ToolMessage`, and the tool does not run -- the recorded conversation still shows
that `run_shell` was requested, which is what the harness verifier reads.

**What it took to make the tool surface exact.** `create_deep_agent(tools=...)`
is additive: it "never removes a built-in". The package's built-in file tools are
`read_file(file_path, offset, limit)`, `write_file(file_path, content)`, plus
`ls`/`edit_file`/`glob`/`grep`, and they operate on a `BackendProtocol`, not on
the workspace. They cannot serve the frozen scripts, which pass `path`, so the
runner registers the contract's tools and removes the built-ins:

* `HarnessProfile.excluded_tools` cannot be used for this. Excluded names are
  stripped from the model request **and rejected at the tool-call boundary** by
  `deepagents.middleware._tool_exclusion._ToolExclusionMiddleware`, so excluding
  `read_file` would also disable the runner's identically named tool;
* instead, `ContractFilesystemMiddleware` (`deepagents_runner.py:68`) subclasses
  `FilesystemMiddleware`, empties `self.tools` after construction (`tools=[]` is
  rejected: "read_file must be included"), and reports `name ==
  "FilesystemMiddleware"` so `create_deep_agent`'s documented merge-by-name
  replaces the default instance instead of appending a second one
  (`_apply_custom_middleware`, `deepagents/graph.py:204` in the installed
  package).

The default general-purpose subagent is disabled with
`register_harness_profile("openai", HarnessProfile(general_purpose_subagent=GeneralPurposeSubagentProfile(enabled=False)))`,
otherwise deepagents adds the `task` tool and its prompt, and the advertised tool
set would be larger than the contract's three.

**Durability.** Same `SqliteSaver` and the same `thread_id` convention as the
LangGraph runner (`bench-<task>`, `runner_common.Config.thread_id`). Phase 1
stops at the interrupt and exits 75; phase 2 calls
`agent.invoke(Command(resume={"decisions": [{"type": "approve"}]}), config)` with
the same `configurable.thread_id`, and the SqliteSaver restores the thread.
Verified working: `durable-task/deepagents` reports `paused -> completed`.

**What deepagents adds to a prompt here: nothing.** Deep Agents 0.7.18 no longer
ships an authored base prompt ("Deep Agents no longer provides an authored base
prompt", `deepagents/graph.py:126-138` in the installed package); the base prompt
comes from a harness profile keyed by provider/model, and
`_harness_profile_for_model` returns an empty profile for a plain `ChatOpenAI`
(checked directly for both `scripted-model` and `gpt-5.4`). The endpoint
therefore sees the runner's system prompt and nothing else, which is why the two
Python runners send byte-identical requests for `edit-file` (3 requests, 3904
bytes total, 2160 bytes of tool schemas each).

## LangGraph runner

`framework`: `langgraph 1.2.12`.

**Which SDK calls carry the task.** The graph is built by hand and is the
framework's documented tool-calling shape (`langgraph_runner.build_graph`,
`langgraph_runner.py:54`):

```
START -> agent --(last message has tool calls?)--> approval --(unanswered calls?)--> tools -> agent
                                                                       \--> END (from agent)
```

* `agent` is `bound_model.invoke(state["messages"])`, where
  `bound_model = model.bind_tools(tools)`;
* `approval` raises `langgraph.types.interrupt(...)` before a `run_shell` call;
* `tools` is `langgraph.prebuilt.ToolNode`, the framework's dispatcher;
* the conditional edges are `after_agent` and `after_approval`
  (`langgraph_runner.py:117`).

**Approval.** `interrupt()` is LangGraph's own HITL primitive; an approval
request is a real interrupt, not a runner-side check. The runner answers it the
way LangGraph documents -- `graph.invoke(Command(resume=<decision>), config)` --
and loops while `result["__interrupt__"]` is set, so a task that needs two
decisions gets two. On `reject` the node keeps the model's call in the assistant
message and appends an error `ToolMessage`; `after_approval` refuses to route a
call that already has a result to `ToolNode`, so the command does not run.

**Durability.** `langgraph.checkpoint.sqlite.SqliteSaver` opened on
`BENCH_STATE_DIR/checkpoints.sqlite` (`langgraph_runner.py:146`). The pause needs
no flag: `interrupt()` ends the run and the saver holds the thread, which is why
phase 1 exits 75 with the checkpoint already on disk. Phase 2 re-enters the same
thread with `Command(resume=...)`; the interrupted node re-executes and
`interrupt()` returns the decision. A resume whose thread has no checkpoint is a
`failed` run that makes no model request (`langgraph_runner.py:149`).

## Assembly cost of a cross-process resume

What each framework needed for a durable pause, stated as a difference:

| | LangGraph | DeepAgents |
| --- | --- | --- |
| extra package | `langgraph-checkpoint-sqlite==3.1.1` (pulls `aiosqlite`, `sqlite-vec`) | the same package -- deepagents ships no saver either |
| extra `pip` install beyond the framework | yes, one package | yes, the same one |
| API argument | `compile(checkpointer=...)` | `create_deep_agent(checkpointer=...)` |
| pause primitive | `interrupt()` in a node | `interrupt_on={...}` -> `HumanInTheLoopMiddleware` |
| resume command | `invoke(Command(resume=<decision>), config)` | `invoke(Command(resume={"decisions":[{"type": ...}]}), config)` |
| convention the runner must add | a stable `thread_id` across the two processes | the same convention, plus the `decisions` envelope |

Runner lines that exist only for durability:

* `langgraph_runner.py`: the `SqliteSaver`/`Command`/`interrupt` imports, the
  `checkpointer=` parameter and `compile(checkpointer=...)`, the `thread_id`
  config, the resume branch, the `Command(resume=...)` calls, the `interrupt()`
  call and the pause raise -- **29 of 196 lines** (28, 32, 54, 83-86, 138, 144,
  146, 149-157, 176-185);
* `deepagents_runner.py`: the same imports, the `checkpointer=` and
  `interrupt_on` arguments, the resume branch, the `decisions` envelope and the
  pause raise -- **23 of 157 lines** (37-38, 88, 100-101, 119, 121, 124-130,
  139-147);
* `runner_common.py`: the `thread_id` and `checkpoint_db` conventions -- 21 lines
  (187-207).

Neither runner reports `unsupported` for `durable-task`: both pause durably and
resume. `unsupported` remains implemented (`RunnerUnsupported`,
`runner_common.py:82`) but no task reaches it.

### The same numbers, in quotable form

**(a) Extra pip packages needed for a durable cross-process resume.**

| Runner | Extra package | Pulls in | Why |
| --- | --- | --- | --- |
| LangGraph | `langgraph-checkpoint-sqlite==3.1.1` | `aiosqlite==0.22.1`, `sqlite-vec==0.1.9` | `langgraph` 1.2.12 depends on `langgraph-checkpoint` 4.2.0, which ships the in-memory saver only |
| DeepAgents | `langgraph-checkpoint-sqlite==3.1.1` | `aiosqlite==0.22.1`, `sqlite-vec==0.1.9` | `deepagents` 0.7.18 depends on `langchain` -> `langgraph` + `langgraph-checkpoint`, and ships no saver of its own |

One package each, the same one, and it is the only dependency either runner
added for durability. (`langchain-openai==1.6.5` plus `tiktoken` and `regex` were
also installed, but that is the model client both runners need to reach the
endpoint at all -- it is not durability.)

**(b) The thread and checkpoint convention.** Identical for both frameworks and
written once in `runner_common.py:187-207` (21 lines):

```python
thread_id      = f"bench-{task}"                        # stable across the two processes
checkpoint_db  = Path(state_dir) / "checkpoints.sqlite" # SqliteSaver.from_conn_string(...)
```

Phase 1 and phase 2 are separate processes with separate environments; the only
thing joining them is this string and this file. The runner passes both to the
framework's own saver.

**(c) Lines that exist only for durability.**

| Runner | Durability-only runner lines | Share of the runner | File:line ranges counted |
| --- | --- | --- | --- |
| LangGraph | 29 of 196 | 14.8% | `langgraph_runner.py` 28, 32, 54, 83-86, 138, 144, 146, 149-157, 176-185 |
| DeepAgents | 23 of 157 | 14.6% | `deepagents_runner.py` 37-38, 88, 100-101, 119, 121, 124-130, 139-147 |
| both | +21 of 431 shared | -- | `runner_common.py` 187-207 (the convention above) |

Neither runner has test files, so these are shares of the whole runner. For
scale: Eino's 161 of 968 (~17%) is the same order; the difference is the size of
the runner, not the amount of recovery plumbing.

**(d) Does the framework ship a durable saver once that package is installed?**

* **LangGraph core ships an in-memory saver only** --
  `langgraph.checkpoint.memory.InMemorySaver` is importable from the core
  distribution (verified), and it cannot survive a process exit. The durable
  `SqliteSaver` comes from the separate `langgraph-checkpoint-sqlite`
  distribution: **shipped by the ecosystem, invoked by the runner, not written by
  the runner.** `langgraph.checkpoint.postgres` is not installed here, so the
  SQLite store is the one this comparison uses.
* **DeepAgents ships no checkpointer at all.** Its declared requirements
  (`langchain`, `langchain-anthropic`, `langchain-core`, `langchain-google-genai`,
  `langsmith`, `packaging`, `wcmatch`) contain no checkpoint package; its
  durability is entirely inherited from the LangGraph ecosystem, so it is the
  same `SqliteSaver`, again not written by the runner.

So on the recovery axis both Python frameworks are in Eino's category in one
sense -- the durable store is external -- and in a different one in another: the
external store exists, is one package, and is shared by both, whereas Eino has no
durable store in its core module at all. What the runner writes is the convention
(21 lines) plus the pause/resume wiring (23-29 lines).

## Where the wall-clock goes

The reported `wall`/`overhead` for a short task is dominated by **Python process
startup and framework imports, not by per-turn work**; the scripted endpoint's own
service time is 0 ms, so `overhead` is essentially the whole row. Measured on this
machine, median of 5 fresh processes each, timing the runners' real construction
path and making no model call:

| Phase | LangGraph | DeepAgents |
| --- | --- | --- |
| interpreter start | ~15 ms | ~15 ms |
| `import runner_common` (langchain-core, `langchain_openai`/`openai`) | ~700 ms | ~700 ms |
| framework modules imported | ~740 ms cumulative | ~1078 ms cumulative |
| `ChatOpenAI` construction | ~54 ms | ~45 ms |
| graph/agent build + `SqliteSaver` open | ~9 ms | ~36 ms |
| **process ready to call the model** | **~802 ms** | **~1163 ms** |

Against the harness rows below (`edit-file` 984 ms / 1561 ms in the median table below), startup is 80-90% of wall-clock; for `durable-task` it is paid
twice, once per process, which is why that row is roughly two startups plus two
model loops. This is a real cost of a subprocess-per-run comparison, and it is a
property of the Python import graph -- deepagents pulls in more of `langchain`
than langgraph alone does, which is worth ~340 ms of imports per process. It is
not evidence that either framework's runtime loop is slow, and it should not be
read as such.

## Where an exact match was impossible

1. **`use_responses_api=False`.** `langchain-openai` 1.x defaults OpenAI models
   to the Responses API; the frozen endpoint only implements chat completions, so
   the canonical client must be pointed at the served path
   (`runner_common.build_model`). No request shape is emulated: the client is
   ChatOpenAI's.
2. **DeepAgents' built-in tools cannot be excluded per name and cannot accept the
   frozen argument names.** Covered above; the resolution is
   `ContractFilesystemMiddleware` plus `create_deep_agent(tools=...)`. A runner
   that simply passed `tools=` would advertise both `read_file(file_path, ...)`
   and `read_file(path)`, and the scripted call would hit one of them arbitrarily.
3. **DeepAgents' default subagent would change the tool surface.** `task` is
   added unless a `HarnessProfile` disables it; the runner registers one. This is
   a runner-side fix to keep the tool set identical to the contract, not a
   framework behavior change.
4. **The rejection path had to keep the model's call visible.** The first
   implementation of the LangGraph approval node dropped the rejected call from
   the assistant message before the next model request. The harness verifier then
   reported `the endpoint never saw a run_shell call for "echo approved >>
   approval.txt"`, because the request record is the only proof the model asked
   for the command. Fixed by keeping the call and letting `after_approval`
   withhold it from `ToolNode` (`langgraph_runner.py:87-127`). This is a real,
   recorded difference in how the two frameworks deliver a denial: langchain's
   HITL middleware also keeps the call and answers it with an error `ToolMessage`.
5. **deepagents 0.7.18 contributes no base prompt for an OpenAI-compatible
   model** (verified directly, not inferred). Its prompt is harness-profile
   driven, and only `openai:gpt-5.4` ships a profile in this install. The finding
   for the comparison: for this endpoint the DeepAgents column is *not* paying
   for a large authored prompt, and the honest reason is that the package has no
   authored prompt to pay for.
6. **The reject pass costs a few more bytes on LangGraph than on DeepAgents**
   (4741 vs 4727 prompt bytes for `approve-command`): the rejection `ToolMessage`
   text is the runner's on one side and langchain's middleware wording on the
   other. Recorded, not normalized.
7. **`run_shell`'s result text is the runner's, not the framework's.** Both
   runners use the same `Workspace.run_shell` formatting (`exit code: N`, then
   `stdout:`, then `stderr:`), so the tool-result bytes are identical across the
   Python columns; what differs between frameworks is only how the result is
   wrapped into a message.

## Verification performed

Direct, with a throwaway endpoint: the benchmark's own `internal/scripted`
package was compiled into a small `main` under `/tmp` (it is an `internal`
package, so it cannot be imported from outside the module), and the frozen
`tasks/*.json` scripts were replayed to each runner in turn. The driver in
`/tmp/zf-pyrunner-test/drive.py` asserts the observable outcome, not the exit
code: artifact bytes, the recorded `read_file` result preceding the `write_file`
call, the command having run exactly once (approve) or not at all (reject), the
command's real stdout reaching the model, the checkpoint file on disk before the
resume, and the resumed request carrying both steps recorded before the pause.
Result: **76 checks, 0 failures**.

Through the harness, repeated three times per cell with `-repeat 3` (the table
below is that run's output; `n` is how many fresh workspaces each cell ran in,
and the deterministic columns are asserted identical across the repeats rather
than averaged):

```sh
cd benchmarks && go run ./cmd/bench -tasks all -runners deepagents,langgraph -repeat 3
```

```
TASK             RUNNER      RESULT   N  WALL_MS  OVERHEAD_MS  REQUESTS  PROMPT_B  TOOLS_B  RECOVERY
edit-file        deepagents  success  3  1561     1561         3         3904      2160     n/a
edit-file        langgraph   success  3  984      984          3         3904      2160     n/a
approve-command  deepagents  success  3  3079     3079         4         4727      2880     n/a
approve-command  langgraph   success  3  1854     1854         4         4741      2880     n/a
durable-task     deepagents  success  3  3179     3179         5         8372      3600     paused -> completed
durable-task     langgraph   success  3  1845     1845         5         8372      3600     paused -> completed
```

Spread over the three repeats, per cell (ms):

| Cell | LangGraph min/median/max | DeepAgents min/median/max |
| --- | --- | --- |
| `edit-file` | 957 / 984 / 1021 | 1541 / 1561 / 1690 |
| `approve-command` | 1851 / 1854 / 1910 | 2963 / 3079 / 3356 |
| `durable-task` | 1815 / 1845 / 1879 | 2973 / 3179 / 3265 |

All 6 cells pass all 132 harness verifier checks across the repeats (0
failures). The correctness columns are what the runners are responsible for;
`overhead` (wall-clock minus the endpoint's own service time) differs here --
DeepAgents costs roughly 1.6-1.7x LangGraph's per-run overhead on this machine,
and the [startup measurement](#where-the-wall-clock-goes) says why: it is import
and construction cost, not per-turn work. The cost columns are identical except
for the rejection message, because both send the same frozen prompt and the same
three tool schemas.

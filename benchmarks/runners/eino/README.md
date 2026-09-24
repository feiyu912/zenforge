# Eino runner

The Eino runner for the cross-framework benchmark in [`../README.md`](../README.md).
It is its own Go module so Eino's dependency graph never enters the SDK module's:

```sh
cd benchmarks/runners/eino
go mod download
go test ./... -race
go build -o /tmp/eino-runner .        # the harness builds exactly this
```

The harness builds and drives it at `benchmarks/internal/harness/runners.go`
(`prepareEino`, `go build -o <tmp>/eino-runner .`).

Module path: `github.com/feiyu912/zenforge/benchmarks/runners/eino`.
Pinned dependencies: `github.com/cloudwego/eino v0.9.21`,
`github.com/cloudwego/eino-ext/components/model/openai v0.1.13`.
`framework` in the result JSON is read from the build's own dependency list at
run time (`debug.ReadBuildInfo`), so it reports `eino 0.9.21` without a constant
to drift.

## Which layer this runner uses, and why

**It uses the ADK layer (`github.com/cloudwego/eino/adk`), not
`github.com/cloudwego/eino/flow/agent/react`.**

`react.NewAgent` is the obvious "use the framework's ReAct agent" choice, and it
is what this runner would use for a task with no durable pause. It cannot carry
`durable-task`, because its internal graph is compiled with a frozen option list
that has no persistence hook:

```
flow/agent/react/react.go:386
    compileOpts := []compose.GraphCompileOption{compose.WithMaxRunSteps(config.MaxStep),
        compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithGraphName(graphName)}
```

`AgentConfig` exposes no `GraphCompileOption` and therefore no
`compose.WithCheckPointStore`. The ADK's `ChatModelAgent` is the same ReAct loop
with the hook the contract needs:

```
adk.RunnerConfig{Agent: …, CheckPointStore: store}   // adk/runner.go:68
adk.WithCheckPointID(id)                             // adk/interrupt.go:192
```

That is the only reason to prefer it: one framework, one agent, the layer that
can be durable. For `edit-file` and `approve-command` both layers would work; the
runner uses one agent construction for all three tasks so the comparison reads
framework behaviour rather than two hand-tuned loops.

## Which Eino API carries each task

| Task | Mechanism | Exact API |
| --- | --- | --- |
| `edit-file` | The ADK ReAct loop; tool schemas advertised through `model.WithTools` | `adk.NewChatModelAgent` + `adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: …}}`; `adk.Runner.Query` |
| `approve-command` | A tool-level interrupt answered **in the same process** through Eino resume data | `tool.StatefulInterrupt` → ADK turns it into `event.Action.Interrupted` → `adk.ResumeWithParams(ctx, id, &adk.ResumeParams{Targets: {interruptID: "approve"/"reject"}})`; the tool reads the answer with `tool.GetResumeContext[string]` |
| `durable-task` | A tool-level interrupt answered by a **second, fresh process** from Eino's own checkpoint | `compose.CheckPointStore` (alias of `adk.CheckPointStore`) written to `BENCH_STATE_DIR`; `adk.WithCheckPointID`; then `adk.ResumeWithParams` in the second process |

Everything else is the framework's own machinery:

- tool declaration: `components/tool/utils.InferTool` reflects the argument
  structs into the JSON Schema the endpoint receives;
- interrupt state persistence: `schema.RegisterName[shellInterruptState]` — the
  ADK checkpoint is gob-encoded, so a value behind `any` must be registered
  before Eino can encode it;
- interrupt detection: `compose.IsInterruptRerunError`;
- model transport: `eino-ext/components/model/openai.NewChatModel` returning a
  `model.ToolCallingChatModel` pointed at `BENCH_BASE_URL`, non-streaming
  (`adk.RunnerConfig.EnableStreaming = false`).

### The pause/resume path, step by step

1. `run_shell` is called for the first time. It does **not** execute anything: it
   returns `tool.StatefulInterrupt(ctx, info, shellInterruptState{Command: cmd})`.
2. Eino's `ToolsNode` aggregates that into a composite interrupt, the
   `ChatModelAgent` surfaces it, and `adk.Runner` writes a checkpoint **before**
   delivering the event (`adk/runner.go`: `runnerSaveCheckPointImpl`, then
   `gen.Send`). The checkpoint goes to `fileCheckPointStore` → a file under
   `BENCH_STATE_DIR`.
3. With `BENCH_REQUIRE_PAUSE=1` the driver records the interrupt id in a small
   sidecar and exits 75 with `status: "paused"`.
4. `BENCH_PHASE=resume` starts a brand-new process. It rebuilds the agent,
   points a new `adk.Runner` at the same store, and calls
   `ResumeWithParams(ctx, checkpointID, &ResumeParams{Targets: {interruptID: approval}})`.
5. Eino reloads the gob checkpoint, restores the message history, the pending
   tool call and the interrupt state, replays the interrupted `run_shell` call
   with the same call id, and the tool then runs the command. The loop continues
   to the script's next turn.

## Recovery capability: assembly cost

The benchmark asks what a framework costs, so the durability work is measured
rather than described.

**Eino v0.9.21 ships no durable `CheckPointStore`.** Searching the module for
implementations of the two-method interface finds only:

- `internal/core/interrupt.go:31` — the interface itself;
- `adk/interrupt.go:369` — `bridgeStore`, unexported and in-memory (it exists to
  move compose checkpoint bytes into a resume, not to persist them);
- test doubles in `*_test.go` files.

The public names are aliases of an internal type: `compose.CheckPointStore`
(`compose/checkpoint.go:53`) and `adk.CheckPointStore` (`adk/runner.go:64`), both
`= core.CheckPointStore`. So the durable store is an application
responsibility, and the interface is the sanctioned way to meet it.

What that cost here, counted in lines that exist **only** for durability:

| Piece | File | Lines |
| --- | --- | --- |
| `CheckPointStore` implementation (Get/Set + optional `CheckPointDeleter`, atomic rename, hashed key names) | `store.go:28-85` | 58 |
| Resume sidecar (which interrupt ids the second process answers) | `store.go:87-123` | 37 |
| `resumeFromCheckpoint` (second-process entry) | `agent.go:118-140` | 23 |
| Pause decision in the event driver | `agent.go:204-228` | 25 |
| Store/runner/checkpoint-id wiring | `agent.go:93-104` | 12 |
| `Config.CheckpointID` | `protocol.go:116-121` | 6 |
| **Total** | | **161** |

Non-test source is 968 lines, so roughly **17% of this runner exists to obtain a
cross-process resume**. None of it is conversation state: the entire graph,
history and pending tool call live in Eino's checkpoint, encoded by Eino. The
sidecar holds only the operator decision the checkpoint API does not carry —
*which interrupt the second process answers, and with what answer* — which is the
same datum an approval UI would have to keep between two HTTP requests.

It works: the first process leaves `checkpoint-<hash>.gob` plus
`eino-resume.json`, the second process finishes the task, and the artifact
written before the pause survives. See the evidence section.

## What the framework needed, and where it bit

### Findings that are framework behaviour, not bugs to hide

1. **Tool schemas dominate the prompt.** `utils.InferTool` emits JSON Schema
   2020-12 with `additionalProperties: false` and a description per argument, and
   the ADK re-sends the whole `tools` array on every request. Measured on
   `edit-file`'s first request: a 1727-byte body of which 1254 bytes are the
   `tools` array (72.6%) and only 368 characters are the system + user preamble.
   Across the harness run, `tools` was 3762 of 6007 prompt bytes (63%). The
   preamble in that measurement was this runner's fallback sentence; with the
   frozen `BENCH_QUERY` the preamble grows, but the point stands — Eino's tool
   schema is the dominant, repeated, per-request cost, and it is reported as
   measured rather than trimmed.

2. **A tool's `Info` is not persisted, its `State` is.** `tool.StatefulInterrupt`
   stores state in the checkpoint, but the `info` passed to `tool.Interrupt` is
   documented as not persisted and is only visible in the in-process
   `InterruptCtx.Info`. A cross-process resume therefore cannot recover the
   user-facing interrupt description; this runner recovers the command from the
   replayed tool arguments and the persisted state instead.

3. **`utils.InferTool` wraps errors with `%w`.** The interrupt signal survives
   (`compose.IsInterruptRerunError` still reports it), but the error text gains a
   `[LocalFunc] failed to invoke tool, toolName=…` prefix. Worth knowing before
   matching on error strings.

4. **An interrupt inside a non-targeted tool must re-interrupt.**
   `tool.GetResumeContext` returns `isResumeTarget = false` for any interrupted
   leaf the resume did not name. Eino's documented strategy is that such a leaf
   re-issues its interrupt rather than proceeding; `runShellWithApproval`
   implements exactly that, and the driver targets every reported tool interrupt
   id so a parallel tool-call turn converges instead of looping.

5. **The `jsonschema` struct tag is comma-delimited.** `jsonschema:"required,description=Read the file, relative to the root."`
   is silently truncated at the comma after "file". The first version of these
   three tools shipped descriptions that stopped mid-sentence; the fix is Eino's
   documented separate `jsonschema_description:"…"` tag
   (`components/tool/utils/doc.go:39-47`).

### Deviations from the contract, and why

1. **`BENCH_REQUIRE_PAUSE=1` with a run that never interrupts reports `failed`,
   not `completed`.** The harness's `durable-task` verifier requires phase `run`
   to report `paused`
   (`tasks/verify.go`, `requirePhase(verdict, in, PhaseRun, "paused")`). If the
   script never produced an interrupt, claiming `completed` would leave the
   resume phase with nothing to resume. Detail names
   `BENCH_REQUIRE_PAUSE` explicitly. No such run was observed — it is a guard,
   not a special case.

2. **`run_shell` always requests approval.** The contract fixes the signature at
   `run_shell(command)` and never sends a per-command "needs approval" flag, so
   the tool cannot distinguish an approval-needing command from any other. Every
   `run_shell` call interrupts first; that is true for both tasks that use it and
   matches the frozen scripts.

3. **Defaults for unset environment variables.** `BENCH_PHASE` defaults to
   `run` and `BENCH_APPROVAL` defaults to `approve` when absent, as the contract
   does not say they are always present. Every genuinely required variable
   (`BENCH_BASE_URL`, `BENCH_MODEL`, `BENCH_WORKSPACE`, `BENCH_STATE_DIR`,
   `BENCH_RESULT`) is validated by name, and a validation failure still writes a
   `failed` result to `BENCH_RESULT` and exits 1.

4. **`BENCH_QUERY` has a fallback, and it is announced.** When the harness passes
   the frozen task instruction in `BENCH_QUERY`, it is used verbatim. When the
   variable is absent — as it is in a harness that predates it — the runner falls
   back to a fixed per-task sentence so it can still run, and prints a stderr
   warning saying the prompt-bytes column for that run is **not** comparable. A
   silent fallback would corrupt exactly the metric the variable exists to fix.

### The user message is the frozen instruction, not this runner's wording

`BENCH_QUERY` carries the task instruction and the runner sends it **verbatim**
as the user message (`userQuery` in `agent.go`; the only transformation is
trimming surrounding whitespace). Anything the runner says in its own voice is
confined to the **system prompt** (`baseInstruction`), because that is Eino's own
framework cost and therefore a legitimate measured difference. Prompt bytes are a
reported metric; an invented user prompt would make the column measure the
runner instead of the framework.

`TestEndToEndUserMessageIsBenchQueryVerbatim` and
`TestRunMainProtocolEndToEnd` assert the exact bytes on the wire, and
`TestUserQueryUsesBenchQueryVerbatim` pins the verbatim path and the fallback.

### The tool result is the command's output, with nothing appended

`run_shell` returns the command's combined stdout+stderr **verbatim**. The exit
status is not part of the tool result; a non-zero exit is written to the
runner's stderr for logging and the command's real output is still returned as
the tool result (a failing command is information for the model, not a reason to
abort the run). Code path: `execShell` in `tools.go`.

An earlier revision appended `[exit status N]` so a command that printed nothing
would still satisfy the task verifier's non-empty-tool-result check. That was a
workaround for a verifier artefact, and it would have made Eino's tool result
incomparable with every other framework's. The frozen commands were changed at
the source instead (`echo approved | tee -a approval.txt`,
`echo milestone | tee milestone.txt`), and this runner dropped the suffix;
`TestEndToEndApproveCommand` asserts the tool result is exactly `"approved\n"`.

### Exact-match impossibilities

None. The three tool names and their arguments match exactly —
`read_file(path)`, `write_file(path, content)`, `run_shell(command)`, asserted by
`TestToolNamesAndSchemas` — the user message is the frozen instruction byte for
byte, and the tool result is the command's output byte for byte. The earlier
empty-result collision was removed at its source rather than worked around here.

## Tests

`go test ./... -race` in this module. Nothing touches the network; the
end-to-end test starts a loopback OpenAI-compatible server in-process.

| File | What it proves |
| --- | --- |
| `protocol_test.go` | the exact five-key result JSON, the 0/75/78/1 exit-code mapping (including that an unknown status is a failure), `BENCH_RESULT` round trip, `BENCH_*` validation by name |
| `workspace_test.go` | lexical workspace confinement: relative and absolute paths inside the root resolve, `../escape`, `sub/../../escape`, `/etc/passwd` and absolute-outside paths are refused, and nothing outside is created |
| `store_test.go` | `CheckPointStore` lifecycle (missing / set / get / overwrite / delete), key collision freedom, a hostile checkpoint id cannot escape `BENCH_STATE_DIR`, sidecar round trip |
| `tools_test.go` | the three contract names and argument schemas, `read_file`/`write_file` confinement, `run_shell` interrupts **without** running the command, and `execShell` runs a real subprocess with the workspace as cwd |
| `agent_test.go` | the whole Eino graph with a scripted in-process model: completion, reject-does-not-run, approve-runs-exactly-once, and **pause in one Execute call then finish in a second fresh Execute call** |
| `endtoend_test.go` | the real OpenAI adapter against a loopback endpoint that implements the contract's turn-selection rule: artifact bytes, the `read_file` result reaching the model before the `write_file` turn, approve/reject observable outcomes, the two-phase durable task, and `runMain`'s result file + exit code |

## Evidence

`go vet ./...` clean, `go test ./... -race` clean. Against the harness's own
scripted endpoint, subprocess protocol and verifier:

```sh
cd benchmarks && go run ./cmd/bench -tasks all -runners eino
```

```
run edit-file/eino: success wall=645ms model=0ms overhead=645ms requests=3 prompt=6007B tools=3762B recovery=n/a
run approve-command/eino: success wall=58ms model=0ms overhead=58ms requests=4 prompt=7541B tools=5016B recovery=n/a
run durable-task/eino: success wall=57ms model=0ms overhead=57ms requests=5 prompt=11687B tools=6270B recovery=paused -> completed

TASK             RUNNER  RESULT   WALL_MS  OVERHEAD_MS  REQUESTS  PROMPT_B  TOOLS_B  RECOVERY
edit-file        eino    success  645      645          3         6007      3762     n/a
approve-command  eino    success  58       58           4         7541      5016     n/a
durable-task     eino    success  57       57           5         11687     6270     paused -> completed
```

`approve-command` reports 4 requests and 7541 prompt bytes because the harness
runs it twice, approve and reject. There is no `unsupported` cell: the durable
pause and the cross-process resume are real, and were not faked.

**This table predates the harness's `BENCH_QUERY` change.** It was captured while
the harness still set no query and still used `echo approved >> approval.txt`, so
the prompt bytes include this runner's fallback sentence, and the approve case
passed only because that revision still appended `[exit status N]`. The runner has
since been changed: it sends `BENCH_QUERY` verbatim and returns the tool result
undecorated. Both behaviours are covered by tests against the revised contract
(see above), and this table will be re-captured once the harness change lands.

# Long Task Agent

The checkpoint-and-resume example: a long task stops in the middle, durably,
and a second process finishes it.

The agent works through the task one step at a time. The step that closes the
task out (`finalize_task`) needs a human decision, so it returns an
approval-required result instead of doing the work. With no approval broker
configured, ZenForge checkpoints the waiting request and stops the run: no
terminal event, no final answer, and a checkpoint on disk that a later process
can pick up. That later process calls `Agent.Resume`, answers the pending
decision, and the run finishes with the conversation, the tool results, and the
task state it had before the pause -- not a restarted task.

## Run it

Start a run. `-workspace` is where the task log and the final report are
written; `-run-dir` is the durable run directory (also settable with
`ZENFORGE_RUN_DIR`).

```sh
export ZENFORGE_PROVIDER=openai
export ZENFORGE_MODEL=your-model
export ZENFORGE_API_KEY=your-key
export ZENFORGE_BASE_URL=https://your-endpoint.example/v1

go run ./examples/long-task-agent \
  -task "Audit the workspace step by step, record every step, then finalize the report." \
  -workspace . \
  -run-dir .zenforge/long-task
```

The process stops at the decision and exits `75` (`EX_TEMPFAIL`: the same
command, run later, is expected to succeed). It prints the id to resume and the
exact command:

```text
approval: requested finalize_task
run: incomplete long_1739401483123456789
run: resume with: long-task-agent -run-dir .zenforge/long-task -workspace . -resume long_1739401483123456789
```

Resume it. The resumed process prompts for the decision on stderr and reads the
numbered choice from stdin; `1` approves the pending call (`2` rejects it).

```sh
go run ./examples/long-task-agent \
  -run-dir .zenforge/long-task \
  -workspace . \
  -resume long_1739401483123456789
# Approval required: Finalize the long task
# Write long-task-report.md and close out the task log.
# Risk: medium
# 1. Approve
# 2. Reject
# > 1
```

The run then performs the approved call and answers:

```text
run: resumed long_1739401483123456789
approval: requested finalize_task
approval: finalize_task approve
tool: finalize_task
step: 4
run: done long_1739401483123456789
answer: audit summary: two steps recorded, report written
```

Both invocations must see the same `-run-dir` and the same `-workspace`, and
the resuming process must register the same tools: the checkpoint holds a
pending `finalize_task` call, and `Resume` looks that tool up by name.

## What the interruption is

It is a waiting approval, and that is deliberate:

- A run that reaches `MaxSteps` is **not** a resumable stop. `harness/runner.go`
  appends a "You have reached the tool-use limit" instruction, makes one more
  model call with `ToolChoiceNone`, and ends the run `run.done` in phase
  `completed`. `Agent.Resume` on such a checkpoint replays the terminal event
  (`agent.go`: `resumeTerminal`), so there is nothing left to continue. The
  example therefore keeps `-max-steps` generous (default 6) so the run reaches
  the decision instead of the finalization path.
- Cancelling the run's context is terminal too: it checkpoints phase
  `cancelled`, and a resume only replays that outcome.
- A waiting approval is the one public-API stop that leaves a genuinely
  resumable run. `harness/runner.go` reports `errApprovalPending`, the run ends
  with no terminal event, and the checkpoint keeps phase `approval` with
  `Approval.Waiting` and the active tool call set.

The pause is deterministic because nothing about it is a race: the approval is
reached by the model's scripted (or real) tool call, not by a timer, a signal,
or a step count, and the paused checkpoint is a distinct durable phase. A test
can assert the phase, the waiting request, and the tool result from before the
pause.

## What the resumed process recovers

- the conversation, including the `tool` messages for the steps recorded before
  the pause -- the resumed model request carries them, which is what proves the
  checkpoint was reloaded instead of the task restarted;
- the pending tool call and its exact checkpointed arguments, which the harness
  retries once the decision approves it;
- the prompt context and model route frozen into the run's `Meta`, so a resume
  reproduces the original run's prompting instead of rediscovering it;
- the workspace artifacts: `long-task.log` holds the steps recorded before the
  pause, and the approved `finalize_task` writes `long-task-report.md`
  containing them.

## Which SDK calls carry each part

| What | Call |
| --- | --- |
| Durable checkpoints | `checkpointjsonl.New(runDir)` on `zenforge.Config.Checkpoints` |
| Durable event log | `eventlogjsonl.New(runDir)` on `zenforge.Config.Events` |
| The run id to resume | `zenforge.Task{RunID: ...}`, echoed back in `zenforge.Result.RunID` |
| The pause | a tool returning `approval.RequiredResult(req), approval.ErrRequired`; with `Config.Approval` unset, `Agent.Run` returns `(*Result, approval.ErrRequired)` |
| The resume | `Agent.Resume(ctx, runID)` (returns `<-chan Event`) |
| The decision | `approvalcli.New(stdin, os.Stderr)` on `Config.Approval` of the resuming agent |
| The final answer | the `run.done` event's `output` payload |

The example prints one transcript line per observable step:
`run: started`, `run: resumed`, `step:`, `tool:`, `approval:`, `checkpoint:`,
`run: incomplete`, `run: done`, and `answer:`. A run checkpoints far more often
than it takes a step (every model attempt draft and every tool boundary), so
the `checkpoint:` lines are the run's durability cadence, not a step count.
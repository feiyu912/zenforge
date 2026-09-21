# ADR 0121: The Turn and Step Boundaries the Console Reads

Status: accepted

## Context

Two of the console's session events were missing or incomplete, and one of them was
missing for a reason that was invisible from the projection code.

- **`turn/start` was never sent.** The console anchors its `turn-process` node on
  it: `match: (event) => { if (event.type === "turn/start") return { id:
  String(event.data.turn), role: "start" }; … }`. Without the marker the turn has
  no start, so the live step timeline and the turn's token/latency row have nothing
  to hang off. The same marker retires the client's optimistic first prompt
  (`if (event.type === "turn/start") this.firstPromptPendingTurn = false`).
- **`step/end` was declared but never written.** The projection had always mapped a
  durable `step.done` to the console's `step/end`, and the console consumes it —
  `if (event.type === "step/end") { … state = { kind: "settled", turn, step } }` in
  the assistant-attempt reducer, and `previous.finalized` for the live row's
  anchor. But `harness/runner.go` emitted `step.started` and never a completion, so
  the record could not exist: a step was left open in the console's state machine
  for the whole run, and every later step inherited that.

## Decision

- **The runner closes a step honestly.** `RuntimeStepDone` (`step.done`) is emitted
  when the step's model call has settled **and** every tool call it asked for has
  resolved: once after `RunPendingTools` returns, and once immediately for a step
  whose model call asked for no tools. A step is therefore never reported as
  finished while one of its tools is still running.
- **The projector emits `turn/start` as the first record of a turn**, carrying
  `{turn}` and nothing else, with no surface operation. It is emitted from the
  run's opening durable event.
- **The question follows the marker immediately**, rather than being held back until
  the step that answers it opens. Upstream records the question inside the step, but
  holding it back would hide a prompt the operator just submitted until the model's
  first step begins, and this host has no separate echo to carry it in the meantime.
  The console does not require the upstream order: its turn row keys off
  `turn/start` and its step rows off `step/start`, so the question renders where it
  belongs either way.
- **One durable event may produce several records**, and every one of them is served.
  The projection drains a queue (`Next`/`Pop`), and the live tail sends each record
  the append produced rather than only the last.

## Consequences

- Live-verified against the real model (qwen-plus, one prompted turn):
  `turn/start#1 {turn:1}`, `user/message#2`, `session/title#3`, `session/title#4`,
  `step/start#5 {step:1,turn:1}`, `assistant/message#6`,
  `step/end#7 {step:1,turn:1}`, `turn/end#8 {reason:{kind:"completed"},turn:1}`.
- Latent bug found and fixed on the way: the live tail sent only
  `tail.Events[len-1]` after an append, so the first record of a multi-record event
  was dropped **while still being counted** in the served sequence. That left every
  later frame numbered one past the cursor the console held — the reconnect rule the
  console enforces. The socket test that pins the next turn's first frame now also
  pins the second.
- The served record count changes: eleven records per prompted turn instead of ten,
  and the page-window and message-identity tests were updated to the measured
  numbering rather than to arithmetic.
- Still missing in this area, and stated as such: `turn/end` carries three of the
  console's six reasons; a `session/page` record is enriched by the page layer with
  the addressed `turn`/`step`, which the console tolerates but upstream's page does
  not add; and a step whose model call is the tool-limit final answer is not opened
  or closed by boundary records.
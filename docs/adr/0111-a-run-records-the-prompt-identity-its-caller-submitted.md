# ADR 0111: A Run Records the Prompt Identity Its Caller Submitted

Status: accepted

Completes [ADR 0110](0110-a-projected-message-is-identified-by-the-session-sequence.md)'s
note that this host projects no `source.rpcId`, and relates to
[ADR 0105](0105-the-durable-log-is-projected-into-the-consoles-vocabulary.md) (the
projection that reads the identity) and [ADR 0109](0109-the-console-host-keeps-its-run-registry-on-disk.md)
(the console's own bookkeeping as this host's concern).

## Context

The operator asked a question in the console and the question appeared **twice**: once as
the transcript's message, and again after the answer:

```
hello
08:58

Hello! 👋

This is a greeting — no plan needed. What would you like to work on?

08:58
hello
```

The second copy is the console's own **local submission echo**. It paints the prompt the
moment the operator submits it and retires it when the durable message arrives — matched
strictly by the request identity the prompt RPC carried
(`webui/dsh/plugins/api/session-controller/client.js`):

```js
observeSubmissionEvent(event) {
    if (event.type !== "user/message") return;
    const source = event.data?.source;
    if (source?.kind !== "user" || typeof source.rpcId !== "string") return;
    this.scheduleObservedRetirement(source.rpcId, ...);
}
```

and the transcript hides it in the same render by the same identity
(`webui/dsh/plugins/client/ui-chat/client.js`: `observedRpcIds`, "the echo→durable swap is
atomic — no duplicate, no gap"). A successful prompt RPC does not retire it: only observing
the durable message does.

This host projected the prompt as `"source": {"kind": "user"}` — an identity was never
written, so the console could never retire the echo. The session's durable log shows it: the
prompt is one message and no second submission ever became a turn, because there was no
second submission — only its echo, still on screen.

The same arm hid a second defect: a prompt that arrives while a run is live is delivered as a
queued turn, which the harness records as `request.steer` with the text under `message`.

```go
if err := emit(EventRequestSteer, map[string]any{"steerId": steer.ID, "message": steer.Message}); err != nil {
```

The projection read `input`, `text` and `content` but not `message`, so a queued question
never became a user message at all: the console showed the operator's local echo and nothing
durable ever replaced it.

## Decision

A run records the identity of the prompt that started it, and the projection publishes it as
the console's `source.rpcId`.

- The deep API carries it as `Task.PromptID` (the console's `requestId`), checkpointed as
  `harness.RunState.PromptID` and emitted as `run.started`'s `promptId` — next to the input
  it identifies, so the run's own log states which submission its first turn answers.
- The host sets it on **every** turn it starts, the first and each continuation, because the
  console retires one echo per submission.
- A queued turn takes its text from `request.steer`'s `message` and its identity from
  `steerId`, which the host sets to the prompt's `requestId` when it queues the turn.
- A run nobody labelled — a CLI run, a resumed run — records no identity, and the projected
  source keeps its previous shape.

## Consequences

- A submitted question stops appearing twice: the console retires the echo in the frame the
  durable message is renderable, and the question stands in the transcript exactly once.
- A queued turn is visible at all, and visible as the operator's own message rather than only
  as the echo of one.
- The identity is durable and part of the run's log, so any process, window, or restart
  projects the same `source.rpcId`; it is carried, not invented at read time.
- The console's identity is the caller's, not the console's: a run started by anything else
  that supplies an identity projects it too, and nothing in the harness reads or validates
  what the caller meant by it.
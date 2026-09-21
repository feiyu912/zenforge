# ADR 0115: An Approval Names the Conversation, Not the Turn

Status: accepted

## Context

The operator's console showed a turn that never finished:

```
Running
Tool call
shell · go build ./...
Waiting for approval            ← no panel under it, no buttons anywhere
```

The turn was real and genuinely waiting: the run's durable log ended at
`approval.requested`, `session/list` reported it running, and the host's `$events`
stream answered the probe with the waterfall it had delivered:

```json
{"type":"waterfall","event":"approval/request",
 "eventId":"approval_run_1789960573715264000~3_call_84c4ee475f474000bd021b_shell_command_…",
 "agentId":"run_1789960573715264000~3",
 "request":{"callId":"call_84c4ee475f474000bd021b","operation":"shell.command",
            "risk":"high","title":"Approve shell command","toolName":"shell"}}
```

The host was delivering it, so the console was dropping it. The shipped panel
answers only a request it can attach to a session it has open:

```js
async function answerApproval(ctx, owner, request, next, registerPendingInteraction) {
    const sessionId = ctx.sessions.scopeOf(owner);
    if (sessionId === void 0) return next();     // no panel, no prompt
    ...
}
```

`agentId` is what resolves that scope, and the recon already documented the field
as `"agentId":"<session id>"`. This host wrote `request.RunID` into it, and a
conversation's later turn runs under `<session>~<turn>` (ADR 0108) — an id the
console never opened and cannot resolve. So a **first** turn's approval rendered
(it is its own session id) and a later turn's did not, which is why the same
host's approvals had looked healthy and this one hung.

## Decision

An `approval/request` waterfall names the **conversation**:

- `dshsession.Base(request.RunID)` is written as the waterfall's `agentId`, so a
  later turn's request reaches the session the console has open. The first turn is
  unchanged: its base is itself.
- The request id (`eventId`) still names the turn it belongs to, and the answer
  still routes by `clientId` + `eventId` alone, so naming the conversation cannot
  misroute a decision.
- Nothing else about the waterfall changes: the client-safe request payload, the
  fail-closed answer rules, and the cancellation frame on a withdrawn request are
  as ADR 0083 and this stream's tests left them.

## Consequences

- The operator's hang is fixed and pinned: with the old value the new test fails
  with `agentId = "run_…~2", want the session id "run_…"`, which is exactly the
  request the panel dropped.
- Verified in a browser against a real model, on the failing shape: turn 1 asks for
  a shell command, the panel appears, "Allow once" runs it; turn 2 asks for a
  second shell command, and the panel **appears again** — `Waiting for approval` /
  `Reject` / `Allow once` — and answering it finishes the turn (`2 turns 3 steps`).
- A pending approval is no longer a state the console can display without offering
  a way out, which is also what makes Stop usable on that turn (ADR 0113 cancels
  the conversation's newest turn).
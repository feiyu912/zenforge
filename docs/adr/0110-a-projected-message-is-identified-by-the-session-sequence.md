# ADR 0110: A Projected Message Is Identified by the Session Sequence, Not the Run's

Status: accepted

Corrects [ADR 0108](0108-a-sessions-log-is-one-sequence-across-its-turns.md) (whose
decision claimed the message identity `msg-<seq>` was unique while the implementation
still derived it from the run's own numbering) and relates to
[ADR 0105](0105-the-durable-log-is-projected-into-the-consoles-vocabulary.md) (the
projection that writes the identity).

## Context

The operator asked a question in the console, read the answer, asked again, and the
transcript showed the **earlier** question again:

```
hello
08:58

Hello! 👋

This is a greeting — no plan needed. What would you like to work on?

08:58
hello
```

The projected record for a prompt is its durable `run.started`, and the projection named it
from the event's own sequence:

```go
base := Event{Seq: p.identity.SeqOffset + event.Seq, Time: event.Timestamp, Data: data}
...
return p.surface(base, "user/message", userMessage(event.Seq, text))   // run-local seq
...
"id": messageID(event.Seq)                                            // run-local seq
```

Every zenforge run numbers its own log from one, so the first prompt of **every** turn was
projected as `msg-1`. ADR 0108 had already made a session's turns one sequence
(`SeqOffset`, so turn two's first record is seq 19, not 1), but the identity kept the
run-local number, which is the one coordinate in that projection that must not be local.

The shipped console matches a message node by exactly that identity
(`webui/dsh/plugins/client/ui-chat/client.js`):

```js
match: (event) => event.type === "user/message" && isAppendSurfaceEvent(event)
    ? { id: String(event.data.id), role: "start" } : null,
```

so the second turn's prompt did not start a node: it matched turn one's node, and the
question already on screen is what the transcript kept. Measured on the operator's host
after the fix was written, before it was deployed:

```
$ session/page on every listed session, ids of user/assistant/tool-result messages
sessions checked: 27 | with duplicated message ids: 21
  run_1789915960885194000 records 98 turns [1, 2] dups {'msg-1': 2}
  ... the same `msg-1` collision in every conversation of more than one turn
```

## Decision

A projected message names itself with the record's **session sequence** — `msg-<seq>` where
`seq` is the number the console cursors on — for the user prompt, a steering submission, the
assistant message, and the tool-result message. The run's own sequence never reaches the
wire as an identity.

The shift is already derived from the durable event counts (ADR 0108), so the identity stays
stable: any window, any process, and any restart reproduces `msg-1` for the conversation's
first prompt and `msg-<offset+n>` for a later turn's.

## Consequences

- A second question renders as itself. Every message in a session's sequence has a distinct
  identity, which is what the console's node match requires.
- The identity is a function of the session's durable shape, not of storage: it is
  recomputed, never stored, exactly like the sequence it comes from.
- A page, a snapshot window, and a live tail agree on the same identities, because all three
  read the same projection.
- The console's local echo of a submission is matched to its durable occurrence by
  `source.rpcId`. This host did not project one, which
  [ADR 0111](0111-a-run-records-the-prompt-identity-its-caller-submitted.md) fixes: the run
  records the caller's identity for its prompt, and the projected message carries it.
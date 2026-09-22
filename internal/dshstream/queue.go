package dshstream

import "github.com/feiyu912/zenforge/internal/dshwire"

// The pending queue, as the console reads it.
//
// A message a console submits while a turn is running is not in the transcript
// yet: it waits in the host's run queue until the agent reaches a model-turn
// boundary, and the console renders it from the `inbox` **projection cell**
// meanwhile. That cell is upstream's own shape
// (`@deepseek-ai/dsh-agent-loop`'s inboxProjectionDefinition, and
// `SessionProjectionMap.inbox: {next-turn, next-step}` of `UserMessage[]`), and
// two console surfaces read it:
//
//   - the queue dock, which lists `next-turn` rows with an edit, remove and
//     steer action for each (client/ui-conversation QueueDock reads
//     `useProjection("inbox")["next-turn"]`), and the "steer everything" chord,
//     which walks the same list calling `session/updateQueue(id, {kind: "steer"})`;
//   - the chat view's pending-steering strip, which reads `next-step`, and the
//     submission-echo retirement both lists feed (a local echo whose
//     `source.rpcId` appears in either list is dropped, because the host now
//     owns the row: api/session-controller sessions/manager.ts
//     observeSubmissionInbox).
//
// Which list an item belongs to is the console's own classification of when the
// run will receive it: `next-turn` is a prompt awaiting a turn of its own, and
// `next-step` is input awaiting the current turn's next step boundary. This host
// delivers both at that boundary -- one prompt path, one queue (ADR 0111) -- and
// says so by keeping each item in the list its prompt mode asked for: a queued
// prompt in `next-turn`, a steering prompt in `next-step` (ADR 0130).
//
// The item's id and its `source.rpcId` are the same string. The steer id the
// prompt path hands the harness is the console's own requestId, so it is the one
// identity that names the row, the local echo, the queue mutation and the
// durable user message the run finally records.

// inboxProjectionKey is the projection name the console's queue reads.
const inboxProjectionKey = "inbox"

// QueueItem is one message waiting for the run to reach its next model-turn
// boundary: the identity the console queued it under, and its text.
type QueueItem struct {
	ID   string
	Text string
}

// QueueState is one session's pending queue, split the way the console splits
// it, with the sequence that orders its frames. The zero value is the honest
// answer for a session with nothing pending.
type QueueState struct {
	// NextTurn holds prompts awaiting a turn of their own (the prompt mode the
	// console calls "queue").
	NextTurn []QueueItem
	// NextStep holds input awaiting the current turn's next step boundary (the
	// prompt mode the console calls "steer").
	NextStep []QueueItem
	// Seq orders the frames a client receives. It is this host's own monotone
	// counter rather than a session-log watermark, and it deliberately outranks
	// any session cursor: the console's history seed installs the projections
	// block at the cursor's watermark and discards a frame numbered at or below
	// it, while a pending message is not a session-log event at all -- it has no
	// cursor of its own to be numbered by.
	Seq int64
}

// QueueUpdate is one committed queue change, as the control stream's projection
// frame needs it: the session, the whole new cell value and its sequence. The
// cell is small and always sent whole, because a client that missed one frame
// must not be left rendering a row that no longer exists.
type QueueUpdate struct {
	SessionID string
	State     QueueState
}

// inboxCell renders a queue as the `inbox` cell's JSON value: both lists are
// always present, because the console reads the key to decide whether this host
// serves the queue at all -- an omitted key is a capability it never had, while
// an empty list is a queue that is empty right now.
func inboxCell(state QueueState) any {
	cell := map[string]any{
		"next-turn": []any{},
		"next-step": []any{},
	}
	if len(state.NextTurn) > 0 {
		cell["next-turn"] = inboxMessages(state.NextTurn)
	}
	if len(state.NextStep) > 0 {
		cell["next-step"] = inboxMessages(state.NextStep)
	}
	return cell
}

// inboxMessages renders queued items as the console's user messages. The id is
// the steer id rather than a session-sequence message id: a pending message has
// no durable position yet, and the id the console sends back in a queue mutation
// has to be the one the host can find it by.
func inboxMessages(items []QueueItem) []any {
	messages := make([]any, 0, len(items))
	for _, item := range items {
		messages = append(messages, dshwire.UserMessage(item.ID, item.Text, item.ID))
	}
	return messages
}

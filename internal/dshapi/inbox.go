package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/internal/dshwire"
)

// The durable inbox (ADR 0136).
//
// Upstream does not keep pending input in memory: the agent loop splices it into
// the session's own event log (`agent/inbox/spliced`) and the `inbox` projection
// cell is a *fold* over those splices
// (`@deepseek-ai/dsh-agent-loop`'s inboxProjectionDefinition). That is what makes
// a queued message survive the run it was queued for -- and it is the shape this
// file ports, because the event type is already in the console's vocabulary
// (`KNOWN_SESSION_EVENT_TYPES`), the console *reads* it (its submission observer
// treats `splice.inserted` as durable acceptance), and the fold's validation is
// fully specified.
//
// The splice payload is the reference's, field for field:
//
//	{target: "next-turn"|"next-step", start: int, removedCount?: int,
//	 inserted: UserMessage[], outcome?: "canceled"}
//
// `removedCount` is omitted when nothing was removed and `outcome` only appears
// on a cancellation, exactly as the reference writes them.
const (
	// inboxEventType is the session event that carries one pending-input change.
	inboxEventType = "agent/inbox/spliced"
	// inboxNextTurn is the list of prompts awaiting a turn of their own.
	inboxNextTurn = "next-turn"
	// inboxNextStep is the list of input awaiting the running turn's next step.
	inboxNextStep = "next-step"
	// inboxCanceled marks a splice that discarded input rather than delivering it.
	inboxCanceled = "canceled"
)

// inboxState is the folded pending queue: the two lists, in order.
type inboxState struct {
	NextTurn []dshstream.QueueItem
	NextStep []dshstream.QueueItem
}

// inboxMessage renders one pending message the way the reference stores it: the
// console's own user-message shape, whose `id` and `source.rpcId` are both the
// identity the console queued it under.
func inboxMessage(id, text string) map[string]any {
	return dshwire.UserMessage(id, text, id)
}

// inboxSplice builds one splice payload. A zero `removedCount` is omitted and
// `outcome` is written only for a cancellation, because the fold distinguishes
// "nothing removed" from "removed nothing" by the key's absence and the reference
// schema declares both fields optional.
func inboxSplice(target string, start, removedCount int, inserted []any, canceled bool) map[string]any {
	payload := map[string]any{"target": target, "start": start}
	if removedCount > 0 {
		payload["removedCount"] = removedCount
	}
	if inserted == nil {
		inserted = []any{}
	}
	payload["inserted"] = inserted
	if canceled {
		payload["outcome"] = inboxCanceled
	}
	return payload
}

// foldInbox replays one session's splices into the pending lists. It is the
// reference's `apply`, including its two validations: a splice whose range leaves
// its list is rejected, and an id that would be pending in both lists at once is
// rejected -- the log is the state, so a log that cannot be folded is a host bug
// rather than something to answer around.
//
// The events are already the session's: they come from the turns whose ids derive
// from it, which is why the splice payload carries no session id -- the reference
// stores none either.
func foldInbox(events []zenforge.Event) (inboxState, error) {
	state := inboxState{NextTurn: []dshstream.QueueItem{}, NextStep: []dshstream.QueueItem{}}
	for _, event := range events {
		if string(event.Type) != inboxEventType {
			continue
		}
		splice, err := decodeInboxSplice(event)
		if err != nil {
			return inboxState{}, err
		}
		list := state.NextTurn
		if splice.target == inboxNextStep {
			list = state.NextStep
		}
		removed := splice.removedCount
		if splice.start < 0 || splice.start > len(list) || removed < 0 || splice.start+removed > len(list) {
			return inboxState{}, fmt.Errorf("invalid persisted inbox splice at session seq %d", event.Seq)
		}
		next := make([]dshstream.QueueItem, 0, len(list)-removed+len(splice.inserted))
		next = append(next, list[:splice.start]...)
		for _, message := range splice.inserted {
			next = append(next, dshstream.QueueItem{ID: message.id, Text: message.text})
		}
		next = append(next, list[splice.start+removed:]...)
		if splice.target == inboxNextStep {
			state.NextStep = next
		} else {
			state.NextTurn = next
		}
		if err := assertInboxIdentities(state); err != nil {
			return inboxState{}, err
		}
	}
	return state, nil
}

// inboxSpliceEvent is one decoded splice: the fields the fold needs, and nothing
// else. A malformed field is an error rather than a zero value, because a splice
// the fold cannot read is not a splice that removed nothing.
type inboxSpliceEvent struct {
	target       string
	start        int
	removedCount int
	inserted     []inboxSpliceMessage
}

// inboxSpliceMessage is one message inside a splice: the identity and the text.
type inboxSpliceMessage struct {
	id   string
	text string
}

// decodeInboxSplice validates one event's payload. The reference's schema is the
// contract: the target is one of the two lists, `start` is a non-negative integer,
// `removedCount` is optional and non-negative, `inserted` is an array of console
// user messages, and `outcome` is only ever `canceled`.
func decodeInboxSplice(event zenforge.Event) (inboxSpliceEvent, error) {
	fail := func(reason string) (inboxSpliceEvent, error) {
		return inboxSpliceEvent{}, fmt.Errorf("invalid persisted inbox splice at session seq %d: %s", event.Seq, reason)
	}
	target, _ := event.Payload["target"].(string)
	if target != inboxNextTurn && target != inboxNextStep {
		return fail(fmt.Sprintf("target %q is not one of the two pending lists", target))
	}
	start, ok := inboxInt(event.Payload["start"])
	if !ok || start < 0 {
		return fail("start is not a non-negative integer")
	}
	removed := 0
	if raw, present := event.Payload["removedCount"]; present {
		value, ok := inboxInt(raw)
		if !ok || value < 0 {
			return fail("removedCount is not a non-negative integer")
		}
		removed = value
	}
	if raw, present := event.Payload["outcome"]; present {
		if outcome, _ := raw.(string); outcome != inboxCanceled {
			return fail(fmt.Sprintf("outcome %q is unknown", outcome))
		}
	}
	rawInserted, ok := event.Payload["inserted"].([]any)
	if !ok {
		return fail("inserted is not an array")
	}
	inserted := make([]inboxSpliceMessage, 0, len(rawInserted))
	for _, raw := range rawInserted {
		message, ok := raw.(map[string]any)
		if !ok {
			return fail("inserted holds a message that is not an object")
		}
		id, _ := message["id"].(string)
		if id == "" {
			return fail("inserted holds a message with no id")
		}
		inserted = append(inserted, inboxSpliceMessage{id: id, text: inboxMessageText(message)})
	}
	return inboxSpliceEvent{target: target, start: start, removedCount: removed, inserted: inserted}, nil
}

// inboxMessageText reads the text back out of a stored console user message. The
// reference stores the wire message, so the only place its text lives is the
// content block the console renders; a message with no text block contributes no
// text rather than a fabricated one.
func inboxMessageText(message map[string]any) string {
	content, _ := message["content"].([]any)
	var builder strings.Builder
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := block["type"].(string)
		if kind != "text" {
			continue
		}
		text, _ := block["text"].(string)
		builder.WriteString(text)
	}
	return builder.String()
}

// assertInboxIdentities enforces the reference's one cross-list invariant: a
// message may be pending in the inbox only once, whichever list it is in.
func assertInboxIdentities(state inboxState) error {
	ids := map[string]bool{}
	for _, item := range append(append([]dshstream.QueueItem{}, state.NextTurn...), state.NextStep...) {
		if ids[item.ID] {
			return fmt.Errorf("message %q is already pending", item.ID)
		}
		ids[item.ID] = true
	}
	return nil
}

// inboxInt reads a JSON integer from a payload that may hold any number type,
// because one event's payload round-trips through the log's own decoder.
func inboxInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if value != float64(int(value)) {
			return 0, false
		}
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

// queueEvents reads one session's raw events for the inbox fold.
func (h *Handler) queueEvents(sessionID string) ([]zenforge.Event, error) {
	events, failure := h.sessionEvents(context.Background(), sessionID)
	if failure != nil {
		return nil, errors.New(failure.message)
	}
	return events, nil
}

// queueRuns names a session's turns, which is how the store finds the live run
// whose queue proves a delivery.
func (h *Handler) queueRuns(sessionID string) []string {
	return h.sessionRunIDs(context.Background(), sessionID)
}

// queueAppend records one inbox splice in the session's newest turn.
func (h *Handler) queueAppend(sessionID string, payload map[string]any) error {
	_, err := h.appendSessionEvent(context.Background(), sessionID, inboxEventType, payload)
	return err
}

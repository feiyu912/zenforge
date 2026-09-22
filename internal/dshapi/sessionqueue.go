package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// sessionUpdateQueue answers POST /api/session/updateQueue: one operation on one
// message the console has queued but the run has not been given yet (ADR 0130).
//
// The request is `{sessionId, itemId, action}`, where the action union is
// `{kind: "edit", content: ContentBlock[]}`, `{kind: "remove"}` or
// `{kind: "steer"}` -- the console's `SessionUpdateQueueParameter`, whose only
// answer is `{accepted: true}`
// (api/session-controller/session_updateQueue_parameter_0$schema). The item id is
// the steer id this host queued the message under, which is also the prompt
// identity the console knows the row by.
//
// Every refusal here is the reference's own, with its own sentence, because the
// console shows them: an edit carrying anything but text is
// `session/attachment-invalid`, an edit with no text at all is
// `gateway/bad-request`, an item the queue no longer holds -- delivered, dropped,
// or never queued -- is `session/queue-item-not-found`, and steering something
// that is not a queued turn, or a turn that has stopped accepting input, is
// `session/steer-unavailable` (dsh-api-session-controller ApiSessionList
// .updateQueue). The console converges silently on the last two, which is why
// they are answers rather than errors.
func (h *Handler) sessionUpdateQueue(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	// Every field the client's SessionUpdateQueueParameter carries, and nothing
	// else: the action union is inside `action`, so the top level is exactly three
	// names.
	if failure := rejectUnknownArguments(args, "sessionId", "itemId", "action"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	itemID, _, failure := stringArg(args, "itemId")
	if failure != nil {
		return nil, failure
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, argumentRequired("itemId")
	}
	action, failure := updateQueueAction(args["action"])
	if failure != nil {
		return nil, failure
	}
	item, ok := h.queues.item(sessionID, itemID)
	if !ok {
		return nil, fail(codeQueueItemNotFound, "queued item is no longer pending",
			map[string]any{"itemId": itemID})
	}
	switch action.kind {
	case "edit":
		if err := h.manager.EditSteer(item.RunID, itemID, action.text); err != nil {
			return nil, queueEditFailure(itemID, err)
		}
		// The row the console holds is derived from the run queue, so a client
		// learns about the edit from a republished cell -- not from this answer,
		// which only acknowledges that it was applied.
		h.queues.refresh(sessionID)
	case "remove":
		if err := h.manager.DropSteer(item.RunID, itemID); err != nil {
			return nil, queueEditFailure(itemID, err)
		}
		h.queues.refresh(sessionID)
	case "steer":
		// Steering is "hand this to the running turn now", which only a queued
		// turn can be asked for. A message already awaiting the next step boundary
		// is in exactly the state the action asks for, and the reference refuses
		// that request rather than reporting a change it did not make.
		if item.Mode != promptModeQueue {
			return nil, fail(codeSteerUnavailable, "current turn no longer accepts steering",
				map[string]any{"itemId": itemID})
		}
		h.queues.promote(sessionID, itemID)
	}
	return map[string]any{"accepted": true}, nil
}

// queueAction is one decoded operation from the action union: which operation,
// and for an edit the text it carries.
type queueAction struct {
	kind string
	text string
}

// updateQueueAction decodes the action union. A kind outside the union is an
// argument failure rather than a method answer: the envelope named an operation
// this host does not have, and there is nothing to accept or refuse.
func updateQueueAction(raw json.RawMessage) (queueAction, *methodError) {
	if len(raw) == 0 {
		return queueAction{}, argumentRequired("action")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return queueAction{}, fail(codeArgumentsInvalid, `"action" must be an object`, nil)
	}
	kind, _, failure := stringArg(object, "kind")
	if failure != nil {
		return queueAction{}, failure
	}
	action := queueAction{kind: strings.TrimSpace(kind)}
	switch action.kind {
	case "edit":
		if failure := rejectUnknownArguments(object, "kind", "content"); failure != nil {
			return queueAction{}, failure
		}
		text, failure := queueEditText(object["content"])
		if failure != nil {
			return queueAction{}, failure
		}
		action.text = text
	case "remove", "steer":
		if failure := rejectUnknownArguments(object, "kind"); failure != nil {
			return queueAction{}, failure
		}
	default:
		return queueAction{}, fail(codeArgumentsInvalid,
			`"action.kind" must be "edit", "remove" or "steer"`, map[string]any{"kind": action.kind})
	}
	return action, nil
}

// queueEditText turns the edit's content blocks into the one text the queue
// holds. The console sends a single text block; anything else in the array is
// the reference's own refusal, because this host's queue stores text and nothing
// else, and pretending to have stored an attachment would lose it silently.
func queueEditText(raw json.RawMessage) (string, *methodError) {
	if len(raw) == 0 {
		return "", fail(codeBadRequest, "queue edit content must include non-whitespace text", nil)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fail(codeArgumentsInvalid, `"action.content" must be an array`, nil)
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(block, &object); err != nil {
			return "", fail(codeArgumentsInvalid, `"action.content" must hold content blocks`, nil)
		}
		blockType, _, failure := stringArg(object, "type")
		if failure != nil {
			return "", failure
		}
		if strings.TrimSpace(blockType) != "text" {
			return "", fail(codeAttachmentInvalid, "queue edits accept text content only",
				map[string]any{"reason": "QUEUE_EDIT_NON_TEXT"})
		}
		text, _, failure := stringArg(object, "text")
		if failure != nil {
			return "", failure
		}
		texts = append(texts, text)
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	if text == "" {
		return "", fail(codeBadRequest, "queue edit content must include non-whitespace text", nil)
	}
	return text, nil
}

// queueEditFailure maps the run manager's answers onto the console's own code for
// a row that is not there any more. A run this process does not hold, a run that
// has ended, and an id that is not pending are the same thing to the console; the
// other errors are the host's own failure and keep their own answer.
func queueEditFailure(itemID string, err error) *methodError {
	switch {
	case errors.Is(err, harnesshttp.ErrSteerNotFound), errors.Is(err, harnesshttp.ErrRunNotFound):
		return fail(codeQueueItemNotFound, "queued item is no longer pending",
			map[string]any{"itemId": itemID})
	default:
		return fail(codeInternal, "update the queued item: "+err.Error(), nil)
	}
}

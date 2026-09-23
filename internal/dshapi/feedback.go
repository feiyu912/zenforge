package dshapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
)

// Message feedback is the console's per-message judgment: a Like/Dislike pair on
// a finalized assistant message, an optional note and category, and a version
// that makes a concurrent second edit a *conflict* instead of a silent overwrite.
// The reference keeps it as Session-log events -- `feedback/message-put` and
// `feedback/message-delete`, folded on read (dsh-message-feedback/lib/index.js) --
// and this host does the same, because the vocabulary already reserves the types
// and the fold is what makes the answer durable without a store of its own.
//
// Two conventions of this namespace differ from the rest of the surface, and both
// are copied from the vendored schema rather than chosen here:
//
//   - The method *succeeds* (`result.ok: true`) and the outcome rides inside the
//     value as a union: `{ok: true, value: {...}}` or
//     `{ok: false, error: {code: ..., ...}}`. The console reads the union, so a
//     business refusal must not be a method-level error.
//   - The item's `version` is a random UUID minted per write, and `ifVersion` is
//     the version the client last observed (`null` when it observed none).
type messageFeedbackItem struct {
	MessageID string `json:"messageId"`
	Rating    string `json:"rating"`
	Note      string `json:"note,omitempty"`
	Category  string `json:"category,omitempty"`
	Version   string `json:"version"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// feedbackMaxNoteBytes is the reference host's configured note policy
// (dsh-web-app/cordis.patch.yml: `maxNoteBytes: 8192`), so a note the reference
// accepts is not refused here.
const feedbackMaxNoteBytes = 8192

// feedbackCategories is the console's closed category set, in the order the
// vendored schema declares it.
var feedbackCategories = []string{
	"other",
	"task-result",
	"instruction-following",
	"product-interaction",
	"service-stability",
	"resource-cost",
	"security-privacy-permission",
}

// messageFeedbackPut answers POST /api/messageFeedback/put.
func (h *Handler) messageFeedbackPut(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId", "messageId", "rating", "note", "category", "ifVersion"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	messageID, _, failure := stringArg(args, "messageId")
	if failure != nil {
		return nil, failure
	}
	if messageID == "" {
		return nil, argumentRequired("messageId")
	}
	rating, _, failure := stringArg(args, "rating")
	if failure != nil {
		return nil, failure
	}
	if rating != "positive" && rating != "negative" {
		return nil, fail(codeArgumentsInvalid, `"rating" must be "positive" or "negative"`,
			map[string]any{"argument": "rating", "value": rating})
	}
	note, present, failure := stringArg(args, "note")
	if failure != nil {
		return nil, failure
	}
	if present && strings.TrimSpace(note) == "" {
		return feedbackValue(map[string]any{"ok": false, "error": map[string]any{"code": "note-blank"}})
	}
	if present && len(note) > feedbackMaxNoteBytes {
		return feedbackValue(map[string]any{"ok": false, "error": map[string]any{
			"code": "note-too-large", "maxBytes": feedbackMaxNoteBytes, "actualBytes": len(note),
		}})
	}
	category, hasCategory, failure := stringArg(args, "category")
	if failure != nil {
		return nil, failure
	}
	if hasCategory && !feedbackCategory(category) {
		return nil, fail(codeArgumentsInvalid, `"category" is not a feedback category`,
			map[string]any{"argument": "category", "value": category})
	}
	// ifVersion is required by the schema and is either a version or null.
	rawVersion, presentVersion := args["ifVersion"]
	if !presentVersion {
		return nil, argumentRequired("ifVersion")
	}
	ifVersion := ""
	hasIfVersion := false
	if string(rawVersion) != "null" {
		value, _, failure := stringArg(args, "ifVersion")
		if failure != nil {
			return nil, failure
		}
		ifVersion, hasIfVersion = value, true
	}

	if !h.sessionKnown(ctx, sessionID) {
		return feedbackSessionNotFound(sessionID)
	}
	events, failure := h.sessionEvents(ctx, sessionID)
	if failure != nil {
		return nil, failure
	}
	items, err := foldMessageFeedback(sessionID, events)
	if err != nil {
		return nil, fail(codeInternal, "read message feedback: "+err.Error(), nil)
	}
	if !h.assistantMessageExists(ctx, sessionID, messageID) {
		return feedbackValue(map[string]any{"ok": false, "error": map[string]any{
			"code": "target-not-found", "sessionId": sessionID, "messageId": messageID,
		}})
	}
	existing, found := items[messageID]
	observed := ""
	if found {
		observed = existing.Version
	}
	if (hasIfVersion && ifVersion != observed) || (!hasIfVersion && observed != "") {
		return feedbackValue(map[string]any{"ok": false, "error": map[string]any{
			"code": "version-conflict", "current": feedbackCurrent(existing, found),
		}})
	}
	// A write that changes nothing keeps the version and appends no event: the
	// console's Like button is idempotent, and an event per click would fill the
	// log with duplicates.
	if found && existing.Rating == rating && existing.Note == note && existing.Category == category {
		return feedbackValue(map[string]any{"ok": true, "value": existing})
	}
	now := time.Now().UnixMilli()
	createdAt := now
	if found {
		createdAt = existing.CreatedAt
		if existing.UpdatedAt > now {
			now = existing.UpdatedAt
		}
	}
	item := messageFeedbackItem{
		MessageID: messageID, Rating: rating, Note: note, Category: category,
		Version: newFeedbackVersion(), CreatedAt: createdAt, UpdatedAt: now,
	}
	payload := map[string]any{"sessionId": sessionID, "item": feedbackItemPayload(item)}
	if _, err := h.appendSessionEvent(ctx, sessionID, "feedback/message-put", payload); err != nil {
		return nil, fail(codeInternal, "commit message feedback: "+err.Error(), nil)
	}
	return feedbackValue(map[string]any{"ok": true, "value": item})
}

// messageFeedbackList answers POST /api/messageFeedback/list with every current
// item of one session, in the order the log first recorded them.
func (h *Handler) messageFeedbackList(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	if !h.sessionKnown(ctx, sessionID) {
		return feedbackSessionNotFound(sessionID)
	}
	events, failure := h.sessionEvents(ctx, sessionID)
	if failure != nil {
		return nil, failure
	}
	items, err := foldMessageFeedback(sessionID, events)
	if err != nil {
		return nil, fail(codeInternal, "read message feedback: "+err.Error(), nil)
	}
	rows := make([]messageFeedbackItem, 0, len(items))
	for _, id := range feedbackItemOrder(sessionID, events) {
		if item, ok := items[id]; ok {
			rows = append(rows, item)
		}
	}
	return feedbackValue(map[string]any{"ok": true, "value": map[string]any{"items": rows}})
}

// messageFeedbackDelete answers POST /api/messageFeedback/delete: remove one
// item, checking the version the client observed. Deleting what is already gone
// succeeds without an event, which is the reference's own postcondition.
func (h *Handler) messageFeedbackDelete(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId", "messageId", "ifVersion"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	messageID, _, failure := stringArg(args, "messageId")
	if failure != nil {
		return nil, failure
	}
	if messageID == "" {
		return nil, argumentRequired("messageId")
	}
	ifVersion, _, failure := stringArg(args, "ifVersion")
	if failure != nil {
		return nil, failure
	}
	if ifVersion == "" {
		return nil, argumentRequired("ifVersion")
	}
	if !h.sessionKnown(ctx, sessionID) {
		return feedbackSessionNotFound(sessionID)
	}
	events, failure := h.sessionEvents(ctx, sessionID)
	if failure != nil {
		return nil, failure
	}
	items, err := foldMessageFeedback(sessionID, events)
	if err != nil {
		return nil, fail(codeInternal, "read message feedback: "+err.Error(), nil)
	}
	existing, found := items[messageID]
	if found && existing.Version != ifVersion {
		return feedbackValue(map[string]any{"ok": false, "error": map[string]any{
			"code": "version-conflict", "current": existing,
		}})
	}
	if found {
		payload := map[string]any{"sessionId": sessionID, "messageId": messageID}
		if _, err := h.appendSessionEvent(ctx, sessionID, "feedback/message-delete", payload); err != nil {
			return nil, fail(codeInternal, "commit message feedback deletion: "+err.Error(), nil)
		}
	}
	return feedbackValue(map[string]any{"ok": true, "value": map[string]any{"absent": true}})
}

// sessionFeedbackRecord answers POST /api/sessionFeedback/record: one remark
// about the conversation itself. Blank text is recorded as absent, and an entry
// with neither text nor category is still a recorded event, exactly as the
// reference's `/feedback` command writes it.
func (h *Handler) sessionFeedbackRecord(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId", "text", "category"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	text, _, failure := stringArg(args, "text")
	if failure != nil {
		return nil, failure
	}
	category, hasCategory, failure := stringArg(args, "category")
	if failure != nil {
		return nil, failure
	}
	if hasCategory && !feedbackCategory(category) {
		return nil, fail(codeArgumentsInvalid, `"category" is not a feedback category`,
			map[string]any{"argument": "category", "value": category})
	}
	if !h.sessionKnown(ctx, sessionID) {
		return feedbackSessionNotFound(sessionID)
	}
	payload := map[string]any{}
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		payload["text"] = trimmed
	}
	if hasCategory {
		payload["category"] = category
	}
	if _, err := h.appendSessionEvent(ctx, sessionID, "feedback/record", payload); err != nil {
		return nil, fail(codeInternal, "commit session feedback: "+err.Error(), nil)
	}
	return feedbackValue(map[string]any{"ok": true, "value": map[string]any{"recorded": true}})
}

// feedbackValue wraps a method that reports its outcome inside the value union.
// The dispatcher serializes whatever a method returns, so the union is returned
// as the value with no envelope-level failure to accompany it: a method error
// here would be a shape the console's schema does not admit.
func feedbackValue(value map[string]any) (any, *methodError) {
	return value, nil
}

// feedbackSessionNotFound is the union's session-not-found arm. It is the code
// the console's own message map knows ("this session is no longer persisted"),
// not this namespace's `session/not-found` envelope error.
func feedbackSessionNotFound(sessionID string) (any, *methodError) {
	return feedbackValue(map[string]any{"ok": false, "error": map[string]any{
		"code": "session-not-found", "sessionId": sessionID,
	}})
}

// feedbackCurrent renders the version-conflict arm's `current`: the item the
// client's edit lost against, or null when nothing is stored.
func feedbackCurrent(item messageFeedbackItem, found bool) any {
	if !found {
		return nil
	}
	return item
}

// feedbackItemPayload is the stored item's own shape. It is built explicitly so a
// field the console does not expect cannot ride along in the log.
func feedbackItemPayload(item messageFeedbackItem) map[string]any {
	payload := map[string]any{
		"messageId": item.MessageID, "rating": item.Rating, "version": item.Version,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
	}
	if item.Note != "" {
		payload["note"] = item.Note
	}
	if item.Category != "" {
		payload["category"] = item.Category
	}
	return payload
}

// foldMessageFeedback replays one session's events into its current items. The
// session id is checked on every event, because a run's log can carry another
// session's feedback only if a caller asked for it -- and then it is not this
// session's answer.
func foldMessageFeedback(sessionID string, events []zenforge.Event) (map[string]messageFeedbackItem, error) {
	items := map[string]messageFeedbackItem{}
	for _, event := range events {
		switch string(event.Type) {
		case "feedback/message-put":
			if payloadSession(event) != sessionID {
				continue
			}
			raw, ok := event.Payload["item"]
			if !ok {
				return nil, fmt.Errorf("feedback/message-put at seq %d has no item", event.Seq)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, err
			}
			var item messageFeedbackItem
			if err := json.Unmarshal(encoded, &item); err != nil {
				return nil, err
			}
			if item.MessageID == "" || item.Version == "" {
				return nil, fmt.Errorf("feedback/message-put at seq %d is missing an identity", event.Seq)
			}
			if item.UpdatedAt < item.CreatedAt {
				return nil, fmt.Errorf("feedback/message-put at seq %d updates before it was created", event.Seq)
			}
			items[item.MessageID] = item
		case "feedback/message-delete":
			if payloadSession(event) != sessionID {
				continue
			}
			messageID, _ := event.Payload["messageId"].(string)
			if messageID == "" {
				return nil, fmt.Errorf("feedback/message-delete at seq %d has no messageId", event.Seq)
			}
			delete(items, messageID)
		}
	}
	return items, nil
}

// feedbackItemOrder names the current items in first-recorded order, so a list
// answer is stable across reads: a map has no order, and the console renders the
// array it is given.
func feedbackItemOrder(sessionID string, events []zenforge.Event) []string {
	order := []string{}
	seen := map[string]bool{}
	for _, event := range events {
		switch string(event.Type) {
		case "feedback/message-put":
			if payloadSession(event) != sessionID {
				continue
			}
			raw, ok := event.Payload["item"].(map[string]any)
			if !ok {
				continue
			}
			messageID, _ := raw["messageId"].(string)
			if messageID != "" && !seen[messageID] {
				seen[messageID] = true
				order = append(order, messageID)
			}
		case "feedback/message-delete":
			if payloadSession(event) != sessionID {
				continue
			}
			messageID, _ := event.Payload["messageId"].(string)
			delete(seen, messageID)
			remaining := order[:0]
			for _, current := range order {
				if current != messageID {
					remaining = append(remaining, current)
				}
			}
			order = remaining
		}
	}
	return order
}

// payloadSession reads the session an event names, when it names one.
func payloadSession(event zenforge.Event) string {
	sessionID, _ := event.Payload["sessionId"].(string)
	return sessionID
}

// feedbackCategory reports whether a category is one the console declares.
func feedbackCategory(category string) bool {
	for _, known := range feedbackCategories {
		if category == known {
			return true
		}
	}
	return false
}

// assistantMessageExists reports whether the session's durable conversation holds
// a finalized assistant message with this console id. The reference checks the
// same thing against the session's own log (`assistant/message` with a derived
// message id), which is what stops a rating from attaching to a message the
// console invented.
func (h *Handler) assistantMessageExists(ctx context.Context, sessionID, messageID string) bool {
	log, err := dshwire.Session(ctx, h, sessionID, func(turn int) dshwire.Identity {
		identity := h.wireIdentity(sessionID)
		identity.Turn = turn
		return identity
	}, nil)
	if err != nil {
		return false
	}
	for _, record := range log.Records {
		if record.Type != "assistant/message" {
			continue
		}
		// A projected assistant record is `{turn, step, message: {...}, stream}`, and
		// the console's message identity is the nested message's id (ADR 0110).
		message, _ := record.Data["message"].(map[string]any)
		if id, _ := message["id"].(string); id == messageID {
			return true
		}
	}
	return false
}

// sessionEvents reads every raw event of a session's turns, which is the log the
// feedback fold replays. It is the unprojected log on purpose: a feedback event
// projects to no console record, so the projected window cannot be the fold's
// input.
func (h *Handler) sessionEvents(ctx context.Context, sessionID string) ([]zenforge.Event, *methodError) {
	runs := h.sessionRunIDs(ctx, sessionID)
	events := []zenforge.Event{}
	for _, runID := range runs {
		turn, err := h.events.Read(ctx, runID, 0, 0)
		if err != nil {
			return nil, fail(codeInternal, "read session events: "+err.Error(), nil)
		}
		events = append(events, turn...)
	}
	return events, nil
}

// appendSessionEvent adds one host-authored event to the session's newest turn
// and returns its seq. The append retries on a moving tail for the reason
// appendTitle does: the seq is computed against the current tail, and a
// concurrent writer must not be overwritten by a stale position.
func (h *Handler) appendSessionEvent(ctx context.Context, sessionID, eventType string, payload map[string]any) (int64, error) {
	runID := h.currentRun(ctx, sessionID)
	var lastErr error
	for attempt := 0; attempt < titleAppendAttempts; attempt++ {
		latest, err := h.events.LatestSeq(ctx, runID)
		if err != nil {
			return 0, err
		}
		event := zenforge.NewEvent(zenforge.EventType(eventType), runID, payload)
		event.Seq = zenforge.NextEventSeq(latest)
		if err := h.events.Append(ctx, event); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return 0, lastErr
			}
			continue
		}
		return event.Seq, nil
	}
	return 0, fmt.Errorf("event log tail did not settle after %d attempts: %w", titleAppendAttempts, lastErr)
}

// newFeedbackVersion mints one item version. It is a UUID rather than a counter
// because the console compares it for equality only: two clients editing the same
// message must conflict, and neither can predict the other's token.
func newFeedbackVersion() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// A version only has to be unique among the writes this process makes; the
		// entropy source failing is not a reason to refuse a human's feedback.
		return fmt.Sprintf("feedback-%d", time.Now().UnixNano())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

package dshapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshstream"
)

// queuePrompt queues one more message into the running turn, which is what the
// console does when the operator types while an answer is streaming.
func (f *fixture) queuePrompt(t *testing.T, rpcID, sessionID, mode, text string) *responseEnvelope {
	t.Helper()
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, rpcID, "session/prompt",
		fmt.Sprintf(`{"requestId":%s,"sessionId":%s,"mode":%s,"content":[{"type":"text","text":%s}]}`,
			mustJSON(t, rpcID), mustJSON(t, sessionID), mustJSON(t, mode), mustJSON(t, text))))
	envelope := decodeResponse(t, recorder)
	return &envelope
}

// updateQueue posts one queue operation and returns its envelope.
func (f *fixture) updateQueue(t *testing.T, rpcID, sessionID, itemID, action string) *responseEnvelope {
	t.Helper()
	recorder := f.post(t, "/api/session/updateQueue", rpcBody(t, rpcID, "session/updateQueue",
		fmt.Sprintf(`{"sessionId":%s,"itemId":%s,"action":%s}`,
			mustJSON(t, sessionID), mustJSON(t, itemID), action)))
	envelope := decodeResponse(t, recorder)
	return &envelope
}

// assertAccepted fails unless the queue operation answered the client's own
// `{accepted: true}`.
func assertAccepted(t *testing.T, envelope *responseEnvelope) {
	t.Helper()
	if envelope.Result.Error != nil {
		t.Fatalf("queue operation failed: %+v", envelope.Result.Error)
	}
	var value struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.Unmarshal(envelope.Result.Value, &value); err != nil {
		t.Fatalf("decode accepted: %v", err)
	}
	if !value.Accepted {
		t.Fatal("accepted = false")
	}
}

// assertRefused fails unless the queue operation failed with exactly this code,
// message and details.
func assertRefused(t *testing.T, envelope *responseEnvelope, code, message string, details map[string]any) {
	t.Helper()
	if envelope.Result.Error == nil {
		t.Fatalf("expected %s, got value %s", code, envelope.Result.Value)
	}
	if envelope.Result.Error.Code != code || envelope.Result.Error.Message != message {
		t.Fatalf("refusal = %s %q, want %s %q", envelope.Result.Error.Code, envelope.Result.Error.Message, code, message)
	}
	for key, want := range details {
		if got := envelope.Result.Error.Details[key]; got != want {
			t.Fatalf("details[%s] = %v, want %v (%v)", key, got, want, envelope.Result.Error.Details)
		}
	}
}

// A message queued into a running turn is pending until the run is handed it, and
// the console renders that pending state from the `inbox` cell -- split into the
// two lists whose names are the prompt modes. Without the cell the row is only
// the console's own echo, which no queue mutation can reach.
func TestSessionPromptProjectsThePendingQueue(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	type observed struct {
		sessionID string
		state     dshstream.QueueState
	}
	updates := make([]observed, 0, 1)
	stop := f.handler.PendingQueueUpdates(func(update dshstream.QueueUpdate) {
		updates = append(updates, observed{sessionID: update.SessionID, state: update.State})
	})
	defer stop()

	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))
	assertAccepted(t, f.queuePrompt(t, "req-steer", sessionID, "steer", "actually, now"))

	state := f.handler.PendingQueue(sessionID)
	if state.Seq == 0 {
		t.Fatal("the queue cell carries a sequence: a frame numbered at or below the cursor is discarded")
	}
	if len(state.NextTurn) != 1 || len(state.NextStep) != 1 {
		t.Fatalf("queue = %+v, want one row in each list", state)
	}
	if state.NextTurn[0].ID != "req-queued" || state.NextTurn[0].Text != "wait for the tests" {
		t.Fatalf("queued row = %+v", state.NextTurn[0])
	}
	if state.NextStep[0].ID != "req-steer" || state.NextStep[0].Text != "actually, now" {
		t.Fatalf("steering row = %+v", state.NextStep[0])
	}
	// The row's identity is the console's own requestId -- the same value the
	// projected user message publishes as source.rpcId -- which is what lets the
	// console retire its echo and address the row by the same id.
	if len(updates) != 2 {
		t.Fatalf("updates = %+v, want one per queued message", updates)
	}
	if updates[1].sessionID != sessionID || len(updates[1].state.NextTurn) != 1 || len(updates[1].state.NextStep) != 1 {
		t.Fatalf("last update = %+v", updates[1])
	}
	if updates[1].state.Seq <= updates[0].state.Seq {
		t.Fatalf("sequences did not advance: %d then %d", updates[0].state.Seq, updates[1].state.Seq)
	}
}

// The projection is a read, not a cache: a message the run has been handed is
// gone from the cell, which is what retires the console's row once the transcript
// carries the message itself.
func TestPendingQueueForgetsDeliveredMessages(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))
	emptied := make([]dshstream.QueueState, 0, 1)
	stop := f.handler.PendingQueueUpdates(func(update dshstream.QueueUpdate) {
		emptied = append(emptied, update.State)
	})
	defer stop()

	f.agent.drainSteers()

	state := f.handler.PendingQueue(sessionID)
	if len(state.NextTurn) != 0 || len(state.NextStep) != 0 {
		t.Fatalf("queue = %+v, want nothing pending after the delivery", state)
	}
	if len(emptied) != 1 || len(emptied[0].NextTurn) != 0 {
		t.Fatalf("updates = %+v, want one empty cell", emptied)
	}
}

// Editing a queued row rewrites the message the run will be handed -- not a copy
// the console keeps -- and the cell republishes it.
func TestSessionUpdateQueueEditsTheQueuedMessage(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait for the tests"))
	changes := 0
	stop := f.handler.PendingQueueUpdates(func(update dshstream.QueueUpdate) { changes++ })
	defer stop()

	assertAccepted(t, f.updateQueue(t, "rpc-edit", sessionID, "req-queued",
		`{"kind":"edit","content":[{"type":"text","text":"wait for the release instead"}]}`))
	// The mutation itself republishes the cell: the console holds rows, not a
	// snapshot it re-reads, so an edit that only changed the run queue would leave
	// the old text on screen until something else happened.
	if changes != 1 {
		t.Fatalf("updates = %d, want one for the edit", changes)
	}
	state := f.handler.PendingQueue(sessionID)
	if len(state.NextTurn) != 1 || state.NextTurn[0].Text != "wait for the release instead" {
		t.Fatalf("queue = %+v, want the edited text", state)
	}
	if got := f.agent.steered(); len(got) != 1 || got[0] != "wait for the release instead" {
		t.Fatalf("agent queue = %v, want the edited message", got)
	}
	// An edit of a row the queue no longer holds is the console's own answer, and
	// nothing about the run changes.
	assertRefused(t, f.updateQueue(t, "rpc-stale", sessionID, "req-gone",
		`{"kind":"edit","content":[{"type":"text","text":"late"}]}`),
		codeQueueItemNotFound, "queued item is no longer pending", map[string]any{"itemId": "req-gone"})
	if got := f.agent.steered(); len(got) != 1 {
		t.Fatalf("agent queue = %v, want it unchanged by the stale edit", got)
	}
}

// Removing a row drops the message before the run is handed it, so the turn the
// operator cancelled never reaches the model.
func TestSessionUpdateQueueRemovesTheQueuedMessage(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "never mind"))
	removed := 0
	stop := f.handler.PendingQueueUpdates(func(update dshstream.QueueUpdate) { removed++ })
	defer stop()
	assertAccepted(t, f.updateQueue(t, "rpc-remove", sessionID, "req-queued", `{"kind":"remove"}`))
	if removed != 1 {
		t.Fatalf("updates = %d, want one for the removal", removed)
	}

	state := f.handler.PendingQueue(sessionID)
	if len(state.NextTurn) != 0 || len(state.NextStep) != 0 {
		t.Fatalf("queue = %+v, want the row gone", state)
	}
	if got := f.agent.steered(); len(got) != 0 {
		t.Fatalf("agent queue = %v, want the removed message gone", got)
	}
	// The row is gone, so a second removal is the not-found answer rather than a
	// silent success -- the console paints a row that may have been delivered.
	assertRefused(t, f.updateQueue(t, "rpc-again", sessionID, "req-queued", `{"kind":"remove"}`),
		codeQueueItemNotFound, "queued item is no longer pending", map[string]any{"itemId": "req-queued"})
}

// Steering a queued turn hands it to the running turn: the row moves to the
// steering half of the cell, where the chat view shows it, and the run still
// receives the same message at the same boundary. Steering something already
// steering is the reference's refusal.
func TestSessionUpdateQueueSteersAQueuedTurn(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "now, please"))
	assertAccepted(t, f.updateQueue(t, "rpc-steer", sessionID, "req-queued", `{"kind":"steer"}`))

	state := f.handler.PendingQueue(sessionID)
	if len(state.NextTurn) != 0 || len(state.NextStep) != 1 || state.NextStep[0].ID != "req-queued" {
		t.Fatalf("queue = %+v, want the row in the steering list only", state)
	}
	if got := f.agent.steered(); len(got) != 1 || got[0] != "now, please" {
		t.Fatalf("agent queue = %v, want the message still queued", got)
	}
	assertRefused(t, f.updateQueue(t, "rpc-steer-again", sessionID, "req-queued", `{"kind":"steer"}`),
		codeSteerUnavailable, "current turn no longer accepts steering", map[string]any{"itemId": "req-queued"})

	// A message queued as steering is already in the state the action asks for,
	// and that request is refused rather than reported as a change.
	assertAccepted(t, f.queuePrompt(t, "req-steer", sessionID, "steer", "into the turn"))
	assertRefused(t, f.updateQueue(t, "rpc-steer-step", sessionID, "req-steer", `{"kind":"steer"}`),
		codeSteerUnavailable, "current turn no longer accepts steering", map[string]any{"itemId": "req-steer"})
}

// The refusals an edit can earn are the reference's own, with its sentences: a
// queue that holds text cannot accept anything else, and an edit with no text is
// not an edit.
func TestSessionUpdateQueueValidatesTheAction(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	assertAccepted(t, f.queuePrompt(t, "req-queued", sessionID, "queue", "wait"))
	image := `{"kind":"edit","content":[{"type":"image","attachment":{"attachmentId":"a-1","mediaType":"image/png","bytes":10,"width":1,"height":1}}]}`

	assertRefused(t, f.updateQueue(t, "rpc-image", sessionID, "req-queued", image),
		codeAttachmentInvalid, "queue edits accept text content only", map[string]any{"reason": "QUEUE_EDIT_NON_TEXT"})
	assertRefused(t, f.updateQueue(t, "rpc-empty", sessionID, "req-queued",
		`{"kind":"edit","content":[{"type":"text","text":"   "}]}`),
		codeBadRequest, "queue edit content must include non-whitespace text", map[string]any{})
	assertRefused(t, f.updateQueue(t, "rpc-no-content", sessionID, "req-queued", `{"kind":"edit"}`),
		codeBadRequest, "queue edit content must include non-whitespace text", map[string]any{})
	assertRefused(t, f.updateQueue(t, "rpc-kind", sessionID, "req-queued", `{"kind":"reorder"}`),
		codeArgumentsInvalid, `"action.kind" must be "edit", "remove" or "steer"`, map[string]any{"kind": "reorder"})
	// The union has no other members, so an extra field is an argument failure
	// rather than something silently ignored.
	assertRefused(t, f.updateQueue(t, "rpc-extra", sessionID, "req-queued", `{"kind":"remove","force":true}`),
		codeArgumentsInvalid, `unexpected argument "force"`, map[string]any{})
	// An unknown session and a session with nothing queued are the same answer:
	// the console shows "the item is not pending" for both.
	assertRefused(t, f.updateQueue(t, "rpc-unknown", "run-nobody", "req-queued", `{"kind":"remove"}`),
		codeQueueItemNotFound, "queued item is no longer pending", map[string]any{"itemId": "req-queued"})
	assertRefused(t, f.updateQueue(t, "rpc-blank-item", sessionID, "", `{"kind":"remove"}`),
		codeArgumentsInvalid, `argument "itemId" is required`, map[string]any{"argument": "itemId"})
}

// The request is the console's own shape and nothing else: the action union lives
// inside `action`, so an extra top-level field is a caller this host does not
// understand rather than one to guess at.
func TestSessionUpdateQueueRejectsUnknownArguments(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	recorder := f.post(t, "/api/session/updateQueue", rpcBody(t, "rpc-extra", "session/updateQueue",
		fmt.Sprintf(`{"sessionId":%s,"itemId":"req-1","action":{"kind":"remove"},"increaseTitle":true}`, mustJSON(t, sessionID))))
	envelope := decodeResponse(t, recorder)
	if envelope.Result.Error == nil || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("envelope = %s, want an argument failure", recorder.Body.String())
	}
}

// The request/result/enum shapes are the vendored console's, and the handler is
// pinned against the bytes it was written from: one flattened request object, an
// action union of exactly three operations, and the console's only answer. A
// regression to a fourth operation, a renamed field or a wider result would fail
// here rather than at the console.
func TestSessionUpdateQueueEnvelopeMatchesTheVendoredConsole(t *testing.T) {
	console := sessionBundle(t)
	descriptor := console.descriptor(t, "updateQueue")
	if wires := console.wireNames(t, descriptor, "updateQueue"); !sameStrings(wires, []string{"request"}) {
		t.Fatalf("session/updateQueue wires = %v, want the request object", wires)
	}
	objects := console.objectParameters(t, "updateQueue")
	if len(objects) != 1 {
		t.Fatalf("session/updateQueue object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"action", "itemId", "sessionId"}) {
		t.Fatalf("session/updateQueue request keys = %v, want the session, the row and the action", keys)
	}
	if keys := sortedKeys(topLevelKeys(t, console.schemaExpression(t, "updateQueue", "result"))); !sameStrings(keys, []string{"accepted"}) {
		t.Fatalf("session/updateQueue result keys = %v, want the acknowledgement", keys)
	}
	kinds := regexp.MustCompile(`"kind":\s*literal\("([^"]+)"\)`).
		FindAllStringSubmatch(console.schemaExpression(t, "updateQueue", "parameter_0"), -1)
	fromBundle := make([]string, 0, len(kinds))
	for _, match := range kinds {
		fromBundle = append(fromBundle, match[1])
	}
	if !sameStrings(fromBundle, []string{"edit", "remove", "steer"}) {
		t.Fatalf("action kinds = %v, want the console's own union", fromBundle)
	}
	// The handler's accepted actions are the same three, read out of its switch
	// rather than restated: a kind the console can send must have a case, and a case
	// it cannot send is dead code.
	source := readSource(t, "sessionqueue.go")
	start := strings.Index(source, "func updateQueueAction")
	if start < 0 {
		t.Fatal("sessionqueue.go declares no updateQueueAction")
	}
	body := source[start:]
	if end := strings.Index(body, "\nfunc "); end >= 0 {
		body = body[:end]
	}
	accepted := []string{}
	for _, clause := range regexp.MustCompile(`case ([^:]+):`).FindAllStringSubmatch(body, -1) {
		for _, name := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(clause[1], -1) {
			accepted = append(accepted, name[1])
		}
	}
	if !sameStrings(accepted, fromBundle) {
		t.Fatalf("sessionUpdateQueue handles %v, want the bundle's %v", accepted, fromBundle)
	}
	if names := handlerArgumentNames(t, source, "sessionUpdateQueue"); !sameStrings(names, []string{"sessionId", "itemId", "action"}) {
		t.Fatalf("sessionUpdateQueue accepts %v, want the flattened request keys", names)
	}
}

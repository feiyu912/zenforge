package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
)

// feedbackFixture starts a session with one answered turn, which is what a rating
// needs: the reference only accepts feedback for a finalized assistant message,
// and the message's console id comes from the session page.
func feedbackFixture(t *testing.T) (*fixture, string, string) {
	t.Helper()
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.answerTurn(t, sessionID, "the answer")
	messageID := ""
	for _, record := range f.pageRecords(t, sessionID) {
		if record.Event.Type != "assistant/message" {
			continue
		}
		message, _ := record.Event.Data["message"].(map[string]any)
		if id, _ := message["id"].(string); id != "" {
			messageID = id
		}
	}
	if messageID == "" {
		t.Fatalf("the answered turn published no assistant message: %+v", f.pageRecords(t, sessionID))
	}
	return f, sessionID, messageID
}

// feedbackOutcome decodes the value union these four methods answer in: the
// method succeeds and the outcome rides inside the value.
type feedbackOutcome struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error struct {
		Code       string          `json:"code"`
		SessionID  string          `json:"sessionId"`
		MessageID  string          `json:"messageId"`
		MaxBytes   int             `json:"maxBytes"`
		ActualByte int             `json:"actualBytes"`
		Current    json.RawMessage `json:"current"`
	} `json:"error"`
}

func feedbackCall(t *testing.T, f *fixture, method, args string) feedbackOutcome {
	t.Helper()
	recorder := f.post(t, "/api/"+method, rpcBody(t, "rpc-feedback", method, args))
	envelope := decodeResponse(t, recorder)
	if !envelope.Result.OK {
		t.Fatalf("%s answered a method error (%s), want the in-value union: %s",
			method, envelope.Result.Error.Code, recorder.Body.String())
	}
	var outcome feedbackOutcome
	decodeValue(t, recorder, &outcome)
	return outcome
}

// A rating round-trips through the session's own log: put stores it, list reads it
// back, a second read after a restart-shaped re-read still finds it, and delete
// leaves the stable absent postcondition.
func TestMessageFeedbackRoundTrip(t *testing.T) {
	f, sessionID, messageID := feedbackFixture(t)
	put := fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"positive","note":"clear and correct","category":"task-result","ifVersion":null}`,
		mustJSON(t, sessionID), mustJSON(t, messageID))

	outcome := feedbackCall(t, f, "messageFeedback/put", put)
	if !outcome.OK {
		t.Fatalf("put = %+v, want the created item", outcome)
	}
	var item messageFeedbackItem
	if err := json.Unmarshal(outcome.Value, &item); err != nil {
		t.Fatal(err)
	}
	if item.MessageID != messageID || item.Rating != "positive" || item.Note != "clear and correct" ||
		item.Category != "task-result" || item.Version == "" || item.UpdatedAt < item.CreatedAt {
		t.Fatalf("item = %+v, want the stored judgment", item)
	}

	// The list reads the same item back, and the log it replays is durable: the
	// answer is derived from the session's events, not from process memory.
	listed := feedbackCall(t, f, "messageFeedback/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))
	if !listed.OK {
		t.Fatalf("list = %+v", listed)
	}
	var value struct {
		Items []messageFeedbackItem `json:"items"`
	}
	if err := json.Unmarshal(listed.Value, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 || value.Items[0].Version != item.Version {
		t.Fatalf("items = %+v, want the one stored item", value.Items)
	}
	if _, err := foldMessageFeedback(sessionID, mustEvents(t, f, sessionID)); err != nil {
		t.Fatalf("the session's own log does not fold: %v", err)
	}

	// Deleting with the observed version leaves the item absent, and deleting it
	// again succeeds without a second event.
	deleted := feedbackCall(t, f, "messageFeedback/delete",
		fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"ifVersion":%s}`, mustJSON(t, sessionID), mustJSON(t, messageID), mustJSON(t, item.Version)))
	if !deleted.OK {
		t.Fatalf("delete = %+v", deleted)
	}
	var absent struct {
		Absent bool `json:"absent"`
	}
	if err := json.Unmarshal(deleted.Value, &absent); err != nil || !absent.Absent {
		t.Fatalf("delete value = %s (err=%v), want {absent:true}", deleted.Value, err)
	}
	again := feedbackCall(t, f, "messageFeedback/delete",
		fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"ifVersion":%s}`, mustJSON(t, sessionID), mustJSON(t, messageID), mustJSON(t, item.Version)))
	if !again.OK {
		t.Fatalf("second delete = %+v, want the stable postcondition", again)
	}
	listed = feedbackCall(t, f, "messageFeedback/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))
	if err := json.Unmarshal(listed.Value, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 0 {
		t.Fatalf("items = %+v, want none after the delete", value.Items)
	}
}

// The version is the concurrency token: a write against a version that is no
// longer current is a conflict carrying the item the client lost against, and a
// write that changes nothing keeps the version and appends nothing.
func TestMessageFeedbackVersionConflictsAndIdempotence(t *testing.T) {
	f, sessionID, messageID := feedbackFixture(t)
	put := func(rating, note, ifVersion string) feedbackOutcome {
		return feedbackCall(t, f, "messageFeedback/put",
			fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":%q,"note":%q,"ifVersion":%s}`,
				mustJSON(t, sessionID), mustJSON(t, messageID), rating, note, ifVersion))
	}

	first := put("positive", "good", "null")
	var item messageFeedbackItem
	if err := json.Unmarshal(first.Value, &item); err != nil {
		t.Fatal(err)
	}

	// A second write that names the observed version replaces the item and mints a
	// new version.
	second := put("negative", "actually wrong", mustJSON(t, item.Version))
	var replaced messageFeedbackItem
	if err := json.Unmarshal(second.Value, &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.Version == item.Version || replaced.Rating != "negative" || replaced.CreatedAt != item.CreatedAt {
		t.Fatalf("replaced = %+v, want a new version with the original createdAt", replaced)
	}

	// The stale version now conflicts, and the conflict carries the current item so
	// the console can reconcile without a second read.
	conflict := put("positive", "stale", mustJSON(t, item.Version))
	if conflict.OK || conflict.Error.Code != "version-conflict" {
		t.Fatalf("stale write = %+v, want a version conflict", conflict)
	}
	var current messageFeedbackItem
	if err := json.Unmarshal(conflict.Error.Current, &current); err != nil || current.Version != replaced.Version {
		t.Fatalf("conflict current = %s (err=%v), want the live item", conflict.Error.Current, err)
	}

	// A write that repeats the stored judgment is a no-op: same version, no event.
	before := len(mustEvents(t, f, sessionID))
	same := put("negative", "actually wrong", mustJSON(t, replaced.Version))
	var unchanged messageFeedbackItem
	if err := json.Unmarshal(same.Value, &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != replaced.Version {
		t.Fatalf("idempotent write version = %q, want %q", unchanged.Version, replaced.Version)
	}
	if after := len(mustEvents(t, f, sessionID)); after != before {
		t.Fatalf("events = %d, want the %d before the no-op write", after, before)
	}

	// Deleting against a stale version conflicts too, and leaves the item in place.
	stale := feedbackCall(t, f, "messageFeedback/delete",
		fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"ifVersion":%s}`, mustJSON(t, sessionID), mustJSON(t, messageID), mustJSON(t, item.Version)))
	if stale.OK || stale.Error.Code != "version-conflict" {
		t.Fatalf("stale delete = %+v, want a version conflict", stale)
	}
	listed := feedbackCall(t, f, "messageFeedback/list", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))
	var value struct {
		Items []messageFeedbackItem `json:"items"`
	}
	if err := json.Unmarshal(listed.Value, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 || value.Items[0].Version != replaced.Version {
		t.Fatalf("items = %+v, want the item the refused delete could not remove", value.Items)
	}
}

// Every business refusal is the schema's own arm, and a malformed request is
// still an argument error: the union is for outcomes, not for nonsense.
func TestMessageFeedbackRefusals(t *testing.T) {
	f, sessionID, messageID := feedbackFixture(t)

	cases := []struct {
		name   string
		method string
		args   string
		code   string
		field  string
	}{
		{"unknown session", "messageFeedback/list", `{"sessionId":"run-nobody"}`, "session-not-found", "sessionId"},
		{"unknown session on put", "messageFeedback/put",
			fmt.Sprintf(`{"sessionId":"run-nobody","messageId":%s,"rating":"positive","ifVersion":null}`, mustJSON(t, messageID)),
			"session-not-found", "sessionId"},
		{"unknown message", "messageFeedback/put",
			fmt.Sprintf(`{"sessionId":%s,"messageId":"msg-9999","rating":"positive","ifVersion":null}`, mustJSON(t, sessionID)),
			"target-not-found", "messageId"},
		{"blank note", "messageFeedback/put",
			fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"positive","note":"   ","ifVersion":null}`, mustJSON(t, sessionID), mustJSON(t, messageID)),
			"note-blank", ""},
		{"oversize note", "messageFeedback/put",
			fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"positive","note":%s,"ifVersion":null}`,
				mustJSON(t, sessionID), mustJSON(t, messageID), mustJSON(t, longNote(feedbackMaxNoteBytes+1))),
			"note-too-large", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := feedbackCall(t, f, testCase.method, testCase.args)
			if outcome.OK || outcome.Error.Code != testCase.code {
				t.Fatalf("outcome = %+v, want code %q", outcome, testCase.code)
			}
			if testCase.field == "sessionId" && outcome.Error.SessionID == "" {
				t.Fatalf("refusal = %+v, want the session named", outcome.Error)
			}
			if testCase.field == "messageId" && outcome.Error.MessageID == "" {
				t.Fatalf("refusal = %+v, want the message named", outcome.Error)
			}
			if testCase.code == "note-too-large" &&
				(outcome.Error.MaxBytes != feedbackMaxNoteBytes || outcome.Error.ActualByte != feedbackMaxNoteBytes+1) {
				t.Fatalf("refusal = %+v, want the byte policy reported", outcome.Error)
			}
		})
	}

	// A malformed request never reaches the union: a bad rating, a missing
	// ifVersion and an unknown argument are all argument errors.
	for _, testCase := range []struct{ name, args, message string }{
		{"bad rating", fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"maybe","ifVersion":null}`, mustJSON(t, sessionID), mustJSON(t, messageID)),
			`"rating" must be "positive" or "negative"`},
		{"missing ifVersion", fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"positive"}`, mustJSON(t, sessionID), mustJSON(t, messageID)),
			`argument "ifVersion" is required`},
		{"unknown argument", fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"positive","ifVersion":null,"extra":1}`, mustJSON(t, sessionID), mustJSON(t, messageID)),
			`unexpected argument "extra"`},
		{"bad category", fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"positive","category":"vibes","ifVersion":null}`, mustJSON(t, sessionID), mustJSON(t, messageID)),
			`"category" is not a feedback category`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := f.post(t, "/api/messageFeedback/put", rpcBody(t, "rpc-bad", "messageFeedback/put", testCase.args))
			envelope := assertMethodFailure(t, recorder, codeArgumentsInvalid)
			if envelope.Result.Error.Message != testCase.message {
				t.Fatalf("message = %q, want %q", envelope.Result.Error.Message, testCase.message)
			}
		})
	}
}

// sessionFeedback/record appends one remark about the conversation, trims it, and
// refuses a session this host does not know with the same union arm.
func TestSessionFeedbackRecord(t *testing.T) {
	f, sessionID, _ := feedbackFixture(t)
	before := len(mustEvents(t, f, sessionID))

	outcome := feedbackCall(t, f, "sessionFeedback/record",
		fmt.Sprintf(`{"sessionId":%s,"text":"  the plan was too eager  ","category":"product-interaction"}`, mustJSON(t, sessionID)))
	if !outcome.OK {
		t.Fatalf("record = %+v", outcome)
	}
	var value struct {
		Recorded bool `json:"recorded"`
	}
	if err := json.Unmarshal(outcome.Value, &value); err != nil || !value.Recorded {
		t.Fatalf("record value = %s (err=%v), want {recorded:true}", outcome.Value, err)
	}
	events := mustEvents(t, f, sessionID)
	if len(events) != before+1 {
		t.Fatalf("events = %d, want one more than %d", len(events), before)
	}
	last := events[len(events)-1]
	if string(last.Type) != "feedback/record" || last.Payload["text"] != "the plan was too eager" ||
		last.Payload["category"] != "product-interaction" {
		t.Fatalf("event = %+v, want the trimmed remark with its category", last)
	}

	// An entry with no text is still recorded, which is the reference's own rule.
	outcome = feedbackCall(t, f, "sessionFeedback/record", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))
	if !outcome.OK {
		t.Fatalf("blank record = %+v", outcome)
	}
	events = mustEvents(t, f, sessionID)
	// "Bare" is the reference's own rule: no text and no category, only the
	// carrier's runId, which the log adds on read.
	if last = events[len(events)-1]; string(last.Type) != "feedback/record" {
		t.Fatalf("event = %+v, want a feedback/record", last)
	} else if _, present := last.Payload["text"]; present {
		t.Fatalf("payload = %#v, want no text for a blank remark", last.Payload)
	} else if _, present := last.Payload["category"]; present {
		t.Fatalf("payload = %#v, want no category when none was sent", last.Payload)
	}

	unknown := feedbackCall(t, f, "sessionFeedback/record", `{"sessionId":"run-nobody","text":"x"}`)
	if unknown.OK || unknown.Error.Code != "session-not-found" || unknown.Error.SessionID != "run-nobody" {
		t.Fatalf("unknown session = %+v, want the session-not-found arm", unknown)
	}
}

// A feedback event is durable log state the console can read: it must survive the
// projection as a record the console knows, and it must not push the conversation's
// sequence or its transcript around.
func TestFeedbackEventsProjectIntoTheConsoleLog(t *testing.T) {
	f, sessionID, messageID := feedbackFixture(t)
	records := f.pageRecords(t, sessionID)
	assistant := 0
	for _, record := range records {
		if record.Event.Type == "assistant/message" {
			assistant++
		}
	}

	outcome := feedbackCall(t, f, "messageFeedback/put",
		fmt.Sprintf(`{"sessionId":%s,"messageId":%s,"rating":"negative","ifVersion":null}`, mustJSON(t, sessionID), mustJSON(t, messageID)))
	if !outcome.OK {
		t.Fatalf("put = %+v", outcome)
	}

	after := f.pageRecords(t, sessionID)
	feedback := 0
	messages := 0
	for _, record := range after {
		switch record.Event.Type {
		case "feedback/message-put":
			feedback++
		case "assistant/message":
			messages++
		}
	}
	if feedback != 1 {
		t.Fatalf("records = %+v, want the feedback event projected as its own record", after)
	}
	if messages != assistant {
		t.Fatalf("assistant messages = %d, want the %d before the rating", messages, assistant)
	}
	// The record keeps the console's vocabulary: no surfaceOp on a type the
	// console reads but does not put on the model-visible surface.
	for _, record := range after {
		if record.Event.Type == "feedback/message-put" && record.Event.SurfaceOp != "" {
			t.Fatalf("feedback record = %+v, want no surfaceOp", record.Event)
		}
	}
}

// mustEvents reads a session's raw events the way the fold does.
func mustEvents(t *testing.T, f *fixture, sessionID string) []zenforge.Event {
	t.Helper()
	events, failure := f.handler.sessionEvents(context.Background(), sessionID)
	if failure != nil {
		t.Fatalf("sessionEvents: %+v", failure)
	}
	return events
}

func longNote(size int) string {
	note := make([]byte, size)
	for index := range note {
		note[index] = 'n'
	}
	return string(note)
}

// This namespace's wire is the vendored console's, re-derived from the bundle
// rather than restated: the request keys, the union's two arms, and -- because
// these four methods report their refusals *inside* the value -- the exact set of
// business codes the console's own message map knows.
func TestFeedbackEnvelopesMatchTheVendoredConsole(t *testing.T) {
	messageFeedback := vendoredBundle{
		source: readSource(t, goalRemotePath),
		pkg:    "@deepseek-ai/dsh-message-feedback",
		ns:     "messageFeedback",
	}
	sessionFeedback := vendoredBundle{
		source: readSource(t, goalRemotePath),
		pkg:    "@deepseek-ai/dsh-command-feedback",
		ns:     "sessionFeedback",
	}
	cases := []struct {
		bundle      vendoredBundle
		method      string
		requestKeys []string
		codes       []string
	}{
		{messageFeedback, "put",
			[]string{"category", "ifVersion", "messageId", "note", "rating", "sessionId"},
			[]string{"session-not-found", "target-not-found", "version-conflict", "note-blank", "note-too-large"}},
		{messageFeedback, "list", []string{"sessionId"}, []string{"session-not-found"}},
		{messageFeedback, "delete",
			[]string{"ifVersion", "messageId", "sessionId"},
			[]string{"session-not-found", "version-conflict"}},
		{sessionFeedback, "record", []string{"category", "sessionId", "text"}, []string{"session-not-found"}},
	}
	for _, testCase := range cases {
		name := testCase.bundle.pkg + "#" + testCase.method
		t.Run(name, func(t *testing.T) {
			descriptor := testCase.bundle.descriptor(t, testCase.method)
			if wires := testCase.bundle.wireNames(t, descriptor, testCase.method); !sameStrings(wires, []string{"request"}) {
				t.Fatalf("wires = %v, want the request object", wires)
			}
			objects := testCase.bundle.objectParameters(t, testCase.method)
			if len(objects) != 1 {
				t.Fatalf("object parameters = %v, want one request object", objects)
			}
			if keys := sortedKeys(objects[0]); !sameStrings(keys, testCase.requestKeys) {
				t.Fatalf("request keys = %v, want %v", keys, testCase.requestKeys)
			}

			// The result is a union of `{ok: true, value: ...}` and
			// `{ok: false, error: ...}`, and the second arm's codes are the whole
			// vocabulary this host may answer with.
			result := testCase.bundle.schemaExpression(t, testCase.method, "result")
			if !strings.Contains(result, "union([") {
				t.Fatalf("result = %s, want the outcome union", result)
			}
			if !strings.Contains(result, `"ok": literal(true)`) || !strings.Contains(result, `"ok": literal(false)`) {
				t.Fatalf("result = %s, want both arms of the union", result)
			}
			codes := []string{}
			for _, match := range feedbackCodePattern.FindAllStringSubmatch(result, -1) {
				codes = append(codes, match[1])
			}
			if !sameStrings(sortedUnique(codes), testCase.codes) {
				t.Fatalf("codes = %v, want %v", sortedUnique(codes), testCase.codes)
			}
			// Every code the bundle declares is one this host actually answers,
			// which is what keeps the arms from drifting apart.
			switch testCase.method {
			case "put":
				assertHostAnswersEveryCode(t, codes, []string{"session-not-found", "target-not-found", "version-conflict", "note-blank", "note-too-large"})
			default:
				assertHostAnswersEveryCode(t, codes, codes)
			}
		})
	}
}

// assertHostAnswersEveryCode fails when a bundle code has no counterpart in the
// host's own vocabulary, so a schema upgrade cannot add an arm silently.
func assertHostAnswersEveryCode(t *testing.T, bundleCodes, hostCodes []string) {
	t.Helper()
	if !sameStrings(sortedUnique(bundleCodes), sortedUnique(hostCodes)) {
		t.Fatalf("bundle codes = %v, host codes = %v", sortedUnique(bundleCodes), sortedUnique(hostCodes))
	}
}

// sortedUnique sorts and de-duplicates extracted codes: a union can name the same
// code in more than one arm.
func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	unique := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return sortedKeys(unique)
}

// feedbackCodePattern finds the literals a schema uses for its error codes.
var feedbackCodePattern = regexp.MustCompile(`"code": literal\("([^"]+)"\)`)

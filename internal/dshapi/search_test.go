package dshapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// promptSession creates a session and starts its first turn with the given text.
// The search tests need their own text, which the shared startSession helper
// hardcodes.
func (f *fixture) promptSession(t *testing.T, rpcID, text string) string {
	t.Helper()
	sessionID := f.createSession(t)
	f.promptMore(t, rpcID, sessionID, text)
	return sessionID
}

// promptMore sends one more queued turn into an existing session.
func (f *fixture) promptMore(t *testing.T, rpcID, sessionID, text string) {
	t.Helper()
	body := fmt.Sprintf(`{"requestId":%s,"sessionId":%s,"mode":"queue","content":[{"type":"text","text":%s}]}`,
		mustJSON(t, rpcID), mustJSON(t, sessionID), mustJSON(t, text))
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt", body))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt %q failed: %s", text, recorder.Body.String())
	}
}

// searchResult is the served envelope's value.
type searchResult struct {
	Items []struct {
		SessionID string `json:"sessionId"`
		Snippet   string `json:"snippet"`
	} `json:"items"`
	HasMore bool `json:"hasMore"`
}

func (f *fixture) search(t *testing.T, query string) searchResult {
	t.Helper()
	recorder := f.post(t, "/api/session/search", rpcBody(t, "rpc-search", "session/search",
		fmt.Sprintf(`{"query":%s}`, mustJSON(t, query))))
	var value searchResult
	decodeValue(t, recorder, &value)
	return value
}

// One conversation is one row, whatever it contains, and the newest message that
// matches is the one the row quotes.
func TestSessionSearchFindsMessageText(t *testing.T) {
	f := newFixture(t, Config{})
	first := f.promptSession(t, "req-1", "alpha release notes")
	time.Sleep(5 * time.Millisecond)
	second := f.promptSession(t, "req-2", "beta release notes")

	value := f.search(t, "release notes")
	if value.HasMore {
		t.Fatal("hasMore = true, want false")
	}
	if len(value.Items) != 2 {
		t.Fatalf("items = %+v, want both conversations", value.Items)
	}
	// The sessions keep the list's order: newest first.
	if value.Items[0].SessionID != second || value.Items[1].SessionID != first {
		t.Fatalf("order = [%s %s], want newest first [%s %s]", value.Items[0].SessionID, value.Items[1].SessionID, second, first)
	}
	for _, item := range value.Items {
		if !strings.Contains(item.Snippet, "release notes") {
			t.Fatalf("snippet = %q, want the matching text", item.Snippet)
		}
	}
}

// The phrase is data: case does not matter, its words may be separated by any
// whitespace, and its punctuation is literal.
func TestSessionSearchMatchesLiterally(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.promptSession(t, "req-1", "Alpha\tRELEASE   notes."+
		"\nand a second line")

	for _, query := range []string{"alpha", "RELEASE", "alpha release", "release\tnotes.", "alpha\nrelease"} {
		value := f.search(t, query)
		if len(value.Items) != 1 || value.Items[0].SessionID != sessionID {
			t.Fatalf("query %q: items = %+v, want the conversation", query, value.Items)
		}
	}
	// A query's punctuation is not a pattern: "notes?" is not "notes.".
	if value := f.search(t, "notes?"); len(value.Items) != 0 {
		t.Fatalf("query %q matched %+v, want nothing: the phrase is literal", "notes?", value.Items)
	}
}

// A message the session did not say is not searchable: tool output is not the
// conversation's message content, which is the same restriction the reference
// applies with its user/message and assistant/message event filter.
func TestSessionSearchIgnoresNonMessageRecords(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.promptSession(t, "req-1", "an ordinary question")
	f.agent.append(sessionID, zenforge.EventToolResult, map[string]any{
		"toolCallId": "call-1",
		"output":     "a distinctive tool output",
	})
	f.agent.append(sessionID, zenforge.EventStepStarted, map[string]any{"step": 1})
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunRunning)

	if value := f.search(t, "distinctive tool output"); len(value.Items) != 0 {
		t.Fatalf("items = %+v, want nothing: a tool result is not message content", value.Items)
	}
	if value := f.search(t, "ordinary question"); len(value.Items) != 1 {
		t.Fatalf("items = %+v, want the prompt that was a message", value.Items)
	}
}

// The newest matching message is the excerpt, and a conversation never appears
// twice however many of its turns match.
func TestSessionSearchKeepsTheNewestMatchPerSession(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)
	f.promptMore(t, "req-2", sessionID, "the needle is in the newest turn")

	value := f.search(t, "needle")
	if len(value.Items) != 1 {
		t.Fatalf("items = %+v, want one row for the conversation", value.Items)
	}
	if value.Items[0].SessionID != sessionID {
		t.Fatalf("sessionId = %q, want %q", value.Items[0].SessionID, sessionID)
	}
	if !strings.Contains(value.Items[0].Snippet, "newest turn") {
		t.Fatalf("snippet = %q, want the newest matching turn", value.Items[0].Snippet)
	}
}

// The reference caps one search at twenty results and says whether it stopped.
func TestSessionSearchCapsResultsAndReportsMore(t *testing.T) {
	f := newFixture(t, Config{})
	for index := 0; index < sessionSearchResultLimit+1; index++ {
		f.promptSession(t, fmt.Sprintf("req-%d", index), fmt.Sprintf("session %d mentions the substring", index))
		time.Sleep(2 * time.Millisecond)
	}
	value := f.search(t, "substring")
	if len(value.Items) != sessionSearchResultLimit {
		t.Fatalf("items = %d, want the %d-result cap", len(value.Items), sessionSearchResultLimit)
	}
	if !value.HasMore {
		t.Fatal("hasMore = false, want true when a conversation was left out")
	}
}

// The query is normalized the way the reference normalizes it, with its own
// refusals, because the sidebar shows the message it returns.
func TestSessionSearchNormalizesTheQuery(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct {
		name string
		args string
		code string
	}{
		{"missing", `{}`, codeArgumentsInvalid},
		{"not a string", `{"query":7}`, codeArgumentsInvalid},
		{"unknown field", `{"query":"x","limit":2}`, codeArgumentsInvalid},
		{"empty", `{"query":""}`, codeBadRequest},
		{"blank", `{"query":" \t\n"}`, codeBadRequest},
		{"too long", `{"query":` + mustJSON(t, strings.Repeat("a", sessionSearchQueryMaxUTF16+1)) + `}`, codeBadRequest},
		{"NUL", `{"query":"a\u0000b"}`, codeBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assertMethodFailure(t, f.post(t, "/api/session/search",
				rpcBody(t, "rpc-search", "session/search", testCase.args)), testCase.code)
		})
	}
	// The bound counts UTF-16 code units, as the reference's does: 500 astral
	// code points are 1000 units and are refused, while 500 BMP characters are
	// accepted.
	assertMethodFailure(t, f.post(t, "/api/session/search",
		rpcBody(t, "rpc-search", "session/search",
			`{"query":`+mustJSON(t, strings.Repeat("\U0001F600", 251))+`}`)), codeBadRequest)
	if value := f.search(t, strings.Repeat("b", sessionSearchQueryMaxUTF16)); len(value.Items) != 0 {
		t.Fatalf("items = %+v, want no matches for a query at the bound", value.Items)
	}
}

// The excerpt obeys the reference's snippet bound, counted the way it counts it.
func TestSessionExcerptIsBounded(t *testing.T) {
	long := strings.Repeat("é", 400) + "needle" + strings.Repeat("ü", 400)
	excerpt := sessionExcerpt(long, strings.Index(long, "needle"))
	if got := utf8.RuneCountInString(excerpt); got > sessionSearchSnippetCodePoints {
		t.Fatalf("excerpt = %d code points, want at most %d", got, sessionSearchSnippetCodePoints)
	}
	if !strings.Contains(excerpt, "needle") {
		t.Fatalf("excerpt = %q, want the match", excerpt)
	}
	if !strings.HasPrefix(excerpt, "…") {
		t.Fatalf("excerpt = %q, want a leading ellipsis when the text was cut", excerpt)
	}
	// A short message is served whole, with no marker.
	if got := sessionExcerpt("needle", 0); got != "needle" {
		t.Fatalf("excerpt = %q, want the message itself", got)
	}
	// The truncation is the reference's: the longest prefix inside the bound,
	// never a byte cut that would split a code point.
	truncated := sessionSearchTruncate(strings.Repeat("é", sessionSearchSnippetCodePoints+5))
	if got := utf8.RuneCountInString(truncated); got != sessionSearchSnippetCodePoints {
		t.Fatalf("truncated = %d code points, want exactly %d", got, sessionSearchSnippetCodePoints)
	}
}

// The envelope is pinned against the vendored bytes: one `request` object holding
// `query`, and a result of `items` and `hasMore`.
func TestSessionSearchEnvelopeMatchesTheVendoredConsole(t *testing.T) {
	console := sessionBundle(t)
	descriptor := console.descriptor(t, "search")
	wires := console.wireNames(t, descriptor, "search")
	if !sameStrings(wires, []string{"request"}) {
		t.Fatalf("session/search wires = %v, want the request object", wires)
	}
	objects := console.objectParameters(t, "search")
	if len(objects) != 1 {
		t.Fatalf("session/search object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"query"}) {
		t.Fatalf("session/search request keys = %v, want query", keys)
	}
	result := console.schemaExpression(t, "search", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"hasMore", "items"}) {
		t.Fatalf("session/search result keys = %v, want items and hasMore", keys)
	}
	// The item's own shape: a session id and the excerpt.
	itemKeys := topLevelKeys(t, nestedObject(t, result, "items"))
	if keys := sortedKeys(itemKeys); !sameStrings(keys, []string{"sessionId", "snippet"}) {
		t.Fatalf("session/search item keys = %v, want sessionId and snippet", keys)
	}
	if accepted := handlerArgumentNames(t, readSource(t, "search.go"), "sessionSearch"); !sameStrings(accepted, []string{"query"}) {
		t.Fatalf("sessionSearch accepts %v, want the flattened query", accepted)
	}
}

// nestedObject returns the object literal declared inside one array field of a
// generated schema expression, so a result's item shape can be read the way the
// top level is.
func nestedObject(t *testing.T, expression, field string) string {
	t.Helper()
	marker := fmt.Sprintf("%q: array(object({", field)
	start := strings.Index(expression, marker)
	if start < 0 {
		t.Fatalf("no array of objects under %q in %s", field, expression)
	}
	return expression[start:]
}

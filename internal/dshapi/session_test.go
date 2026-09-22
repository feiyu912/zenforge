package dshapi

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/internal/dshsession"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

type listItem struct {
	SessionID string `json:"sessionId"`
	UpdatedAt int64  `json:"updatedAt"`
	Running   bool   `json:"running"`
	Blank     bool   `json:"blank"`
	// Projections is the row's projection hints block. The console reads a
	// conversation's name from this block's `title` cell and from nowhere else, so
	// a top-level title field would be a field nothing renders (ADR 0119).
	Projections *struct {
		AsOfSeq int64          `json:"asOfSeq"`
		Values  map[string]any `json:"values"`
	} `json:"projections"`
	// ParentSessionID is the conversation this one was forked from, which the
	// console reads to nest a fork under its source (flattenLineage).
	ParentSessionID string `json:"parentSessionId"`
}

// title is the name the console would render for this row.
func (i listItem) title() string {
	if i.Projections == nil {
		return ""
	}
	value, _ := i.Projections.Values["title"].(string)
	return value
}

func (f *fixture) listItems(t *testing.T) []listItem {
	t.Helper()
	recorder := f.post(t, "/api/session/list", rpcBody(t, "rpc-list", "session/list", ""))
	var value struct {
		Items []listItem `json:"items"`
	}
	decodeValue(t, recorder, &value)
	return value.Items
}

func findItem(items []listItem, sessionID string) *listItem {
	for index := range items {
		if items[index].SessionID == sessionID {
			return &items[index]
		}
	}
	return nil
}

func TestSessionCreateAllocatesPendingSession(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	if !strings.HasPrefix(sessionID, "run_") {
		t.Fatalf("sessionId = %q, want the zenforge.NewRunID() shape", sessionID)
	}
	items := f.listItems(t)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the one pending session", items)
	}
	if items[0].SessionID != sessionID || !items[0].Blank || items[0].Running {
		t.Fatalf("item = %+v, want a blank, not-running session %s", items[0], sessionID)
	}
	if items[0].UpdatedAt <= 0 {
		t.Fatalf("updatedAt = %d, want a millisecond timestamp", items[0].UpdatedAt)
	}
}

func TestSessionCreateAdoptsExplicitSessionID(t *testing.T) {
	f := newFixture(t, Config{})
	for attempt := 0; attempt < 2; attempt++ {
		recorder := f.post(t, "/api/session/create",
			rpcBody(t, "rpc-create", "session/create", `{"sessionId":"run-explicit-1"}`))
		var value struct {
			SessionID string `json:"sessionId"`
		}
		decodeValue(t, recorder, &value)
		if value.SessionID != "run-explicit-1" {
			t.Fatalf("sessionId = %q, want run-explicit-1", value.SessionID)
		}
	}
}

func TestSessionCreateRejectsUnsupportedFields(t *testing.T) {
	f := newFixture(t, Config{})
	// workspaceId is no longer in this list: it now resolves against the
	// workspace registry, and its refusals (no registry, unknown row, a row
	// that is not the directory this host runs in) are covered by
	// workspaces_test.go.
	cases := []string{
		`{"cwd":"/tmp/elsewhere"}`,
		`{"agentPreset":"fast"}`,
	}
	for _, args := range cases {
		recorder := f.post(t, "/api/session/create", rpcBody(t, "rpc-create", "session/create", args))
		envelope := assertMethodFailure(t, recorder, codeUnimplemented)
		if !strings.Contains(envelope.Result.Error.Message, "session/create") {
			t.Fatalf("message does not name the unimplemented field: %s", envelope.Result.Error.Message)
		}
	}
}

func TestSessionCreateRejectsInvalidSessionID(t *testing.T) {
	f := newFixture(t, Config{})
	for _, sessionID := range []string{"a/b", `a\b`, ".", ".."} {
		args := fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))
		recorder := f.post(t, "/api/session/create", rpcBody(t, "rpc-create", "session/create", args))
		assertMethodFailure(t, recorder, codeArgumentsInvalid)
	}
}

func TestSessionCreateAdoptsExistingDurableSession(t *testing.T) {
	f := newFixture(t, Config{})
	f.agent.append("run-durable-1", zenforge.EventRunStarted, map[string]any{"input": "x"})
	recorder := f.post(t, "/api/session/create",
		rpcBody(t, "rpc-create", "session/create", `{"sessionId":"run-durable-1"}`))
	var value struct {
		SessionID string `json:"sessionId"`
	}
	decodeValue(t, recorder, &value)
	if value.SessionID != "run-durable-1" {
		t.Fatalf("sessionId = %q, want run-durable-1", value.SessionID)
	}
	// Adopting a durable session continues it: the prompt starts the next turn
	// of that conversation rather than claiming the session is fresh or ending
	// the conversation at its first run.
	recorder = f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":"run-durable-1","mode":"queue","content":[{"type":"text","text":"hi"}]}`))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt failed: %s", recorder.Body.String())
	}
	task, ok := f.agent.task("run-durable-1~2")
	if !ok {
		t.Fatalf("no continuation run: started %v", f.agent.taskRunIDs())
	}
	want := []model.Message{{Role: "user", Content: "x"}}
	if !reflect.DeepEqual(task.InitialMessages, want) {
		t.Fatalf("initial messages = %+v, want the durable turn %+v", task.InitialMessages, want)
	}
}

func TestSessionListIsNewestFirst(t *testing.T) {
	f := newFixture(t, Config{})
	first := f.createSession(t)
	time.Sleep(5 * time.Millisecond)
	second := f.createSession(t)
	items := f.listItems(t)
	if len(items) != 2 {
		t.Fatalf("items = %+v, want two sessions", items)
	}
	if items[0].SessionID != second || items[1].SessionID != first {
		t.Fatalf("order = [%s %s], want newest first [%s %s]",
			items[0].SessionID, items[1].SessionID, second, first)
	}
}

func TestSessionListReportsRunningRun(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	item := findItem(f.listItems(t), sessionID)
	if item == nil {
		t.Fatalf("running session %s missing from list", sessionID)
	}
	if !item.Running || item.Blank {
		t.Fatalf("item = %+v, want running and not blank", item)
	}
}

func TestSessionListAcceptsOpaqueCursor(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/list",
		rpcBody(t, "rpc-list", "session/list", `{"cursor":"opaque-cursor"}`))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("list with a cursor failed: %s", recorder.Body.String())
	}
}

func TestSessionPromptStartsPendingRun(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-1","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"  hello world  "}]}`,
			mustJSON(t, sessionID))))
	var value struct {
		Accepted bool `json:"accepted"`
	}
	decodeValue(t, recorder, &value)
	if !value.Accepted {
		t.Fatalf("accepted = false: %s", recorder.Body.String())
	}
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunRunning)
	events, err := f.store.Read(context.Background(), sessionID, 0, 0)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if len(events) == 0 || events[0].Type != zenforge.EventRunStarted {
		t.Fatalf("run log = %+v, want a run.started event", events)
	}
	// The pending allocation was consumed; the new log is the pending flag's
	// replacement, so a second prompt queues instead of starting again.
	item := findItem(f.listItems(t), sessionID)
	if item == nil || !item.Running || item.Blank {
		t.Fatalf("item after prompt = %+v, want a running, non-blank run", item)
	}
}

func TestSessionPromptUnknownSessionIsMethodError(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":"run-missing","mode":"queue","content":[{"type":"text","text":"hi"}]}`))
	assertMethodFailure(t, recorder, codeSessionNotFound)
}

// TestSessionPromptCrossProcessRunIsUnimplemented covers the registry case
// where a run is listed as active but is owned by another manager: the handler
// must not pretend it can deliver a turn to it.
func TestSessionPromptCrossProcessRunIsUnimplemented(t *testing.T) {
	registry := harnesshttp.NewMemoryRunRegistry()
	store := memory.New()
	bus := eventlog.NewBus()

	other := harnesshttp.NewRunManager(newStubAgent(store), store, bus, harnesshttp.RunManagerOptions{
		Registry: registry, OwnerID: "owner-other", TerminalRetention: -1,
	})
	t.Cleanup(func() { _ = other.Close(context.Background()) })
	if _, err := other.Start(context.Background(), zenforge.Task{RunID: "run-remote-1", Input: "hi"}); err != nil {
		t.Fatalf("other manager start: %v", err)
	}

	ourAgent := newStubAgent(store)
	our := harnesshttp.NewRunManager(ourAgent, store, bus, harnesshttp.RunManagerOptions{
		Registry: registry, OwnerID: "owner-ours", TerminalRetention: -1,
	})
	t.Cleanup(func() { _ = our.Close(context.Background()) })
	handler, err := New(our, store, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &fixture{handler: handler, manager: our, agent: ourAgent, store: store}
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":"run-remote-1","mode":"queue","content":[{"type":"text","text":"hi"}]}`))
	envelope := assertMethodFailure(t, recorder, codeUnimplemented)
	if !strings.Contains(envelope.Result.Error.Message, "not owned") {
		t.Fatalf("message does not explain the ownership gap: %s", envelope.Result.Error.Message)
	}
}

func TestSessionPromptRejectsBadArguments(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := mustJSON(t, f.createSession(t))
	cases := []struct {
		name string
		args string
	}{
		{"missing requestId",
			fmt.Sprintf(`{"sessionId":%s,"mode":"queue","content":[{"type":"text","text":"hi"}]}`, sessionID)},
		{"missing mode",
			fmt.Sprintf(`{"requestId":"req","sessionId":%s,"content":[{"type":"text","text":"hi"}]}`, sessionID)},
		{"bad mode",
			fmt.Sprintf(`{"requestId":"req","sessionId":%s,"mode":"now","content":[{"type":"text","text":"hi"}]}`, sessionID)},
		{"missing content",
			fmt.Sprintf(`{"requestId":"req","sessionId":%s,"mode":"queue"}`, sessionID)},
		{"empty content",
			fmt.Sprintf(`{"requestId":"req","sessionId":%s,"mode":"queue","content":[]}`, sessionID)},
		{"whitespace text",
			fmt.Sprintf(`{"requestId":"req","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"   "}]}`, sessionID)},
		{"non-string requestId",
			fmt.Sprintf(`{"requestId":7,"sessionId":%s,"mode":"queue","content":[{"type":"text","text":"hi"}]}`, sessionID)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt", testCase.args))
			assertMethodFailure(t, recorder, codeArgumentsInvalid)
		})
	}
}

func TestSessionPromptRefusesUnsupportedContent(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := mustJSON(t, f.createSession(t))
	cases := []struct {
		part string
		want string
	}{
		{`{"type":"image","mediaType":"image/png","data":"AAAA"}`, "image"},
		{`{"type":"file","receiptId":"receipt-1"}`, "file"},
		{`{"type":"audio","data":"AAAA"}`, "audio"},
	}
	for _, testCase := range cases {
		args := fmt.Sprintf(`{"requestId":"req","sessionId":%s,"mode":"queue","content":[%s]}`, sessionID, testCase.part)
		recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt", args))
		envelope := assertMethodFailure(t, recorder, codeUnsupportedContent)
		if !strings.Contains(envelope.Result.Error.Message, testCase.want) {
			t.Fatalf("message %q does not name the part %q", envelope.Result.Error.Message, testCase.want)
		}
	}
}

func TestSessionPromptQueuesIntoActiveRun(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-steer", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-2","sessionId":%s,"mode":"steer","content":[{"type":"text","text":"second turn"}]}`,
			mustJSON(t, sessionID))))
	var value struct {
		Accepted bool `json:"accepted"`
	}
	decodeValue(t, recorder, &value)
	if !value.Accepted {
		t.Fatalf("accepted = false: %s", recorder.Body.String())
	}
	steered := f.agent.steered()
	if len(steered) != 1 || steered[0] != "second turn" {
		t.Fatalf("steered = %v, want [second turn]", steered)
	}
}

// A conversation outlives its first run: a prompt to a finished session starts
// the next turn under the chain's run id, carrying the exchange so far so the
// model answers in context instead of meeting a stranger (ADR 0086).
func TestSessionPromptOnFinishedRunStartsTheNextTurn(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.append(sessionID, zenforge.EventRunDone, map[string]any{"output": "hi there"})
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-3","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"again"}]}`,
			mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt failed: %s", recorder.Body.String())
	}
	continuationID := sessionID + "~2"
	task, ok := f.agent.task(continuationID)
	if !ok {
		t.Fatalf("no continuation run %q: started %v", continuationID, f.agent.taskRunIDs())
	}
	if task.Input != "again" {
		t.Fatalf("input = %q, want the new turn's text", task.Input)
	}
	want := []model.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	if !reflect.DeepEqual(task.InitialMessages, want) {
		t.Fatalf("initial messages = %+v, want the exchange so far %+v", task.InitialMessages, want)
	}
}

// A prompt may name a continuation run id -- an older listing, or a client that
// followed one -- and it still names the same conversation.
func TestSessionPromptOnAContinuationRunIDResolvesToItsSession(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	continuationID := sessionID + "~2"
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-3","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"third"}]}`,
			mustJSON(t, continuationID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt failed: %s", recorder.Body.String())
	}
	// The named id was itself a continuation, so the next turn continues the
	// session rather than inventing a second chain under it.
	if _, ok := f.agent.task(sessionID + "~2"); !ok {
		t.Fatalf("session %q was not continued: started %v", sessionID, f.agent.taskRunIDs())
	}
	if _, ok := f.agent.task(continuationID + "~2"); ok {
		t.Fatalf("a second chain was started under %q: %v", continuationID, f.agent.taskRunIDs())
	}
}

// A prompt to an unknown session is still not-found: continuing a conversation
// is not the same as inventing one.
func TestSessionPromptOnAnUnknownSessionIsNotFound(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-3","sessionId":"run_never_seen","mode":"queue","content":[{"type":"text","text":"hi"}]}`))
	assertMethodFailure(t, recorder, codeSessionNotFound)
}

func TestSessionCancelActiveRun(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	recorder := f.post(t, "/api/session/cancel", rpcBody(t, "rpc-cancel", "session/cancel",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))))
	var value struct {
		Accepted bool `json:"accepted"`
	}
	decodeValue(t, recorder, &value)
	if !value.Accepted {
		t.Fatalf("accepted = false: %s", recorder.Body.String())
	}
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCancelled)
}

func TestSessionCancelUnknownSession(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/cancel", rpcBody(t, "rpc-cancel", "session/cancel",
		`{"sessionId":"run-missing"}`))
	assertMethodFailure(t, recorder, codeSessionNotFound)
}

func TestSessionCancelFinishedRunIsHonest(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)
	recorder := f.post(t, "/api/session/cancel", rpcBody(t, "rpc-cancel", "session/cancel",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))))
	envelope := assertMethodFailure(t, recorder, codeSessionConflict)
	if !strings.Contains(envelope.Result.Error.Message, string(harnesshttp.RunCompleted)) {
		t.Fatalf("message does not report the finished status: %s", envelope.Result.Error.Message)
	}
}

func TestSessionCancelAlreadyCancelledIsAccepted(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	body := rpcBody(t, "rpc-cancel", "session/cancel", fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID)))
	first := f.post(t, "/api/session/cancel", body)
	var value struct {
		Accepted bool `json:"accepted"`
	}
	decodeValue(t, first, &value)
	if !value.Accepted {
		t.Fatalf("first cancel rejected: %s", first.Body.String())
	}
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCancelled)
	second := f.post(t, "/api/session/cancel", body)
	decodeValue(t, second, &value)
	if !value.Accepted {
		t.Fatalf("second cancel rejected: %s", second.Body.String())
	}
}

func TestSessionRenameCommitsDurableTitle(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	recorder := f.post(t, "/api/session/rename", rpcBody(t, "rpc-rename", "session/rename",
		fmt.Sprintf(`{"sessionId":%s,"title":"  My   Session  "}`, mustJSON(t, sessionID))))
	var value struct {
		Title string `json:"title"`
		Seq   int64  `json:"seq"`
	}
	decodeValue(t, recorder, &value)
	if value.Title != "My Session" {
		t.Fatalf("title = %q, want the normalized %q", value.Title, "My Session")
	}
	if value.Seq < 1 {
		t.Fatalf("seq = %d, want a durable event position", value.Seq)
	}
	events, err := f.store.Read(context.Background(), sessionID, value.Seq-1, 1)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if len(events) != 1 || events[0].Type != zenforge.EventSessionTitle {
		t.Fatalf("event at seq %d = %+v, want session.title", value.Seq, events)
	}
	if events[0].Payload["title"] != "My Session" {
		t.Fatalf("event title = %v, want My Session", events[0].Payload["title"])
	}
	item := findItem(f.listItems(t), sessionID)
	if item == nil || item.title() != "My Session" {
		t.Fatalf("list item = %+v, want the title projection My Session", item)
	}
	if item.Projections.AsOfSeq < 1 {
		t.Fatalf("title projection watermark = %d, want the served sequence that set it",
			item.Projections.AsOfSeq)
	}
}

func TestSessionRenamePendingSessionIsUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	recorder := f.post(t, "/api/session/rename", rpcBody(t, "rpc-rename", "session/rename",
		fmt.Sprintf(`{"sessionId":%s,"title":"Named"}`, mustJSON(t, sessionID))))
	envelope := assertMethodFailure(t, recorder, codeUnimplemented)
	if !strings.Contains(envelope.Result.Error.Message, "pending session") {
		t.Fatalf("message does not name what is missing: %s", envelope.Result.Error.Message)
	}
}

func TestSessionRenameRejectsEmptyTitle(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	recorder := f.post(t, "/api/session/rename", rpcBody(t, "rpc-rename", "session/rename",
		fmt.Sprintf(`{"sessionId":%s,"title":"   "}`, mustJSON(t, sessionID))))
	assertMethodFailure(t, recorder, codeTitleInvalid)
}

func TestSessionRenameUnknownSession(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/rename", rpcBody(t, "rpc-rename", "session/rename",
		`{"sessionId":"run-missing","title":"Named"}`))
	assertMethodFailure(t, recorder, codeSessionNotFound)
}

type pageRecord struct {
	Type  string `json:"type"`
	Event struct {
		Type      string         `json:"type"`
		Seq       int64          `json:"seq"`
		Time      int64          `json:"time"`
		Data      map[string]any `json:"data"`
		SurfaceOp string         `json:"surfaceOp"`
		Ignorable bool           `json:"ignorable"`
	} `json:"event"`
}

type pageValue struct {
	Records []pageRecord `json:"records"`
	HasMore bool         `json:"hasMore"`
}

func pageArgs(t *testing.T, sessionID, extra string) string {
	t.Helper()
	args := fmt.Sprintf(`{"address":{"kind":"session","sessionId":%s}}`, mustJSON(t, sessionID))
	if extra != "" {
		args = strings.TrimSuffix(args, "}") + "," + extra + "}"
	}
	return args
}

func TestSessionPageReturnsRecordsAndHasMore(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.append(sessionID, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.append(sessionID, zenforge.EventStepDone, map[string]any{"step": 1})

	recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"maxMessages":2`)))
	var value pageValue
	decodeValue(t, recorder, &value)
	if len(value.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(value.Records))
	}
	if value.Records[0].Event.Seq != 3 || value.Records[1].Event.Seq != 4 {
		t.Fatalf("seqs = [%d %d], want [3 4]", value.Records[0].Event.Seq, value.Records[1].Event.Seq)
	}
	if !value.HasMore {
		t.Fatal("hasMore = false, want true with an earlier event available")
	}
	if value.Records[0].Type != "event" || value.Records[0].Event.Type != "step/start" {
		t.Fatalf("record = %+v, want the step opened as the console's step/start", value.Records[0])
	}
	// Only the four message-producing types may carry surfaceOp; a step boundary
	// that carried it is what the page reported as "Failed to load history".
	if value.Records[0].Event.SurfaceOp != "" {
		t.Fatalf("step/start carries surfaceOp %q", value.Records[0].Event.SurfaceOp)
	}
	if !value.Records[0].Event.Ignorable && value.Records[0].Event.Type != "step/start" {
		t.Fatalf("record = %+v, want a console event", value.Records[0])
	}
	if value.Records[0].Event.Time <= 0 {
		t.Fatalf("time = %d, want a millisecond timestamp", value.Records[0].Event.Time)
	}

	recorder = f.post(t, "/api/session/page", rpcBody(t, "rpc-page-2", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"beforeSeq":3,"maxMessages":10`)))
	decodeValue(t, recorder, &value)
	if len(value.Records) != 2 {
		t.Fatalf("earlier page records = %d, want 2", len(value.Records))
	}
	if value.Records[0].Event.Seq != 1 || value.Records[1].Event.Seq != 2 {
		t.Fatalf("earlier seqs = [%d %d], want [1 2]", value.Records[0].Event.Seq, value.Records[1].Event.Seq)
	}
	if value.Records[0].Event.Type != "turn/start" {
		t.Fatalf("record = %+v, want the turn's opening marker", value.Records[0])
	}
	// The turn's marker carries the turn and nothing else, and is not a surface
	// append: the console reads it as the anchor of the turn's process row.
	if turn, ok := value.Records[0].Event.Data["turn"].(float64); !ok || turn != 1 {
		t.Fatalf("turn/start data = %v, want the turn number", value.Records[0].Event.Data)
	}
	// The page layer adds the addressed turn and step to every record it serves, so
	// the step here is the page's, not the marker's: what matters is that the
	// projector's own map carried the turn (asserted above) and no surface op.

	if value.Records[0].Event.SurfaceOp != "" {
		t.Fatalf("turn/start carries surfaceOp %q", value.Records[0].Event.SurfaceOp)
	}
	// The prompt follows it, and that one is a surface append.
	if value.Records[1].Event.Type != "user/message" || value.Records[1].Event.SurfaceOp != "append" {
		t.Fatalf("record = %+v, want the prompt as an append user/message", value.Records[1])
	}
	if value.HasMore {
		t.Fatal("hasMore = true, want false at the start of the log")
	}
}

func TestSessionPageReadsDurableStore(t *testing.T) {
	f := newDurableFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.append(sessionID, zenforge.EventStepStarted, map[string]any{"step": 1})
	recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"maxMessages":10`)))
	var value pageValue
	decodeValue(t, recorder, &value)
	if len(value.Records) != 3 {
		t.Fatalf("records = %d, want 3 from the jsonl store", len(value.Records))
	}
}

// TestSessionPageServesACreatedSessionWithNoTurns is the draft case the console
// hits on every new chat: it creates a session, then loads that session's
// history before the first prompt has started a run. An empty history is not a
// missing session, so the page is empty instead of not-found -- which is what the
// page reported as "Failed to load history" on a brand-new conversation. An id
// this host never created stays not-found, and that is the next test.
func TestSessionPageServesACreatedSessionWithNoTurns(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1`)))
	var value pageValue
	decodeValue(t, recorder, &value)
	if len(value.Records) != 0 {
		t.Fatalf("records = %d, want an empty page for a session with no turns", len(value.Records))
	}
	if value.HasMore {
		t.Fatal("hasMore = true for an empty page")
	}
}

func TestSessionPageUnknownSession(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		pageArgs(t, "run-missing", `"throughSeq":-1`)))
	assertMethodFailure(t, recorder, codeSessionNotFound)
}

func TestSessionPageSubagentAddressIsUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		`{"address":{"kind":"subagent","parentSessionId":"p","childSessionId":"c","mode":"one-shot"},"throughSeq":-1}`))
	assertMethodFailure(t, recorder, codeUnimplemented)
}

func TestSessionPageRejectsBadArguments(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct {
		name string
		args string
	}{
		{"missing address", `{"throughSeq":-1}`},
		{"missing sessionId", `{"address":{},"throughSeq":-1}`},
		{"missing throughSeq", `{"address":{"kind":"session","sessionId":"run-x"}}`},
		{"throughSeq too small", `{"address":{"kind":"session","sessionId":"run-x"},"throughSeq":-2}`},
		{"zero maxMessages", `{"address":{"kind":"session","sessionId":"run-x"},"throughSeq":-1,"maxMessages":0}`},
		{"negative beforeSeq", `{"address":{"kind":"session","sessionId":"run-x"},"throughSeq":-1,"beforeSeq":-1}`},
		{"non-numeric throughSeq", `{"address":{"kind":"session","sessionId":"run-x"},"throughSeq":"later"}`},
		{"unknown address kind", `{"address":{"kind":"team","sessionId":"run-x"},"throughSeq":-1}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page", testCase.args))
			assertMethodFailure(t, recorder, codeArgumentsInvalid)
		})
	}
}

// One conversation is one list entry. Listing every turn as its own session
// would make a five-turn conversation look like five sessions sharing a title.
func TestSessionListGroupsATurnsRunsUnderTheirSession(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	second := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-2","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"again"}]}`,
			mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, second); !envelope.Result.OK {
		t.Fatalf("second prompt failed: %s", second.Body.String())
	}

	items := f.listItems(t)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one entry for the conversation", items)
	}
	if items[0].SessionID != sessionID {
		t.Fatalf("sessionId = %q, want the session %q, not one of its turns", items[0].SessionID, sessionID)
	}
}

// An adopted session id that only looks like a continuation is not merged into
// the run it names: the grouping rule needs the base to exist, and this one
// does not.
func TestSessionListKeepsAForeignRunIDLookingLikeAContinuation(t *testing.T) {
	f := newFixture(t, Config{})
	// An operator (or another tool) adopted this id as a session id. Its own
	// base run does not exist, so the grouping rule must leave it alone.
	recorder := f.post(t, "/api/session/create",
		rpcBody(t, "rpc-create", "session/create", `{"sessionId":"run_adopted~2"}`))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("create failed: %s", recorder.Body.String())
	}
	recorder = f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":"run_adopted~2","mode":"queue","content":[{"type":"text","text":"hi"}]}`))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt failed: %s", recorder.Body.String())
	}
	items := f.listItems(t)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the adopted run listed once", items)
	}
	if items[0].SessionID != "run_adopted~2" {
		t.Fatalf("sessionId = %q, want the adopted id to keep its own identity", items[0].SessionID)
	}
}

// History and the live stream must describe the same run: after a second turn,
// the page for the session is the newest turn's log, not the first one's.
func TestSessionPageServesTheNewestTurn(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	prompt := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-2","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"the second question"}]}`,
			mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, prompt); !envelope.Result.OK {
		t.Fatalf("second prompt failed: %s", prompt.Body.String())
	}

	page := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		fmt.Sprintf(`{"address":{"kind":"session","sessionId":%s},"throughSeq":-1}`, mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, page); !envelope.Result.OK {
		t.Fatalf("page failed: %s", page.Body.String())
	}
	if !strings.Contains(page.Body.String(), "the second question") {
		t.Fatalf("page does not serve the newest turn: %s", page.Body.String())
	}
}

func TestSessionPageServesEveryTurnInOneSequence(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.append(sessionID, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.append(sessionID, zenforge.EventStepDone, map[string]any{"step": 1})
	f.agent.finish(sessionID)
	// Wait for the first turn to be terminal before prompting again: an active run
	// would take the second prompt as a steer of the first turn instead of
	// starting the conversation's next turn, which is the resolution under test.
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	// The operator asks a second question: the console prompts the same session,
	// and the host starts the next turn of the conversation under the
	// deterministic continuation run id.
	recorder := f.post(t, "/api/session/prompt",
		rpcBody(t, "rpc-prompt-2", "session/prompt",
			fmt.Sprintf(`{"requestId":"req-2","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"again"}]}`, mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("second prompt failed: %s", recorder.Body.String())
	}
	second := dshsession.ContinuationRunID(sessionID, 2)
	waitForStatus(t, f.manager, second, harnesshttp.RunRunning)
	f.agent.append(second, zenforge.EventStepStarted, map[string]any{"step": 1})
	f.agent.finish(second)

	recorder = f.post(t, "/api/session/page", rpcBody(t, "rpc-page-turns", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"maxMessages":50`)))
	var value pageValue
	decodeValue(t, recorder, &value)
	if len(value.Records) != 9 {
		// Five records for the prompted turn (turn/start, step/start, the prompt,
		// the assistant settlement and the step's end) and four for the second, whose
		// question is grouped with the step that starts it.
		t.Fatalf("records = %d, want both turns' nine events", len(value.Records))
	}
	// One conversation, one sequence: a second turn that numbered from one is what
	// the console rejects as a stream resumed behind its last applied entry.
	for index, record := range value.Records {
		want := int64(index + 1)
		if record.Event.Seq != want {
			t.Fatalf("record %d has seq %d, want %d", index, record.Event.Seq, want)
		}
	}
	if value.HasMore {
		t.Fatal("hasMore = true, want false for a log this short")
	}
	// The second turn's prompt is a user message of its own, and its records are
	// grouped as turn 2 rather than folded into the first turn.
	var prompts []int64
	var promptIdentities []string
	for _, record := range value.Records {
		if record.Event.Type == "user/message" {
			prompts = append(prompts, record.Event.Seq)
			source, _ := record.Event.Data["source"].(map[string]any)
			identity, _ := source["rpcId"].(string)
			promptIdentities = append(promptIdentities, identity)
		}
		if turn, ok := record.Event.Data["turn"].(float64); ok && turn != 2 && record.Event.Seq > 11 {
			t.Fatalf("seq %d carries turn %v, want the second turn", record.Event.Seq, turn)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("user messages at %v, want one per turn", prompts)
	}
	// Each prompt names the requestId it arrived with: that is the identity the
	// console matches its local submission echo against, and a turn without one
	// leaves the echo on screen as a second copy of the question (ADR 0111).
	if len(promptIdentities) != 2 || promptIdentities[0] != "req-1" || promptIdentities[1] != "req-2" {
		t.Fatalf("prompt identities = %v, want the requestId of each turn's prompt", promptIdentities)
	}
	// Each message names itself by the conversation's sequence, not the run's:
	// the console matches a `user/message` node on `String(data.id)`, so two
	// turns whose prompt both said `msg-1` rendered as one message and the
	// operator's second question showed as the first one (ADR 0110).
	ids := map[string]int64{}
	for _, record := range value.Records {
		id := ""
		if raw, ok := record.Event.Data["id"].(string); ok {
			id = raw
		} else if message, ok := record.Event.Data["message"].(map[string]any); ok {
			if raw, ok := message["id"].(string); ok {
				id = raw
			}
		}
		if id == "" {
			continue
		}
		if first, seen := ids[id]; seen {
			t.Fatalf("seq %d and seq %d both carry message id %q", first, record.Event.Seq, id)
		}
		ids[id] = record.Event.Seq
	}
	if len(ids) == 0 {
		t.Fatal("the page carries no message identities")
	}

	// "Load earlier" pages back below the second turn's records and reaches the
	// first turn, ending exactly where the console's window begins.
	recorder = f.post(t, "/api/session/page", rpcBody(t, "rpc-page-earlier", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"maxMessages":2`)))
	decodeValue(t, recorder, &value)
	if len(value.Records) != 2 || value.Records[0].Event.Seq != 8 || value.Records[1].Event.Seq != 9 {
		t.Fatalf("newest page seqs = [%d %d], want [8 9]", value.Records[0].Event.Seq, value.Records[1].Event.Seq)
	}
	if !value.HasMore {
		t.Fatal("hasMore = false, want the earlier turn available")
	}
	recorder = f.post(t, "/api/session/page", rpcBody(t, "rpc-page-first", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"beforeSeq":6,"maxMessages":2`)))
	decodeValue(t, recorder, &value)
	// The page ends exactly one below the window it extends -- the console's
	// prepend requires the earlier page's newest record to be firstCursor - 1 --
	// and reaches into the first turn.
	if len(value.Records) != 2 || value.Records[0].Event.Seq != 4 || value.Records[1].Event.Seq != 5 {
		t.Fatalf("earlier page seqs = [%d %d], want [4 5]", value.Records[0].Event.Seq, value.Records[1].Event.Seq)
	}
}

// TestSessionListReportsAnExpiredLeaseAsNotRunning covers the record a host
// leaves behind when the process executing a run dies: the status stays active
// and the lease expires. The console must not present that conversation as
// running, and the next prompt must continue the conversation instead of
// steering a run nobody owns (ADR 0109).
func TestSessionListReportsAnExpiredLeaseAsNotRunning(t *testing.T) {
	registry := harnesshttp.NewMemoryRunRegistry()
	store := memory.New()
	bus := eventlog.NewBus()

	// A previous process claimed the run and then died: the claim is active and
	// its lease has already expired.
	now := time.Now().UTC()
	leaseUntil := now.Add(-time.Minute)
	if _, err := registry.Claim(context.Background(), harnesshttp.RunClaim{
		RunID: "run_interrupted", OwnerID: "owner-gone", Status: harnesshttp.RunRunning,
		LeaseUntil: leaseUntil, StartedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The transcript is durable, so the session is real even though its owner is
	// not: the title comes from the log, exactly as it would after a restart.
	agent := newStubAgent(store)
	agent.append("run_interrupted", zenforge.EventSessionTitle, map[string]any{"title": "an interrupted question"})

	manager := harnesshttp.NewRunManager(agent, store, bus, harnesshttp.RunManagerOptions{
		Registry: registry, OwnerID: "owner-new", TerminalRetention: -1,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	handler, err := New(manager, store, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &fixture{handler: handler, manager: manager, agent: agent, store: store}

	item := findItem(f.listItems(t), "run_interrupted")
	if item == nil {
		t.Fatal("the interrupted session is not listed")
	}
	if item.Running {
		t.Fatal("an expired lease is listed as running")
	}
	if item.title() != "an interrupted question" {
		t.Fatalf("title = %q, want the durable transcript's title", item.title())
	}

	// The prompt continues the conversation: a run nobody owns cannot be steered.
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":"run_interrupted","mode":"queue","content":[{"type":"text","text":"carry on"}]}`))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt after an interruption failed: %s", recorder.Body.String())
	}
	second := dshsession.ContinuationRunID("run_interrupted", 2)
	waitForStatus(t, manager, second, harnesshttp.RunRunning)
}

// TestSessionListSurvivesARestart covers what a durable registry is for: the
// console lists the conversations this install served after the host restarts,
// with their titles and their finished state, instead of an empty sidebar whose
// transcripts are all still on disk.
func TestSessionListSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store := jsonl.New(dir)
	registryPath := filepath.Join(dir, "runs.sqlite")

	registry, err := harnesshttp.OpenSQLiteRunRegistry(context.Background(), registryPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	first := newFixtureOver(t, store, registry)
	sessionID := first.createSession(t)
	recorder := first.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		fmt.Sprintf(`{"requestId":"req-1","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"first question"}]}`, mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("prompt failed: %s", recorder.Body.String())
	}
	first.agent.append(sessionID, zenforge.EventSessionTitle, map[string]any{"title": "first question"})
	first.agent.finish(sessionID)
	waitForStatus(t, first.manager, sessionID, harnesshttp.RunCompleted)
	if err := first.manager.Close(context.Background()); err != nil {
		t.Fatalf("close the first manager: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("close the first registry: %v", err)
	}

	// A new host over the same durable state: an empty in-process manager and a
	// registry that still knows the run.
	restarted, err := harnesshttp.OpenSQLiteRunRegistry(context.Background(), registryPath)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	second := newFixtureOver(t, store, restarted)

	item := findItem(second.listItems(t), sessionID)
	if item == nil {
		t.Fatal("the restarted host does not list the conversation it served")
	}
	if item.Running {
		t.Fatal("a finished conversation is listed as running after a restart")
	}
	if item.title() != "first question" {
		t.Fatalf("title = %q, want the operator's task", item.title())
	}
}

// TestSessionListOmitsARunThatNeverWroteAnEvent covers the other half of a
// durable registry: a run whose start failed keeps a record with no transcript,
// and the console answers not-found for it. A registry that outlives the process
// must not turn that record into a permanent conversation nobody can open.
func TestSessionListOmitsARunThatNeverWroteAnEvent(t *testing.T) {
	registry := harnesshttp.NewMemoryRunRegistry()
	store := memory.New()
	bus := eventlog.NewBus()
	now := time.Now().UTC()
	leaseUntil := now.Add(-time.Minute)
	lease, err := registry.Claim(context.Background(), harnesshttp.RunClaim{
		RunID: "run_failed_start", OwnerID: "owner-gone", Status: harnesshttp.RunStarting,
		LeaseUntil: leaseUntil, StartedAt: now.Add(-time.Minute), UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The start failed before the transcript began: the record goes terminal and
	// no event is ever written.
	if err := registry.Release(context.Background(), lease, harnesshttp.RunInfo{
		RunID: "run_failed_start", Status: harnesshttp.RunFailed, OwnerID: "owner-gone",
		StartedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: now,
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	manager := harnesshttp.NewRunManager(newStubAgent(store), store, bus, harnesshttp.RunManagerOptions{
		Registry: registry, OwnerID: "owner-new", TerminalRetention: -1,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	handler, err := New(manager, store, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &fixture{handler: handler, manager: manager, agent: newStubAgent(store), store: store}
	if item := findItem(f.listItems(t), "run_failed_start"); item != nil {
		t.Fatalf("a run with no transcript is listed: %+v", item)
	}
}

// TestSessionCancelStopsTheNewestTurn is the operator's case: the console names
// the session it has open, which is the conversation's first turn, while the
// turn actually running is `<session>~2`. Stop has to reach that turn instead of
// answering a conflict about a finished one (ADR 0113).
func TestSessionCancelStopsTheNewestTurn(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	recorder := f.post(t, "/api/session/prompt",
		rpcBody(t, "rpc-prompt-2", "session/prompt",
			fmt.Sprintf(`{"requestId":"req-2","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"again"}]}`, mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("second prompt failed: %s", recorder.Body.String())
	}
	second := dshsession.ContinuationRunID(sessionID, 2)
	waitForStatus(t, f.manager, second, harnesshttp.RunRunning)

	recorder = f.post(t, "/api/session/cancel", rpcBody(t, "rpc-cancel", "session/cancel",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))))
	var value struct {
		Accepted bool `json:"accepted"`
	}
	decodeValue(t, recorder, &value)
	if !value.Accepted {
		t.Fatalf("cancel of the running turn rejected: %s", recorder.Body.String())
	}
	waitForStatus(t, f.manager, second, harnesshttp.RunCancelled)

	// The first turn keeps what its own log ended with: stopping the newest turn
	// does not rewrite the conversation.
	info, err := f.manager.Get(sessionID)
	if err != nil {
		t.Fatalf("the first turn is no longer known: %v", err)
	}
	if info.Status != harnesshttp.RunCompleted {
		t.Fatalf("first turn status = %q, want it untouched at completed", info.Status)
	}

	// Naming the running turn directly still cancels it, and cancelling it twice
	// stays idempotent.
	recorder = f.post(t, "/api/session/cancel", rpcBody(t, "rpc-cancel-turn", "session/cancel",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, second))))
	decodeValue(t, recorder, &value)
	if !value.Accepted {
		t.Fatalf("cancel by the continuation id rejected: %s", recorder.Body.String())
	}
}

// TestSessionCancelReportsTheTurnItRefused: when the newest turn is finished,
// the conflict names that turn rather than the id the caller used, which is the
// message the operator read while a later turn was still running.
func TestSessionCancelReportsTheTurnItRefused(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.startSession(t)
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	recorder := f.post(t, "/api/session/prompt",
		rpcBody(t, "rpc-prompt-2", "session/prompt",
			fmt.Sprintf(`{"requestId":"req-2","sessionId":%s,"mode":"queue","content":[{"type":"text","text":"again"}]}`, mustJSON(t, sessionID))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("second prompt failed: %s", recorder.Body.String())
	}
	second := dshsession.ContinuationRunID(sessionID, 2)
	waitForStatus(t, f.manager, second, harnesshttp.RunRunning)
	f.agent.finish(second)
	waitForStatus(t, f.manager, second, harnesshttp.RunCompleted)

	recorder = f.post(t, "/api/session/cancel", rpcBody(t, "rpc-cancel", "session/cancel",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, sessionID))))
	envelope := assertMethodFailure(t, recorder, codeSessionConflict)
	if !strings.Contains(envelope.Result.Error.Message, second) {
		t.Fatalf("conflict names the wrong turn: %s", envelope.Result.Error.Message)
	}
}

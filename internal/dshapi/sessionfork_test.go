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
	"github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// answerTurn gives a running turn an answer and finishes it. The model events are
// what the transcript records as an assistant message; the run's own output is
// what the next turn inherits as history. A fork has to carry both.
func (f *fixture) answerTurn(t *testing.T, runID, output string) {
	t.Helper()
	f.agent.append(runID, zenforge.EventModelDelta, map[string]any{"textDelta": output})
	f.agent.append(runID, zenforge.EventModelDone, map[string]any{"step": 1})
	f.agent.append(runID, zenforge.EventRunDone, map[string]any{"output": output})
	f.agent.finish(runID)
	waitForStatus(t, f.manager, runID, harnesshttp.RunCompleted)
}

// pageRecords reads a session's whole projected history the way the console does.
func (f *fixture) pageRecords(t *testing.T, sessionID string) []pageRecord {
	t.Helper()
	recorder := f.post(t, "/api/session/page", rpcBody(t, "rpc-page", "session/page",
		pageArgs(t, sessionID, `"throughSeq":-1,"maxMessages":100`)))
	var value pageValue
	decodeValue(t, recorder, &value)
	return value.Records
}

// forkSession forks and returns the child id.
func (f *fixture) forkSession(t *testing.T, args string) string {
	t.Helper()
	recorder := f.post(t, "/api/session/fork", rpcBody(t, "rpc-fork", "session/fork", args))
	var value struct {
		SessionID string `json:"sessionId"`
	}
	decodeValue(t, recorder, &value)
	if value.SessionID == "" {
		t.Fatalf("fork returned no sessionId: %s", recorder.Body.String())
	}
	return value.SessionID
}

// hasRecordText reports whether any projected record carries the text, whatever
// record type the projection chose for it.
func hasRecordText(records []pageRecord, text string) bool {
	for _, record := range records {
		if strings.Contains(fmt.Sprint(record.Event.Data), text) {
			return true
		}
	}
	return false
}

// recordSeq is the served sequence of the first record carrying the text: the
// number a console page would hand back as atSeq.
func recordSeq(t *testing.T, records []pageRecord, text string) int64 {
	t.Helper()
	for _, record := range records {
		if strings.Contains(fmt.Sprint(record.Event.Data), text) {
			return record.Event.Seq
		}
	}
	t.Fatalf("no record carries %q", text)
	return 0
}

// A fork is a conversation of its own whose history is the source's completed
// turns, and the console must be able to find it: the child is listed, it is not
// blank, and its records are the source's copied under a new session.
func TestSessionForkCopiesCompletedTurnsIntoAChild(t *testing.T) {
	f := newFixture(t, Config{})
	parent := f.startSession(t)
	f.answerTurn(t, parent, "the first answer")
	f.promptMore(t, "req-2", parent, "second question")
	f.answerTurn(t, parent+"~2", "the second answer")
	recorder := f.post(t, "/api/session/rename", rpcBody(t, "rpc-rename", "session/rename",
		fmt.Sprintf(`{"sessionId":%s,"title":"Fork source"}`, mustJSON(t, parent))))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("rename failed: %s", recorder.Body.String())
	}

	time.Sleep(5 * time.Millisecond)
	child := f.forkSession(t, fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, parent)))
	if child == parent {
		t.Fatalf("child = source %q, want a new conversation", child)
	}
	parentRecords := f.pageRecords(t, parent)
	childRecords := f.pageRecords(t, child)
	if len(childRecords) != len(parentRecords) {
		t.Fatalf("child records = %d, want the source's %d", len(childRecords), len(parentRecords))
	}
	for index := range parentRecords {
		if childRecords[index].Event.Type != parentRecords[index].Event.Type {
			t.Fatalf("record %d = %q, want %q", index, childRecords[index].Event.Type, parentRecords[index].Event.Type)
		}
		// The served sequence is the session's own, and the copy has the source's
		// shape, so the console cursors on it exactly as it did on the source.
		if childRecords[index].Event.Seq != parentRecords[index].Event.Seq {
			t.Fatalf("record %d seq = %d, want the source's %d",
				index, childRecords[index].Event.Seq, parentRecords[index].Event.Seq)
		}
	}
	for _, text := range []string{"hello", "the first answer", "second question", "the second answer"} {
		if !hasRecordText(childRecords, text) {
			t.Fatalf("child history is missing %q: %+v", text, childRecords)
		}
	}
	// A conversation's title is part of its log, so a fork carries it -- which is
	// why the console renames the child to an increased title of its own.
	if !hasRecordText(childRecords, "Fork source") {
		t.Fatal("child history is missing the source's title")
	}
	// The child is a session the sidebar shows: listed, not blank, and the newest
	// thing that happened -- a conversation that exists but cannot be listed is
	// one the operator cannot get back to.
	items := f.listItems(t)
	item := findItem(items, child)
	if item == nil {
		t.Fatalf("child %s missing from the session list: %+v", child, items)
	}
	if item.Blank {
		t.Fatal("child is listed blank, want a conversation with history")
	}
	if items[0].SessionID != child {
		t.Fatalf("list starts with %s, want the fork %s", items[0].SessionID, child)
	}
	// The lineage travels with the child, so the console nests the fork under the
	// conversation it came from instead of showing it as a root row -- and a
	// conversation nobody forked carries no parent at all.
	if item.ParentSessionID != parent {
		t.Fatalf("child parentSessionId = %q, want the source %q", item.ParentSessionID, parent)
	}
	if source := findItem(items, parent); source == nil || source.ParentSessionID != "" {
		t.Fatalf("source row = %+v, want no parent", source)
	}
}

// atSeq is the console sequence a page shows: forking at a message keeps that
// message's completed turn and everything before it, and leaves the rest behind.
func TestSessionForkAtSeqKeepsTheTurnsUpToIt(t *testing.T) {
	f := newFixture(t, Config{})
	parent := f.startSession(t)
	f.answerTurn(t, parent, "the first answer")
	f.promptMore(t, "req-2", parent, "second question")
	f.answerTurn(t, parent+"~2", "the second answer")
	records := f.pageRecords(t, parent)

	// The sequence of the first turn's prompt: its turn is completed, so it is a
	// legal cut and the second turn is not inherited.
	firstTurn := recordSeq(t, records, "hello")
	child := f.forkSession(t, fmt.Sprintf(`{"sessionId":%s,"atSeq":%d}`, mustJSON(t, parent), firstTurn))
	childRecords := f.pageRecords(t, child)
	if !hasRecordText(childRecords, "hello") || !hasRecordText(childRecords, "the first answer") {
		t.Fatalf("child history is missing the first turn: %+v", childRecords)
	}
	if hasRecordText(childRecords, "second question") || hasRecordText(childRecords, "the second answer") {
		t.Fatalf("child history reached past the cut: %+v", childRecords)
	}

	// The last record of that turn is the same cut, and a sequence past the end
	// of the log means "the last completed turn" rather than a refusal: that is
	// how the sidebar's own fork button calls this, with no atSeq at all.
	lastSeq := records[len(records)-1].Event.Seq
	past := f.forkSession(t, fmt.Sprintf(`{"sessionId":%s,"atSeq":%d}`, mustJSON(t, parent), lastSeq+1000))
	if got := len(f.pageRecords(t, past)); got != len(records) {
		t.Fatalf("fork past the log = %d records, want the whole conversation's %d", got, len(records))
	}
	if !hasRecordText(f.pageRecords(t, past), "second question") {
		t.Fatal("fork past the log dropped the last completed turn")
	}
}

// A cut inside a turn that has not finished is the reference's own refusal, named
// with the record the caller asked about; and a source with no completed turn at
// all cannot be forked, which is not the same as an unknown session.
func TestSessionForkRefusesAnUnfinishedTurn(t *testing.T) {
	f := newFixture(t, Config{})
	parent := f.startSession(t)
	f.answerTurn(t, parent, "the first answer")
	f.promptMore(t, "req-2", parent, "the unfinished question")
	waitForStatus(t, f.manager, parent+"~2", harnesshttp.RunRunning)

	records := f.pageRecords(t, parent)
	seq := recordSeq(t, records, "the unfinished question")
	envelope := decodeResponse(t, f.post(t, "/api/session/fork", rpcBody(t, "rpc-fork", "session/fork",
		fmt.Sprintf(`{"sessionId":%s,"atSeq":%d}`, mustJSON(t, parent), seq))))
	if envelope.Result.OK {
		t.Fatalf("fork inside a running turn = %s, want a refusal", envelope.Result.Value)
	}
	if envelope.Result.Error.Code != codeForkUnavailable {
		t.Fatalf("code = %q, want %q", envelope.Result.Error.Code, codeForkUnavailable)
	}
	want := fmt.Sprintf("session %q has not completed the turn containing event %d", parent, seq)
	if envelope.Result.Error.Message != want {
		t.Fatalf("message = %q, want %q", envelope.Result.Error.Message, want)
	}

	// Without atSeq the completed turn is what a fork takes, and the running turn
	// is left out rather than copied half-finished.
	child := f.forkSession(t, fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, parent)))
	childRecords := f.pageRecords(t, child)
	if !hasRecordText(childRecords, "the first answer") {
		t.Fatalf("child history is missing the completed turn: %+v", childRecords)
	}
	if hasRecordText(childRecords, "the unfinished question") {
		t.Fatalf("child history contains the unfinished turn: %+v", childRecords)
	}
	// The source keeps running: a fork copies, it does not take the source over.
	if _, err := f.manager.Get(parent + "~2"); err != nil {
		t.Fatalf("source turn disappeared: %v", err)
	}

	// A conversation whose only turn is still running has no completed turn to
	// fork from.
	fresh := f.startSession(t)
	waitForStatus(t, f.manager, fresh, harnesshttp.RunRunning)
	envelope = decodeResponse(t, f.post(t, "/api/session/fork", rpcBody(t, "rpc-fork", "session/fork",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, fresh)))))
	if envelope.Result.OK || envelope.Result.Error.Code != codeForkUnavailable {
		t.Fatalf("fork of a running-only session = %+v, want fork-unavailable", envelope.Result)
	}
	wantFresh := fmt.Sprintf("session %q has no completed turn to fork from", fresh)
	if envelope.Result.Error.Message != wantFresh {
		t.Fatalf("message = %q, want %q", envelope.Result.Error.Message, wantFresh)
	}
}

// The child continues the conversation it inherited: its first new turn carries
// the copied exchange as history, which is what makes a fork a fork instead of a
// lookalike transcript.
func TestForkedSessionContinuesTheInheritedConversation(t *testing.T) {
	f := newFixture(t, Config{})
	parent := f.startSession(t)
	f.answerTurn(t, parent, "the first answer")
	child := f.forkSession(t, fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, parent)))

	f.promptMore(t, "req-9", child, "a follow-up")
	task, ok := f.agent.task(child + "~2")
	if !ok {
		t.Fatalf("no child turn %q: started %v", child+"~2", f.agent.taskRunIDs())
	}
	if task.Input != "a follow-up" {
		t.Fatalf("input = %q, want the new turn's text", task.Input)
	}
	want := []model.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "the first answer"},
	}
	if !reflect.DeepEqual(task.InitialMessages, want) {
		t.Fatalf("initial messages = %+v, want the inherited exchange %+v", task.InitialMessages, want)
	}
}

func TestSessionForkRefusesUnknownSessionsAndBadArguments(t *testing.T) {
	f := newFixture(t, Config{})
	parent := f.startSession(t)
	f.answerTurn(t, parent, "the answer")

	// A session this host never created is not-found; a session it created but
	// never prompted is a conversation with no completed turn.
	recorder := f.post(t, "/api/session/fork", rpcBody(t, "rpc-fork", "session/fork", `{"sessionId":"run-missing"}`))
	assertMethodFailure(t, recorder, codeSessionNotFound)
	draft := f.createSession(t)
	recorder = f.post(t, "/api/session/fork", rpcBody(t, "rpc-fork", "session/fork",
		fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, draft))))
	assertMethodFailure(t, recorder, codeForkUnavailable)

	cases := []struct {
		name string
		args string
		code string
	}{
		{"missing session", `{}`, codeArgumentsInvalid},
		{"not a string", `{"sessionId":7}`, codeArgumentsInvalid},
		{"unknown field", fmt.Sprintf(`{"sessionId":%s,"increaseTitle":true}`, mustJSON(t, parent)), codeArgumentsInvalid},
		{"atSeq not a number", fmt.Sprintf(`{"sessionId":%s,"atSeq":"3"}`, mustJSON(t, parent)), codeArgumentsInvalid},
		{"atSeq negative", fmt.Sprintf(`{"sessionId":%s,"atSeq":-1}`, mustJSON(t, parent)), codeBadRequest},
		{"atSeq fractional", fmt.Sprintf(`{"sessionId":%s,"atSeq":1.5}`, mustJSON(t, parent)), codeBadRequest},
		{"atSeq past the safe range", fmt.Sprintf(`{"sessionId":%s,"atSeq":9007199254740992}`, mustJSON(t, parent)), codeBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assertMethodFailure(t, f.post(t, "/api/session/fork",
				rpcBody(t, "rpc-fork", "session/fork", testCase.args)), testCase.code)
		})
	}
	envelope := decodeResponse(t, f.post(t, "/api/session/fork", rpcBody(t, "rpc-fork", "session/fork",
		fmt.Sprintf(`{"sessionId":%s,"atSeq":-1}`, mustJSON(t, parent)))))
	if envelope.Result.Error.Message != "atSeq must be a non-negative safe integer" {
		t.Fatalf("message = %q, want the reference's own words", envelope.Result.Error.Message)
	}
}

// The envelope is pinned against the vendored bytes: one request object holding
// sessionId and atSeq, and a result of the child's sessionId.
func TestSessionForkEnvelopeMatchesTheVendoredConsole(t *testing.T) {
	console := sessionBundle(t)
	descriptor := console.descriptor(t, "fork")
	wires := console.wireNames(t, descriptor, "fork")
	if !sameStrings(wires, []string{"request"}) {
		t.Fatalf("session/fork wires = %v, want the request object", wires)
	}
	objects := console.objectParameters(t, "fork")
	if len(objects) != 1 {
		t.Fatalf("session/fork object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"atSeq", "sessionId"}) {
		t.Fatalf("session/fork request keys = %v, want the session and the optional cut", keys)
	}
	result := console.schemaExpression(t, "fork", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"sessionId"}) {
		t.Fatalf("session/fork result keys = %v, want the child session", keys)
	}
	if accepted := handlerArgumentNames(t, readSource(t, "sessionfork.go"), "sessionFork"); !sameStrings(accepted, []string{"sessionId", "atSeq"}) {
		t.Fatalf("sessionFork accepts %v, want the flattened request keys", accepted)
	}
}

// A fork is durable state, not a process fact: the child's turns are its own
// events and its run records are in the registry, so a restarted host lists and
// serves it exactly as the host that made it did.
func TestForkedSessionSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store := jsonl.New(dir)
	registryPath := filepath.Join(dir, "runs.sqlite")
	registry, err := harnesshttp.OpenSQLiteRunRegistry(context.Background(), registryPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	first := newFixtureOver(t, store, registry)
	parent := first.startSession(t)
	first.answerTurn(t, parent, "the first answer")
	child := first.forkSession(t, fmt.Sprintf(`{"sessionId":%s}`, mustJSON(t, parent)))
	if err := first.manager.Close(context.Background()); err != nil {
		t.Fatalf("close the first manager: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("close the first registry: %v", err)
	}

	restarted, err := harnesshttp.OpenSQLiteRunRegistry(context.Background(), registryPath)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	second := newFixtureOver(t, store, restarted)
	item := findItem(second.listItems(t), child)
	if item == nil {
		t.Fatal("the restarted host does not list the fork it created")
	}
	if item.Running || item.Blank {
		t.Fatalf("restarted fork row = %+v, want a finished conversation with history", item)
	}
	if records := second.pageRecords(t, child); !hasRecordText(records, "hello") {
		t.Fatalf("restarted fork history = %+v, want the inherited turn", records)
	}
	if item.ParentSessionID != parent {
		t.Fatalf("restarted fork parentSessionId = %q, want %q", item.ParentSessionID, parent)
	}
}

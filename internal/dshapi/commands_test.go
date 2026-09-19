package dshapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// stubCommands is a catalog a test controls: the descriptors the menu lists and
// the expansions a submitted line resolves to.
type stubCommands struct {
	descriptors []CommandDescriptor
	expansions  map[string]string
	lines       []string
}

func (s *stubCommands) Commands() []CommandDescriptor { return s.descriptors }

func (s *stubCommands) Expand(line string) (string, string, bool) {
	s.lines = append(s.lines, line)
	name := strings.TrimPrefix(strings.TrimSpace(line), "/")
	name, _, _ = strings.Cut(name, " ")
	text, ok := s.expansions[name]
	return name, text, ok
}

func commandsFixture(t *testing.T) (*fixture, string, *stubCommands) {
	t.Helper()
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	source := &stubCommands{
		descriptors: []CommandDescriptor{
			{Name: "review", Description: "Review the working tree", Input: &CommandInputDescriptor{Hint: "<path>"}},
			{Name: "commit", Description: "Write a commit"},
		},
		expansions: map[string]string{"review": "Review the working tree now"},
	}
	f.handler.SetCommands(source)
	return f, sessionID, source
}

func TestCommandsListAnswersWithTheCatalog(t *testing.T) {
	f, sessionID, _ := commandsFixture(t)
	var listed []CommandDescriptor
	decodeValue(t, f.post(t, "/api/commands/list",
		rpcBody(t, "s1", "commands/list", `{"agentId":`+jsonString(sessionID)+`}`)), &listed)
	if len(listed) != 2 || listed[0].Name != "review" {
		t.Fatalf("listed = %+v, want the two commands the host has", listed)
	}
	if listed[0].Input == nil || listed[0].Input.Hint != "<path>" {
		t.Fatalf("input = %+v, want the argument hint the command declares", listed[0].Input)
	}
	if listed[1].Input != nil {
		t.Fatalf("input = %+v, want none for a command that takes no arguments", listed[1].Input)
	}
}

// An empty catalog is a fact, not a failure: the menu renders nothing rather than
// reporting that listing failed.
func TestCommandsListAnswersWithAnEmptyArrayWhenTheHostHasNone(t *testing.T) {
	f, sessionID, source := commandsFixture(t)
	source.descriptors = nil
	recorder := f.post(t, "/api/commands/list", rpcBody(t, "s1", "commands/list", `{"agentId":`+jsonString(sessionID)+`}`))
	if body := recorder.Body.String(); !strings.Contains(body, `"value":[]`) {
		t.Fatalf("body = %s, want an empty array", body)
	}
}

// execute is admission: the command's effect is the run it stands for, so the
// successful answer is proven by the run actually starting.
func TestCommandsExecuteStartsTheRunTheCommandStandsFor(t *testing.T) {
	f, sessionID, source := commandsFixture(t)
	var execution CommandExecution
	decodeValue(t, f.post(t, "/api/commands/execute",
		rpcBody(t, "s1", "commands/execute",
			`{"agentId":`+jsonString(sessionID)+`,"line":"/review src","submittedAttachments":[]}`)), &execution)
	if execution.CommandID != "review" || execution.Result.Kind != "success" {
		t.Fatalf("execution = %+v, want the review command admitted", execution)
	}
	if len(source.lines) != 1 || source.lines[0] != "/review src" {
		t.Fatalf("lines = %v, want the submitted line resolved once", source.lines)
	}
	// The run is real: the session reaches running with the expanded text as its
	// input, which is what the console then follows.
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunRunning)
	page := decodeValueMap(t, f.post(t, "/api/session/page",
		rpcBody(t, "s2", "session/page",
			`{"address":{"kind":"session","sessionId":`+jsonString(sessionID)+`},"throughSeq":-1}`)))
	if !strings.Contains(page, "Review the working tree now") {
		t.Fatalf("page = %s, want the expanded command text recorded as the run's input", page)
	}
}

// A line this host has no command for is answered without a value: that is how the
// composer knows to say "unknown or malformed command" and keep the draft.
func TestCommandsExecuteAnswersAnUnknownLineWithoutAValue(t *testing.T) {
	f, sessionID, _ := commandsFixture(t)
	recorder := f.post(t, "/api/commands/execute",
		rpcBody(t, "s1", "commands/execute", `{"agentId":`+jsonString(sessionID)+`,"line":"/nope"}`))
	envelope := decodeResponse(t, recorder)
	if !envelope.Result.OK {
		t.Fatalf("result = %+v, want ok: an unknown command is not a transport failure", envelope.Result.Error)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result := body["result"].(map[string]any)
	if _, present := result["value"]; present {
		t.Fatalf("result = %v, want no value so the composer reports the command as unknown", result)
	}
	// A path is not a command invocation, so it is unknown here too rather than
	// being expanded into a task.
	pathRecorder := f.post(t, "/api/commands/execute",
		rpcBody(t, "s2", "commands/execute", `{"agentId":`+jsonString(sessionID)+`,"line":"/usr/local/bin/thing"}`))
	if body := pathRecorder.Body.String(); !strings.Contains(body, `"ok":true`) || strings.Contains(body, `"value"`) {
		t.Fatalf("body = %s, want ok with no value", body)
	}
}

func TestCommandsExecuteValidatesItsCall(t *testing.T) {
	f, sessionID, source := commandsFixture(t)
	cases := []struct {
		name string
		body string
		code string
	}{
		{"no line", `{"agentId":` + jsonString(sessionID) + `}`, codeArgumentsInvalid},
		{"no agent", `{"line":"/review"}`, codeArgumentsInvalid},
		{"an unknown session", `{"agentId":"run_nobody","line":"/review"}`, codeSessionNotFound},
		{"attachments this host cannot carry", `{"agentId":` + jsonString(sessionID) + `,"line":"/review","submittedAttachments":[{"type":"file","receiptId":"r"}]}`, codeUnimplemented},
		{"attachments that are not an array", `{"agentId":` + jsonString(sessionID) + `,"line":"/review","submittedAttachments":7}`, codeBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source.lines = nil
			response := decodeResponse(t, f.post(t, "/api/commands/execute",
				rpcBody(t, "s1", "commands/execute", testCase.body)))
			if response.Result.OK || response.Result.Error.Code != testCase.code {
				t.Fatalf("code = %q, want %q", response.Result.Error.Code, testCase.code)
			}
			if len(source.lines) != 0 {
				t.Fatalf("lines = %v, want the catalog untouched by a refused call", source.lines)
			}
		})
	}
}

func TestCommandsWithoutACatalogAnswerUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	for _, method := range []string{"commands/list", "commands/execute"} {
		response := decodeResponse(t, f.post(t, "/api/"+method,
			rpcBody(t, "s1", method, `{"agentId":`+jsonString(sessionID)+`,"line":"/review"}`)))
		if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want unimplemented", method, response.Result.Error.Code)
		}
		if response.Result.Error.Details["dependency"] != "CommandSource" {
			t.Fatalf("%s: details = %v, want the dependency named", method, response.Result.Error.Details)
		}
	}
}

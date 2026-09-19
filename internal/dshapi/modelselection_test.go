package dshapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// stubSelections records what the prompt path applied and what selectModel stored.
type stubSelections struct {
	selected []ModelSelection
	sessions []string
	fail     error
	applyErr error
}

func (s *stubSelections) SelectModel(sessionID string, selection ModelSelection) (ModelSelection, error) {
	if s.fail != nil {
		return ModelSelection{}, s.fail
	}
	s.selected = append(s.selected, selection)
	return selection, nil
}

func (s *stubSelections) ApplyModelSelection(sessionID string) error {
	s.sessions = append(s.sessions, sessionID)
	return s.applyErr
}

func selectionFixture(t *testing.T) (*fixture, *stubSelections) {
	t.Helper()
	f := newFixture(t, Config{})
	store := &stubSelections{}
	f.handler.SetModelSelections(store)
	return f, store
}

// The picker submits the complete selection and reads the effective one back from
// the session's projection, so the response only has to carry what was resolved.
func TestSessionSelectModelRecordsTheSelection(t *testing.T) {
	f, store := selectionFixture(t)
	var value struct {
		Selected ModelSelection `json:"selected"`
	}
	decodeValue(t, f.post(t, "/api/session/selectModel", rpcBody(t, "rpc-select", "session/selectModel",
		`{"sessionId":"run-1","provider":"acme","model":"acme-large"}`)), &value)
	if value.Selected.Provider != "acme" || value.Selected.Model != "acme-large" {
		t.Fatalf("selected = %+v, want the submitted selection", value.Selected)
	}
	if len(store.selected) != 1 || store.selected[0].Model != "acme-large" {
		t.Fatalf("store = %+v, want the selection recorded", store.selected)
	}
	if store.selected[0].ReasoningEffort != "" {
		t.Fatalf("effort = %q, want it omitted when the request omits it", store.selected[0].ReasoningEffort)
	}
}

func TestSessionSelectModelPassesTheReasoningEffort(t *testing.T) {
	f, store := selectionFixture(t)
	f.post(t, "/api/session/selectModel", rpcBody(t, "rpc-select", "session/selectModel",
		`{"sessionId":"run-1","provider":"acme","model":"acme-large","reasoningEffort":"high"}`))
	if len(store.selected) != 1 || store.selected[0].ReasoningEffort != "high" {
		t.Fatalf("store = %+v, want the effort forwarded to the host", store.selected)
	}
}

// A selection this host cannot serve is refused with the host's reason, which is
// the whole point of resolving against the catalog instead of storing the name.
func TestSessionSelectModelRefusesWhatTheHostCannotServe(t *testing.T) {
	f, store := selectionFixture(t)
	store.fail = errors.New(`model "nope" is not one provider "acme" offers`)
	response := decodeResponse(t, f.post(t, "/api/session/selectModel", rpcBody(t, "rpc-select", "session/selectModel",
		`{"sessionId":"run-1","provider":"acme","model":"nope"}`)))
	if response.Result.OK || response.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("result = %+v, want a refusal naming the selection", response.Result)
	}
	if !strings.Contains(response.Result.Error.Message, "is not one provider") {
		t.Fatalf("message = %q, want the host's reason", response.Result.Error.Message)
	}
}

func TestSessionSelectModelRequiresItsArguments(t *testing.T) {
	f, _ := selectionFixture(t)
	cases := []struct {
		name string
		body string
		code string
	}{
		{"no sessionId", `{"provider":"acme","model":"m"}`, codeArgumentsInvalid},
		{"no provider", `{"sessionId":"run-1","model":"m"}`, codeArgumentsInvalid},
		{"no model", `{"sessionId":"run-1","provider":"acme"}`, codeArgumentsInvalid},
		{"a malformed session id", `{"sessionId":"a/b","provider":"acme","model":"m"}`, codeArgumentsInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := decodeResponse(t, f.post(t, "/api/session/selectModel",
				rpcBody(t, "rpc-select", "session/selectModel", testCase.body)))
			if response.Result.OK || response.Result.Error.Code != testCase.code {
				t.Fatalf("result = %+v, want %s", response.Result, testCase.code)
			}
		})
	}
	response := decodeResponse(t, f.post(t, "/api/session/selectModel",
		rpcBody(t, "rpc-select", "session/selectModel", `{"sessionId":"run-1","provider":"a","model":"m","unexpected":1}`)))
	if response.Result.OK || response.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("result = %+v, want unexpected arguments refused", response.Result)
	}
}

func TestSessionSelectModelWithoutAStoreIsANamedGap(t *testing.T) {
	f := newFixture(t, Config{})
	response := decodeResponse(t, f.post(t, "/api/session/selectModel", rpcBody(t, "rpc-select", "session/selectModel",
		`{"sessionId":"run-1","provider":"acme","model":"m"}`)))
	if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
		t.Fatalf("result = %+v, want unimplemented", response.Result)
	}
	if !strings.Contains(response.Result.Error.Message, "SetModelSelections") {
		t.Fatalf("message = %q, want the dependency named", response.Result.Error.Message)
	}
}

// A selection is only real if the run uses it: the prompt path applies the
// session's adapter before the run starts, on the first turn and on every
// continuation.
func TestSessionPromptAppliesTheSelectionBeforeTheFirstRun(t *testing.T) {
	f, store := selectionFixture(t)
	sessionID := f.createSession(t)
	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-1", "session/prompt",
		`{"requestId":"req-1","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if !response.Result.OK {
		t.Fatalf("prompt = %+v, want it accepted", response.Result)
	}
	if len(store.sessions) != 1 || store.sessions[0] != sessionID {
		t.Fatalf("applied = %v, want the pending session applied before its first run", store.sessions)
	}
}

func TestSessionPromptAppliesTheSelectionOnAContinuation(t *testing.T) {
	f, store := selectionFixture(t)
	sessionID := f.startSession(t)
	f.agent.append(sessionID, zenforge.EventRunDone, map[string]any{"output": "hi there"})
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-2", "session/prompt",
		`{"requestId":"req-2","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"again"}]}`)))
	if !response.Result.OK {
		t.Fatalf("continuation = %+v, want it accepted", response.Result)
	}
	if _, ok := f.agent.task(sessionID + "~2"); !ok {
		t.Fatalf("no continuation run: started %v", f.agent.taskRunIDs())
	}
	// startSession prompted the first turn, so a second application is the
	// continuation path applying the session's selection again.
	if len(store.sessions) != 2 || store.sessions[1] != sessionID {
		t.Fatalf("applied = %v, want the selection applied again for the continuation", store.sessions)
	}
}

// A selection that cannot be applied stops the prompt: starting the run anyway
// would use a model the operator did not choose.
func TestSessionPromptRefusesWhenTheSelectionCannotBeApplied(t *testing.T) {
	f, store := selectionFixture(t)
	sessionID := f.createSession(t)
	store.applyErr = errors.New("ACME_API_KEY is not set")
	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-1", "session/prompt",
		`{"requestId":"req-1","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if response.Result.OK || response.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("result = %+v, want the prompt refused", response.Result)
	}
	if !strings.Contains(response.Result.Error.Message, "ACME_API_KEY") {
		t.Fatalf("message = %q, want the host's reason", response.Result.Error.Message)
	}
	// The allocation survives, so retrying after the credential arrives works.
	store.applyErr = nil
	response = decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-2", "session/prompt",
		`{"requestId":"req-2","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if !response.Result.OK {
		t.Fatalf("retry = %+v, want the retry accepted", response.Result)
	}
}

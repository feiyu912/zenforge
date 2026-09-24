package dshapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// stubSelections records what the prompt path asked for and what selectModel
// stored, so a test can assert both halves of the handoff: the route a run is
// started on, and the "used" mark that follows a run that really started.
type stubSelections struct {
	selected []ModelSelection
	// route is the answer ModelRoute gives every session: nil means "no recorded
	// choice", which is a session that must run on the host's configured model.
	route *ModelRoute
	// asked records every session the prompt path asked a route for, and used
	// every session it then marked used, so a test can tell a read from a
	// consumption.
	asked []string
	used  []string
	fail  error
	// routeErr is a recorded choice this host can no longer build.
	routeErr error
}

func (s *stubSelections) SelectModel(sessionID string, selection ModelSelection) (ModelSelection, error) {
	if s.fail != nil {
		return ModelSelection{}, s.fail
	}
	s.selected = append(s.selected, selection)
	return selection, nil
}

func (s *stubSelections) ModelRoute(sessionID string) (ModelRoute, bool, error) {
	s.asked = append(s.asked, sessionID)
	if s.routeErr != nil {
		return ModelRoute{}, false, s.routeErr
	}
	if s.route == nil {
		return ModelRoute{}, false, nil
	}
	return *s.route, true, nil
}

func (s *stubSelections) MarkModelUsed(sessionID string) {
	s.used = append(s.used, sessionID)
}

// routeAdapter is the adapter a stub route carries. The prompt path hands the
// route's adapter to the run as it is, so a test only has to recognize it.
type routeAdapter struct{ name string }

func (a *routeAdapter) Generate(context.Context, model.Request) (*model.Response, error) {
	return nil, nil
}

func (a *routeAdapter) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	return nil, nil
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

// A selection is only real if the run uses it, and it is real for that run alone:
// the route rides on the task the session's run is started with, together with the
// adapter the route built, so no other session's selection can reach it (ADR 0140).
func TestSessionPromptStartsTheRunOnTheSessionsOwnModel(t *testing.T) {
	f, store := selectionFixture(t)
	adapter := &routeAdapter{name: "acme"}
	store.route = &ModelRoute{Provider: "acme", Model: "acme-large", Adapter: adapter}
	sessionID := f.createSession(t)
	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-1", "session/prompt",
		`{"requestId":"req-1","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if !response.Result.OK {
		t.Fatalf("prompt = %+v, want it accepted", response.Result)
	}
	task, ok := f.agent.task(sessionID)
	if !ok {
		t.Fatalf("no run started: %v", f.agent.taskRunIDs())
	}
	if task.ModelProvider != "acme" || task.ModelName != "acme-large" {
		t.Fatalf("route = %q/%q, want the session's own choice", task.ModelProvider, task.ModelName)
	}
	if task.Model != model.Model(adapter) {
		t.Fatalf("adapter = %v, want the one this session's route built", task.Model)
	}
	// The route is consumed only after the run started, never by the read.
	if len(store.used) != 1 || store.used[0] != sessionID {
		t.Fatalf("used = %v, want the session marked used after its run started", store.used)
	}
}

// A session that chose nothing runs on the host's configured adapter: the task
// names no route, so the agent's own model is what serves it.
func TestSessionPromptWithoutAChoiceLeavesTheRunOnTheHostsModel(t *testing.T) {
	f, store := selectionFixture(t)
	sessionID := f.createSession(t)
	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-1", "session/prompt",
		`{"requestId":"req-1","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if !response.Result.OK {
		t.Fatalf("prompt = %+v, want it accepted", response.Result)
	}
	task, ok := f.agent.task(sessionID)
	if !ok {
		t.Fatalf("no run started: %v", f.agent.taskRunIDs())
	}
	if task.Model != nil || task.ModelProvider != "" || task.ModelName != "" {
		t.Fatalf("task = %+v, want no route so the host's configured model serves it", task)
	}
	if len(store.used) != 0 {
		t.Fatalf("used = %v, want nothing marked for a session that chose nothing", store.used)
	}
}

// Every turn of a conversation carries its own session's route, so a later turn
// cannot inherit whichever session ran last.
func TestSessionPromptReadsTheSessionsRouteOnAContinuation(t *testing.T) {
	f, store := selectionFixture(t)
	adapter := &routeAdapter{name: "acme"}
	store.route = &ModelRoute{Provider: "acme", Model: "acme-large", Adapter: adapter}
	sessionID := f.startSession(t)
	f.agent.append(sessionID, zenforge.EventRunDone, map[string]any{"output": "hi there"})
	f.agent.finish(sessionID)
	waitForStatus(t, f.manager, sessionID, harnesshttp.RunCompleted)

	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-2", "session/prompt",
		`{"requestId":"req-2","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"again"}]}`)))
	if !response.Result.OK {
		t.Fatalf("continuation = %+v, want it accepted", response.Result)
	}
	task, ok := f.agent.task(sessionID + "~2")
	if !ok {
		t.Fatalf("no continuation run: started %v", f.agent.taskRunIDs())
	}
	if task.ModelProvider != "acme" || task.ModelName != "acme-large" || task.Model != model.Model(adapter) {
		t.Fatalf("continuation task = %+v, want the session's own route on it too", task)
	}
	// startSession prompted the first turn, so the second read and the second
	// "used" mark are the continuation path answering for itself.
	if len(store.asked) != 2 || len(store.used) != 2 {
		t.Fatalf("asked = %v, used = %v, want one of each per turn", store.asked, store.used)
	}
}

// A recorded choice this host can no longer build stops the prompt: starting the
// run anyway would use a model the operator did not choose.
func TestSessionPromptRefusesWhenTheSessionsRouteCannotBeBuilt(t *testing.T) {
	f, store := selectionFixture(t)
	sessionID := f.createSession(t)
	store.routeErr = errors.New("ACME_API_KEY is not set")
	response := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-1", "session/prompt",
		`{"requestId":"req-1","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if response.Result.OK || response.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("result = %+v, want the prompt refused", response.Result)
	}
	if !strings.Contains(response.Result.Error.Message, "ACME_API_KEY") {
		t.Fatalf("message = %q, want the host's reason", response.Result.Error.Message)
	}
	if len(f.agent.taskRunIDs()) != 0 {
		t.Fatalf("started %v, want no run for a prompt that was refused", f.agent.taskRunIDs())
	}
	if len(store.used) != 0 {
		t.Fatalf("used = %v, want nothing marked used for a run that never started", store.used)
	}
	// The allocation survives, so retrying after the credential arrives works.
	store.routeErr = nil
	store.route = &ModelRoute{Provider: "acme", Model: "acme-large", Adapter: &routeAdapter{name: "acme"}}
	response = decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "req-2", "session/prompt",
		`{"requestId":"req-2","sessionId":"`+sessionID+`","mode":"queue","content":[{"type":"text","text":"hi"}]}`)))
	if !response.Result.OK {
		t.Fatalf("retry = %+v, want the retry accepted", response.Result)
	}
	if len(store.used) != 1 {
		t.Fatalf("used = %v, want the retry's run marked used", store.used)
	}
}

package dshapi

import (
	"encoding/json"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshstream"
)

// The shipped console names its parameters in the generated remote map, so a
// method whose only parameter is called "request" arrives with its fields inside
// that object. These shapes were captured from a live console session, not
// invented: session/create came in as {"request":{"workspaceId":"ws-..."}},
// session/list as {"_request":{}} and llm/discoverModels as
// {"request":{...},"settingsNs":"llm-pi-ai"}.

func TestUnwrapRequestArguments(t *testing.T) {
	decode := func(t *testing.T, body string) map[string]json.RawMessage {
		t.Helper()
		var args map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &args); err != nil {
			t.Fatalf("decode args: %v", err)
		}
		return args
	}
	t.Run("fields move up and siblings stay", func(t *testing.T) {
		flat := unwrapRequestArguments(decode(t, `{"request":{"provider":"qwen"},"settingsNs":"llm-pi-ai"}`))
		if string(flat["provider"]) != `"qwen"` {
			t.Fatalf("provider = %s, want the request field", flat["provider"])
		}
		if string(flat["settingsNs"]) != `"llm-pi-ai"` {
			t.Fatalf("settingsNs = %s, want the sibling parameter kept", flat["settingsNs"])
		}
		if _, stale := flat["request"]; stale {
			t.Fatal("the request wrapper survived the unwrap")
		}
	})
	t.Run("the underscore spelling is the same shape", func(t *testing.T) {
		flat := unwrapRequestArguments(decode(t, `{"_request":{"cursor":"c1"}}`))
		if string(flat["cursor"]) != `"c1"` {
			t.Fatalf("cursor = %s, want the request field", flat["cursor"])
		}
	})
	t.Run("a field in both places keeps the request's value", func(t *testing.T) {
		flat := unwrapRequestArguments(decode(t, `{"request":{"provider":"inner"},"provider":"outer"}`))
		if string(flat["provider"]) != `"inner"` {
			t.Fatalf("provider = %s, want the request object's value", flat["provider"])
		}
	})
	t.Run("a request that is not an object is left for the handler", func(t *testing.T) {
		flat := unwrapRequestArguments(decode(t, `{"request":"baseURL","settingsNs":"llm-pi-ai"}`))
		if string(flat["request"]) != `"baseURL"` {
			t.Fatalf("request = %s, want it untouched so the handler can refuse it", flat["request"])
		}
	})
	t.Run("args without a wrapper are untouched", func(t *testing.T) {
		flat := unwrapRequestArguments(decode(t, `{"path":"/srv/host"}`))
		if string(flat["path"]) != `"/srv/host"` {
			t.Fatalf("path = %s, want it untouched", flat["path"])
		}
	})
}

// TestRequestWrappedCallsReachTheirHandler drives the real wire shapes through
// the dispatcher. A handler reading a field the wrapper hides would answer with
// the wrong behaviour, not with an error -- which is how session/create used to
// ignore its workspaceId and leave the console's session ungrouped.
func TestRequestWrappedCallsReachTheirHandler(t *testing.T) {
	t.Run("session/create groups the session it was asked for", func(t *testing.T) {
		f, stub := withWorkspaces(t)
		recorder := f.post(t, "/api/session/create",
			rpcBody(t, "r1", "session/create", `{"request":{"workspaceId":"ws-host"}}`))
		var value struct {
			SessionID string `json:"sessionId"`
		}
		decodeValue(t, recorder, &value)
		if len(stub.attached) != 1 || stub.attached[0] != "ws-host/"+value.SessionID {
			t.Fatalf("attached = %v, want the session under ws-host", stub.attached)
		}
	})
	t.Run("session/create still refuses a field the client cannot send", func(t *testing.T) {
		f, _ := withWorkspaces(t)
		recorder := f.post(t, "/api/session/create",
			rpcBody(t, "r1", "session/create", `{"workspaceId":"ws-host","workSpaceId":"ws-other"}`))
		assertMethodFailure(t, recorder, codeArgumentsInvalid)
	})
	t.Run("session/list reads the cursor it was given", func(t *testing.T) {
		f := newFixture(t, Config{})
		recorder := f.post(t, "/api/session/list", rpcBody(t, "r1", "session/list", `{"_request":{}}`))
		envelope := decodeResponse(t, recorder)
		if !envelope.Result.OK {
			t.Fatalf("session/list failed: %+v", envelope.Result.Error)
		}
	})
	t.Run("session/cancel names the session it could not find", func(t *testing.T) {
		f := newFixture(t, Config{})
		recorder := f.post(t, "/api/session/cancel",
			rpcBody(t, "r1", "session/cancel", `{"request":{"sessionId":"run-unknown"}}`))
		assertMethodFailure(t, recorder, codeSessionNotFound)
	})
	t.Run("session/rename renames a session this host knows", func(t *testing.T) {
		f := newFixture(t, Config{})
		created := f.post(t, "/api/session/create", rpcBody(t, "r1", "session/create", `{}`))
		var session struct {
			SessionID string `json:"sessionId"`
		}
		decodeValue(t, created, &session)
		recorder := f.post(t, "/api/session/rename", rpcBody(t, "r1", "session/rename",
			`{"request":{"sessionId":"`+session.SessionID+`","title":"renamed"}}`))
		envelope := decodeResponse(t, recorder)
		// A pending session has no durable log to rename, which this host says
		// by name. What the assertion is about is that the wrapped sessionId
		// arrived: an unread one would be an argument error naming sessionId.
		if envelope.Result.OK {
			return
		}
		if envelope.Result.Error.Code == codeArgumentsInvalid {
			t.Fatalf("rename was refused as an argument error: %+v", envelope.Result.Error)
		}
		if envelope.Result.Error.Details["sessionId"] != session.SessionID {
			t.Fatalf("details = %v, want the wrapped sessionId echoed", envelope.Result.Error.Details)
		}
	})
	t.Run("workspace/create adopts the directory it was given", func(t *testing.T) {
		f, _ := withWorkspaces(t)
		recorder := f.post(t, "/api/workspace/create",
			rpcBody(t, "r1", "workspace/create", `{"request":{"path":"/srv/added"}}`))
		var value struct {
			Workspace dshstream.WorkspaceView `json:"workspace"`
			Created   bool                    `json:"created"`
		}
		decodeValue(t, recorder, &value)
		if value.Workspace.Path != "/srv/added" {
			t.Fatalf("workspace = %+v, want the adopted directory", value.Workspace)
		}
	})
	t.Run("a sibling parameter beside the request still arrives", func(t *testing.T) {
		// llm/discoverModels is declared (request, settingsNs): both have to
		// survive the unwrap, or the method refuses for the wrong reason.
		f := llmFixture(t, LlmDirectory{})
		recorder := f.post(t, "/api/llm/discoverModels", rpcBody(t, "l1", "llm/discoverModels",
			`{"request":{"provider":"nope"},"settingsNs":"llm-pi-ai"}`))
		envelope := decodeResponse(t, recorder)
		if envelope.Result.OK {
			t.Fatal("discovery of an unknown provider succeeded")
		}
		if envelope.Result.Error.Code == codeArgumentsInvalid {
			t.Fatalf("discovery was refused as an argument error: %+v", envelope.Result.Error)
		}
		if envelope.Result.Error.Details["settingsNs"] != "llm-pi-ai" {
			t.Fatalf("details = %v, want the namespace echoed", envelope.Result.Error.Details)
		}
	})
}

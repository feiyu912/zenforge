package dshapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// stubFileReferences records the queries it is asked and answers a fixed list, so
// the handler's own contract (scope, argument handling, array shape, the nil
// dependency) is what the tests observe.
type stubFileReferences struct {
	candidates []FileReference
	err        error
	queries    []string
	sessions   []string
}

func (s *stubFileReferences) FileReferenceCandidates(_ context.Context, sessionID, query string) ([]FileReference, error) {
	s.queries = append(s.queries, query)
	s.sessions = append(s.sessions, sessionID)
	return s.candidates, s.err
}

// fileReferenceCall posts fileReferences/list the way the console does: the scope
// arrives as `agentId` (the reference's own `wire`, the same one the goals
// namespace is served under), and the query follows `@`.
func fileReferenceCall(t *testing.T, f *fixture, args string) ([]FileReference, *responseError) {
	t.Helper()
	recorder := f.post(t, "/api/fileReferences/list", rpcBody(t, "rpc-refs", "fileReferences/list", args))
	envelope := decodeResponse(t, recorder)
	if !envelope.Result.OK {
		return nil, envelope.Result.Error
	}
	var candidates []FileReference
	decodeValue(t, recorder, &candidates)
	return candidates, nil
}

func TestFileReferencesListAnswersAnArray(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	source := &stubFileReferences{candidates: []FileReference{
		{Path: "docs", Kind: "directory"},
		{Path: "main.go", Kind: "file"},
	}}
	f.handler.SetFileReferences(source)

	candidates, failure := fileReferenceCall(t, f,
		fmt.Sprintf(`{"agentId":%s,"query":"main"}`, mustJSON(t, sessionID)))
	if failure != nil {
		t.Fatalf("fileReferences/list = %+v, want the candidate array", failure)
	}
	if len(candidates) != 2 || candidates[0].Path != "docs" || candidates[1].Kind != "file" {
		t.Fatalf("candidates = %+v, want the source's own rows in order", candidates)
	}
	if len(source.sessions) != 1 || source.sessions[0] != sessionID {
		t.Fatalf("sessions = %v, want the session the scope named", source.sessions)
	}
	if len(source.queries) != 1 || source.queries[0] != "main" {
		t.Fatalf("queries = %v, want the query passed through", source.queries)
	}

	// An empty answer is `[]`, never null: the console maps the array straight
	// into its menu.
	source.candidates = nil
	recorder := f.post(t, "/api/fileReferences/list", rpcBody(t, "rpc-empty", "fileReferences/list",
		fmt.Sprintf(`{"agentId":%s,"query":""}`, mustJSON(t, sessionID))))
	if body := recorder.Body.String(); !strings.Contains(body, `"value":[]`) {
		t.Fatalf("body = %s, want an empty array", body)
	}
}

// The scope is a session: an unknown one is refused by name, and a missing or
// malformed scope is an argument error rather than a silent answer for the host's
// own directory.
func TestFileReferencesListScopeRefusals(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetFileReferences(&stubFileReferences{})
	sessionID := f.createSession(t)

	if _, failure := fileReferenceCall(t, f, `{"agentId":"run-nobody","query":""}`); failure == nil ||
		failure.Code != codeSessionNotFound || failure.Details["sessionId"] != "run-nobody" {
		t.Fatalf("unknown scope = %+v, want session/not-found naming it", failure)
	}

	for _, testCase := range []struct{ name, args, message string }{
		{"missing scope", `{"query":""}`, `argument "agentId" is required`},
		{"missing query", fmt.Sprintf(`{"agentId":%s}`, mustJSON(t, sessionID)), `argument "query" is required`},
		{"query of the wrong type", fmt.Sprintf(`{"agentId":%s,"query":1}`, mustJSON(t, sessionID)), `argument "query" must be a string`},
		{"unknown argument", fmt.Sprintf(`{"agentId":%s,"query":"","path":"y"}`, mustJSON(t, sessionID)), `unexpected argument "path"`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := f.post(t, "/api/fileReferences/list", rpcBody(t, "rpc-bad", "fileReferences/list", testCase.args))
			envelope := assertMethodFailure(t, recorder, codeArgumentsInvalid)
			if envelope.Result.Error.Message != testCase.message {
				t.Fatalf("message = %q, want %q", envelope.Result.Error.Message, testCase.message)
			}
		})
	}
}

// A host with no readable workspace answers unimplemented naming the dependency,
// which is the same honest refusal the skills and commands namespaces use.
func TestFileReferencesListWithoutASource(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)

	recorder := f.post(t, "/api/fileReferences/list", rpcBody(t, "rpc-none", "fileReferences/list",
		fmt.Sprintf(`{"agentId":%s,"query":""}`, mustJSON(t, sessionID))))
	envelope := assertMethodFailure(t, recorder, codeUnimplemented)
	if envelope.Result.Error.Details["dependency"] != "FileReferenceSource" {
		t.Fatalf("details = %+v, want the dependency named", envelope.Result.Error.Details)
	}
}

// A source failure is an internal error, not an empty menu: an operator cannot act
// on "no files" when the host could not read the tree.
func TestFileReferencesListReportsASourceFailure(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	f.handler.SetFileReferences(&stubFileReferences{err: errors.New("readdir: permission denied")})

	recorder := f.post(t, "/api/fileReferences/list", rpcBody(t, "rpc-fail", "fileReferences/list",
		fmt.Sprintf(`{"agentId":%s,"query":""}`, mustJSON(t, sessionID))))
	envelope := assertMethodFailure(t, recorder, codeInternal)
	if !strings.Contains(envelope.Result.Error.Message, "readdir: permission denied") {
		t.Fatalf("message = %q, want the source's reason", envelope.Result.Error.Message)
	}
}

// The wire is the vendored console's: the scope's `agentId`, a required `query`,
// and a result that is a bare array of `{path, kind}` -- no envelope, no items
// wrapper, and a closed two-value kind.
func TestFileReferencesEnvelopeMatchesTheVendoredConsole(t *testing.T) {
	bundle := vendoredBundle{
		source: readSource(t, goalRemotePath),
		pkg:    "@deepseek-ai/dsh-api-session-controller",
		ns:     "fileReferences",
	}
	descriptor := bundle.descriptor(t, "list")
	wires := bundle.wireNames(t, descriptor, "list")
	if !sameStrings(wires, []string{"agentId", "query"}) {
		t.Fatalf("wires = %v, want the agent scope and the query", wires)
	}
	if !strings.Contains(descriptor, `source: "lookup"`) || !strings.Contains(descriptor, `context: "agent"`) {
		t.Fatalf("descriptor = %s, want the agent scope the handler reads", descriptor)
	}

	result := bundle.schemaExpression(t, "list", "result")
	if !strings.Contains(result, "array(object({") {
		t.Fatalf("result = %s, want a bare array result", result)
	}
	if !strings.Contains(result, `"path": string()`) {
		t.Fatalf("result = %s, want each row to carry a path", result)
	}
	if !strings.Contains(result, `union([literal("file"), literal("directory")])`) {
		t.Fatalf("result = %s, want the closed kind union the host answers with", result)
	}
	if strings.Contains(result, `"ok": literal(true)`) {
		t.Fatalf("result = %s, want no outcome envelope here", result)
	}
}

package dshapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshstream"
)

// stubWorkspaces is a scripted registry: the handler tests care about the
// refusals and the values that cross the wire, and a real registry (the serve
// command's) has its own tests in the cli package. Importing it here would
// cycle, because it imports this package.
type stubWorkspaces struct {
	root     string
	views    map[string]dshstream.WorkspaceView
	created  bool
	archived []string
	attached []string

	createErr error
	renameErr error
	deleteErr error
	attachErr error
	insertErr error
	moveErr   error

	// moves records the manual-order calls the handlers made, so a test can pin
	// the arguments the console's wire actually produced.
	moves []string
}

func newStubWorkspaces(root string) *stubWorkspaces {
	return &stubWorkspaces{root: root, views: map[string]dshstream.WorkspaceView{}}
}

func (s *stubWorkspaces) Create(path string) (dshstream.WorkspaceView, bool, error) {
	if s.createErr != nil {
		return dshstream.WorkspaceView{}, false, s.createErr
	}
	view := dshstream.WorkspaceView{
		WorkspaceID: "ws-created",
		Path:        path,
		Title:       "created",
		SessionIDs:  []string{},
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}
	s.views[view.WorkspaceID] = view
	return view, s.created, nil
}

func (s *stubWorkspaces) Rename(workspaceID, title string) (dshstream.WorkspaceView, error) {
	if s.renameErr != nil {
		return dshstream.WorkspaceView{}, s.renameErr
	}
	view, ok := s.views[workspaceID]
	if !ok {
		return dshstream.WorkspaceView{}, notFound(workspaceID)
	}
	view.Title = title
	s.views[workspaceID] = view
	return view, nil
}

func (s *stubWorkspaces) Delete(workspaceID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.views[workspaceID]; !ok {
		return notFound(workspaceID)
	}
	delete(s.views, workspaceID)
	return nil
}

func (s *stubWorkspaces) ArchiveSession(sessionID string) ([]string, error) {
	s.archived = append(s.archived, sessionID)
	return append([]string{}, s.archived...), nil
}

func (s *stubWorkspaces) UnarchiveSession(sessionID string) ([]string, error) {
	remaining := s.archived[:0]
	for _, id := range s.archived {
		if id != sessionID {
			remaining = append(remaining, id)
		}
	}
	s.archived = remaining
	return append([]string{}, s.archived...), nil
}

func (s *stubWorkspaces) Workspace(workspaceID string) (dshstream.WorkspaceView, bool) {
	view, ok := s.views[workspaceID]
	return view, ok
}

func (s *stubWorkspaces) InsertBefore(workspaceID, beforeWorkspaceID string) ([]string, error) {
	s.moves = append(s.moves, "workspace "+workspaceID+" before "+beforeWorkspaceID)
	if s.insertErr != nil {
		return nil, s.insertErr
	}
	order := []string{"ws-host", "ws-other"}
	if _, ok := s.views[workspaceID]; !ok {
		return nil, notFound(workspaceID)
	}
	if beforeWorkspaceID != "" {
		if _, ok := s.views[beforeWorkspaceID]; !ok {
			return nil, notFound(beforeWorkspaceID)
		}
	}
	without := make([]string, 0, len(order))
	for _, id := range order {
		if id != workspaceID {
			without = append(without, id)
		}
	}
	if beforeWorkspaceID == "" {
		return append(without, workspaceID), nil
	}
	moved := []string{}
	for _, id := range without {
		if id == beforeWorkspaceID {
			moved = append(moved, workspaceID)
		}
		moved = append(moved, id)
	}
	return moved, nil
}

func (s *stubWorkspaces) InsertSessionBefore(workspaceID, sessionID, beforeSessionID string) (dshstream.WorkspaceView, error) {
	s.moves = append(s.moves, "session "+sessionID+" before "+beforeSessionID+" in "+workspaceID)
	if s.moveErr != nil {
		return dshstream.WorkspaceView{}, s.moveErr
	}
	view, ok := s.views[workspaceID]
	if !ok {
		return dshstream.WorkspaceView{}, notFound(workspaceID)
	}
	view.SessionIDs = append([]string{sessionID}, view.SessionIDs...)
	view.UpdatedAt = "2026-01-02T00:00:00Z"
	s.views[workspaceID] = view
	return view, nil
}

func (s *stubWorkspaces) Root() string { return s.root }

func (s *stubWorkspaces) AttachSession(workspaceID, sessionID string) error {
	if s.attachErr != nil {
		return s.attachErr
	}
	s.attached = append(s.attached, workspaceID+"/"+sessionID)
	return nil
}

func notFound(workspaceID string) error {
	return &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeNotFound,
		Message: fmt.Sprintf("workspace %q is not registered on this host", workspaceID),
		Details: map[string]any{"workspaceId": workspaceID},
	}
}

// withWorkspaces installs a stub registry whose table already holds the host's
// own workspace and one row for a different directory.
func withWorkspaces(t *testing.T) (*fixture, *stubWorkspaces) {
	t.Helper()
	f := newFixture(t, Config{})
	stub := newStubWorkspaces("/srv/host")
	stub.views["ws-host"] = dshstream.WorkspaceView{
		WorkspaceID: "ws-host", Path: "/srv/host", Title: "host",
		SessionIDs: []string{}, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	stub.views["ws-other"] = dshstream.WorkspaceView{
		WorkspaceID: "ws-other", Path: "/srv/other", Title: "other",
		SessionIDs: []string{}, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	f.handler.SetWorkspaces(stub)
	return f, stub
}

func TestWorkspaceNamespaceRefusesWithoutARegistry(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct{ method, args string }{
		{"workspace/create", `{"path":"/srv/host"}`},
		{"workspace/rename", `{"workspaceId":"ws-1","title":"x"}`},
		{"workspace/delete", `{"workspaceId":"ws-1"}`},
		{"workspace/insertBefore", `{"workspaceId":"ws-1","beforeWorkspaceId":"ws-2"}`},
		{"workspace/insertSessionBefore", `{"workspaceId":"ws-1","sessionId":"run-1","beforeSessionId":"run-2"}`},
		{"workspace/archiveSession", `{"sessionId":"run-1"}`},
		{"workspace/unarchiveSession", `{"sessionId":"run-1"}`},
	}
	for _, testCase := range cases {
		recorder := f.post(t, "/api/"+testCase.method, rpcBody(t, "rpc-1", testCase.method, testCase.args))
		envelope := assertMethodFailure(t, recorder, codeUnimplemented)
		if want := "workspace registry"; !strings.Contains(envelope.Result.Error.Message, want) {
			t.Fatalf("%s: message %q does not name %q", testCase.method, envelope.Result.Error.Message, want)
		}
	}
}

func TestWorkspaceCreateReportsRowAndCreatedFlag(t *testing.T) {
	f, stub := withWorkspaces(t)
	stub.created = true
	recorder := f.post(t, "/api/workspace/create",
		rpcBody(t, "rpc-1", "workspace/create", `{"path":"/srv/added"}`))
	var value struct {
		Workspace dshstream.WorkspaceView `json:"workspace"`
		Created   bool                    `json:"created"`
	}
	decodeValue(t, recorder, &value)
	if !value.Created {
		t.Fatal("created = false, want true for a fresh registration")
	}
	if value.Workspace.Path != "/srv/added" || value.Workspace.WorkspaceID == "" {
		t.Fatalf("workspace = %+v, want the registered row", value.Workspace)
	}
}

func TestWorkspaceCreateRefusals(t *testing.T) {
	f, stub := withWorkspaces(t)
	stub.createErr = &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeInvalidPath,
		Message: "/srv/missing cannot back a workspace",
		Details: map[string]any{"path": "/srv/missing"},
	}
	recorder := f.post(t, "/api/workspace/create",
		rpcBody(t, "rpc-1", "workspace/create", `{"path":"/srv/missing"}`))
	envelope := assertMethodFailure(t, recorder, dshstream.WorkspaceCodeInvalidPath)
	if envelope.Result.Error.Details["path"] != "/srv/missing" {
		t.Fatalf("details = %v, want the refused path", envelope.Result.Error.Details)
	}
	// A blank path never reaches the registry, and an unknown argument is
	// refused rather than ignored.
	for _, args := range []string{`{"path":"   "}`, `{}`, `{"path":"/srv/added","extra":1}`} {
		recorder := f.post(t, "/api/workspace/create", rpcBody(t, "rpc-1", "workspace/create", args))
		assertMethodFailure(t, recorder, codeArgumentsInvalid)
	}
}

func TestWorkspaceRenameAndDelete(t *testing.T) {
	f, stub := withWorkspaces(t)
	recorder := f.post(t, "/api/workspace/rename",
		rpcBody(t, "rpc-1", "workspace/rename", `{"workspaceId":"ws-host","title":"renamed"}`))
	var renamed struct {
		Workspace dshstream.WorkspaceView `json:"workspace"`
	}
	decodeValue(t, recorder, &renamed)
	if renamed.Workspace.Title != "renamed" {
		t.Fatalf("title = %q, want renamed", renamed.Workspace.Title)
	}
	for _, args := range []string{
		`{"workspaceId":"ws-unknown","title":"x"}`,
		`{"workspaceId":"ws-host","title":"   "}`,
		`{"workspaceId":"","title":"x"}`,
	} {
		recorder := f.post(t, "/api/workspace/rename", rpcBody(t, "rpc-1", "workspace/rename", args))
		envelope := decodeResponse(t, recorder)
		if envelope.Result.OK {
			t.Fatalf("rename %s succeeded, want a refusal", args)
		}
	}
	stub.deleteErr = &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeRootImmutable,
		Message: "the host workspace cannot be removed",
		Details: map[string]any{"workspaceId": "ws-host", "path": "/srv/host"},
	}
	recorder = f.post(t, "/api/workspace/delete",
		rpcBody(t, "rpc-1", "workspace/delete", `{"workspaceId":"ws-host"}`))
	assertMethodFailure(t, recorder, dshstream.WorkspaceCodeRootImmutable)

	stub.deleteErr = nil
	recorder = f.post(t, "/api/workspace/delete",
		rpcBody(t, "rpc-1", "workspace/delete", `{"workspaceId":"ws-other"}`))
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	decodeValue(t, recorder, &deleted)
	if !deleted.Deleted {
		t.Fatal("deleted = false, want true")
	}
}

func TestWorkspaceArchiveSessionRequiresAKnownSession(t *testing.T) {
	f, _ := withWorkspaces(t)
	recorder := f.post(t, "/api/workspace/archiveSession",
		rpcBody(t, "rpc-1", "workspace/archiveSession", `{"sessionId":"run-unknown"}`))
	assertMethodFailure(t, recorder, codeSessionNotFound)

	// A session session/create has already shown the console is known, which is
	// the ordinary case: the console archives a row it just listed.
	created := f.post(t, "/api/session/create", rpcBody(t, "rpc-1", "session/create", `{}`))
	var session struct {
		SessionID string `json:"sessionId"`
	}
	decodeValue(t, created, &session)
	recorder = f.post(t, "/api/workspace/archiveSession",
		rpcBody(t, "rpc-1", "workspace/archiveSession", fmt.Sprintf(`{"sessionId":%q}`, session.SessionID)))
	var archived struct {
		ArchivedSessionIDs []string `json:"archivedSessionIds"`
	}
	decodeValue(t, recorder, &archived)
	if len(archived.ArchivedSessionIDs) != 1 || archived.ArchivedSessionIDs[0] != session.SessionID {
		t.Fatalf("archivedSessionIds = %v, want [%s]", archived.ArchivedSessionIDs, session.SessionID)
	}
	recorder = f.post(t, "/api/workspace/unarchiveSession",
		rpcBody(t, "rpc-1", "workspace/unarchiveSession", fmt.Sprintf(`{"sessionId":%q}`, session.SessionID)))
	var restored struct {
		ArchivedSessionIDs []string `json:"archivedSessionIds"`
	}
	decodeValue(t, recorder, &restored)
	if len(restored.ArchivedSessionIDs) != 0 {
		t.Fatalf("archivedSessionIds = %v, want none", restored.ArchivedSessionIDs)
	}
}

func TestSessionCreateGroupsSessionInTheHostWorkspace(t *testing.T) {
	f, stub := withWorkspaces(t)
	recorder := f.post(t, "/api/session/create",
		rpcBody(t, "rpc-1", "session/create", `{"workspaceId":"ws-host"}`))
	var value struct {
		SessionID string `json:"sessionId"`
	}
	decodeValue(t, recorder, &value)
	if value.SessionID == "" {
		t.Fatal("sessionId is empty")
	}
	if len(stub.attached) != 1 || stub.attached[0] != "ws-host/"+value.SessionID {
		t.Fatalf("attached = %v, want the session under ws-host", stub.attached)
	}

	// A session created without naming a workspace still belongs to the one
	// this host runs in, otherwise the sidebar could not show it.
	recorder = f.post(t, "/api/session/create", rpcBody(t, "rpc-1", "session/create", `{}`))
	decodeValue(t, recorder, &value)
	if len(stub.attached) != 2 || stub.attached[1] != "/"+value.SessionID {
		t.Fatalf("attached = %v, want the second session under the host workspace", stub.attached)
	}
}

func TestSessionCreateRefusesAWorkspaceThisHostDoesNotRun(t *testing.T) {
	f, _ := withWorkspaces(t)
	recorder := f.post(t, "/api/session/create",
		rpcBody(t, "rpc-1", "session/create", `{"workspaceId":"ws-other"}`))
	envelope := assertMethodFailure(t, recorder, codeUnimplemented)
	for _, want := range []string{"/srv/host", "/srv/other", "--workspace"} {
		if !strings.Contains(envelope.Result.Error.Message, want) {
			t.Fatalf("message %q does not name %q", envelope.Result.Error.Message, want)
		}
	}
	recorder = f.post(t, "/api/session/create",
		rpcBody(t, "rpc-1", "session/create", `{"workspaceId":"ws-unknown"}`))
	assertMethodFailure(t, recorder, codeWorkspaceNotFound)
}

// A registry that fails for its own reasons is reported as an internal failure,
// not silently answered: the console must not read a broken host as a
// successful grouping.
func TestSessionCreateReportsAnUnattachableSessionAsInternal(t *testing.T) {
	f, stub := withWorkspaces(t)
	stub.attachErr = errors.New("registry is closed")
	recorder := f.post(t, "/api/session/create",
		rpcBody(t, "rpc-1", "session/create", `{"workspaceId":"ws-host"}`))
	assertMethodFailure(t, recorder, codeInternal)
}

// The workspace reorder answers the complete order, appends when no anchor is
// named, and keeps the anchor's meaning: a request the console cannot render is a
// regression the result shape catches.
func TestWorkspaceInsertBeforeReturnsTheWholeOrder(t *testing.T) {
	f, stub := withWorkspaces(t)

	recorder := f.post(t, "/api/workspace/insertBefore",
		rpcBody(t, "rpc-1", "workspace/insertBefore", `{"workspaceId":"ws-other","beforeWorkspaceId":"ws-host"}`))
	var value struct {
		WorkspaceIDs []string `json:"workspaceIds"`
	}
	decodeValue(t, recorder, &value)
	if !sameStrings(value.WorkspaceIDs, []string{"ws-other", "ws-host"}) {
		t.Fatalf("workspaceIds = %v, want the moved row before its anchor", value.WorkspaceIDs)
	}

	recorder = f.post(t, "/api/workspace/insertBefore",
		rpcBody(t, "rpc-2", "workspace/insertBefore", `{"workspaceId":"ws-other"}`))
	value.WorkspaceIDs = nil
	decodeValue(t, recorder, &value)
	if !sameStrings(value.WorkspaceIDs, []string{"ws-host", "ws-other"}) {
		t.Fatalf("workspaceIds = %v, want the appended order when no anchor is named", value.WorkspaceIDs)
	}
	if len(stub.moves) != 2 || stub.moves[1] != "workspace ws-other before " {
		t.Fatalf("moves = %v, want the absent anchor passed through as empty", stub.moves)
	}
}

// The session reorder answers the updated row, and the arguments the console
// sends are unpacked from the request object it wraps them in.
func TestWorkspaceInsertSessionBeforeReturnsTheRow(t *testing.T) {
	f, _ := withWorkspaces(t)
	recorder := f.post(t, "/api/workspace/insertSessionBefore",
		rpcBody(t, "rpc-1", "workspace/insertSessionBefore",
			`{"request":{"workspaceId":"ws-host","sessionId":"run-2","beforeSessionId":"run-1"}}`))
	var value struct {
		Workspace dshstream.WorkspaceView `json:"workspace"`
	}
	decodeValue(t, recorder, &value)
	if value.Workspace.WorkspaceID != "ws-host" || len(value.Workspace.SessionIDs) == 0 ||
		value.Workspace.SessionIDs[0] != "run-2" {
		t.Fatalf("workspace = %+v, want the row carrying the moved session", value.Workspace)
	}
}

// Every refusal the registry names reaches the console unchanged, and a move the
// registry cannot perform is upstream's move-invalid with its details.
func TestWorkspaceMoveRefusals(t *testing.T) {
	f, stub := withWorkspaces(t)

	cases := []struct {
		method string
		args   string
		code   string
		detail string
	}{
		{"workspace/insertBefore", `{"workspaceId":"ws-gone"}`, dshstream.WorkspaceCodeNotFound, "workspaceId"},
		{"workspace/insertBefore", `{}`, codeArgumentsInvalid, "argument"},
		{"workspace/insertBefore", `{"workspaceId":"ws-host","anchor":"ws-other"}`, codeArgumentsInvalid, "argument"},
		{"workspace/insertSessionBefore", `{"workspaceId":"ws-host"}`, codeArgumentsInvalid, "argument"},
		{"workspace/insertSessionBefore", `{"workspaceId":"ws-host","sessionId":"run-1","extra":true}`, codeArgumentsInvalid, "argument"},
	}
	for _, testCase := range cases {
		recorder := f.post(t, "/api/"+testCase.method, rpcBody(t, "rpc-1", testCase.method, testCase.args))
		envelope := assertMethodFailure(t, recorder, testCase.code)
		if _, present := envelope.Result.Error.Details[testCase.detail]; !present {
			t.Fatalf("%s %s: details %v lack %q", testCase.method, testCase.args,
				envelope.Result.Error.Details, testCase.detail)
		}
	}

	// A session the workspace does not account is move-invalid, carrying the ids
	// the console needs to say which move it refused.
	stub.moveErr = &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeMoveInvalid,
		Message: `cannot move session "run-1" in workspace "/srv/host": the session is not accounted`,
		Details: map[string]any{"workspaceId": "ws-host", "sessionId": "run-1", "beforeSessionId": "run-2"},
	}
	recorder := f.post(t, "/api/workspace/insertSessionBefore",
		rpcBody(t, "rpc-1", "workspace/insertSessionBefore",
			`{"workspaceId":"ws-host","sessionId":"run-1","beforeSessionId":"run-2"}`))
	envelope := assertMethodFailure(t, recorder, dshstream.WorkspaceCodeMoveInvalid)
	if envelope.Result.Error.Message != stub.moveErr.(*dshstream.WorkspaceError).Message {
		t.Fatalf("message = %q, want the registry's own sentence", envelope.Result.Error.Message)
	}
	if envelope.Result.Error.Details["beforeSessionId"] != "run-2" {
		t.Fatalf("details = %v, want the anchor named", envelope.Result.Error.Details)
	}
}

// Both methods' request and result shapes are the vendored console's, re-derived
// from the bundle rather than restated.
func TestWorkspaceMoveEnvelopesMatchTheVendoredConsole(t *testing.T) {
	console := vendoredBundle{
		source: readSource(t, goalRemotePath),
		pkg:    "@deepseek-ai/dsh-api-workspace-controller",
		ns:     "workspace",
	}
	cases := []struct {
		method      string
		wires       []string
		requestKeys []string
		resultKeys  []string
	}{
		{"insertBefore", []string{"request"}, []string{"beforeWorkspaceId", "workspaceId"}, []string{"workspaceIds"}},
		{"insertSessionBefore", []string{"request"},
			[]string{"beforeSessionId", "sessionId", "workspaceId"}, []string{"workspace"}},
	}
	for _, testCase := range cases {
		descriptor := console.descriptor(t, testCase.method)
		if wires := console.wireNames(t, descriptor, testCase.method); !sameStrings(wires, testCase.wires) {
			t.Fatalf("%s wires = %v, want %v", testCase.method, wires, testCase.wires)
		}
		objects := console.objectParameters(t, testCase.method)
		if len(objects) != 1 {
			t.Fatalf("%s object parameters = %v, want one request object", testCase.method, objects)
		}
		if keys := sortedKeys(objects[0]); !sameStrings(keys, testCase.requestKeys) {
			t.Fatalf("%s request keys = %v, want %v", testCase.method, keys, testCase.requestKeys)
		}
		result := console.schemaExpression(t, testCase.method, "result")
		if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, testCase.resultKeys) {
			t.Fatalf("%s result keys = %v, want %v", testCase.method, keys, testCase.resultKeys)
		}
	}
}

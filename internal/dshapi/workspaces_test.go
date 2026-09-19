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

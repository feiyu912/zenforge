package dshapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// stubWorkspaceFiles answers with what a test hands it, so the handler's own
// rules -- scope validation, range validation, the entry cap, the refusals -- are
// what the assertions are about rather than a filesystem.
type stubWorkspaceFiles struct {
	listing WorkspaceDirectoryListing
	stat    WorkspaceFileStat
	page    WorkspaceFileText
	bytes   WorkspaceFileBytes
	err     error
	calls   []string
}

func (s *stubWorkspaceFiles) List(string) (WorkspaceDirectoryListing, error) {
	s.calls = append(s.calls, "list")
	return s.listing, s.err
}

func (s *stubWorkspaceFiles) Stat(string) (WorkspaceFileStat, error) {
	s.calls = append(s.calls, "stat")
	return s.stat, s.err
}

func (s *stubWorkspaceFiles) ReadPage(string, int, int) (WorkspaceFileText, error) {
	s.calls = append(s.calls, "read")
	return s.page, s.err
}

func (s *stubWorkspaceFiles) ReadAll(string) (WorkspaceFileText, error) {
	s.calls = append(s.calls, "readAll")
	return s.page, s.err
}

func (s *stubWorkspaceFiles) ReadBytes(string, int, int) (WorkspaceFileBytes, error) {
	s.calls = append(s.calls, "readBytes")
	return s.bytes, s.err
}

func workspaceFileFixture(t *testing.T) (*fixture, string, *stubWorkspaceFiles) {
	t.Helper()
	f := newFixture(t, Config{})
	// The scope must be a session this host serves. A session the manager lists
	// is one that has started, which is exactly when the console's sidebar has a
	// directory to show.
	scope := f.startSession(t)
	store := &stubWorkspaceFiles{
		listing: WorkspaceDirectoryListing{Path: "src", Entries: []WorkspaceDirectoryEntry{
			{Name: "main.go", Type: "file"},
			{Name: "vendor", Type: "directory"},
		}},
		stat:  WorkspaceFileStat{AbsolutePath: "/ws/src/main.go", Version: "sha", Bytes: int64Pointer(11)},
		page:  WorkspaceFileText{Offset: 2, Text: "two", Lines: 1, EOF: false},
		bytes: WorkspaceFileBytes{Offset: 4, Data: "dHdv", EOF: false},
	}
	f.handler.SetWorkspaceFiles(store)
	return f, scope, store
}

func int64Pointer(value int64) *int64 { return &value }

func workspaceFileArgs(scope, extra string) string {
	if extra == "" {
		return fmt.Sprintf(`{"workspaceFileScopeId":%q}`, scope)
	}
	return fmt.Sprintf(`{"workspaceFileScopeId":%q,%s}`, scope, extra)
}

func TestWorkspaceFilesListAnswersWithTheDirectory(t *testing.T) {
	f, scope, store := workspaceFileFixture(t)
	var listing WorkspaceDirectoryListing
	decodeValue(t, f.post(t, "/api/workspaceFiles/list",
		rpcBody(t, "s1", "workspaceFiles/list", workspaceFileArgs(scope, `"path":"src"`))), &listing)
	if len(listing.Entries) != 2 {
		t.Fatalf("entries = %+v, want the two the host returned", listing.Entries)
	}
	if listing.Path != "src" || listing.Truncated {
		t.Fatalf("listing = %+v, want the requested path and no truncation", listing)
	}
	if len(store.calls) != 1 || store.calls[0] != "list" {
		t.Fatalf("calls = %v, want one list", store.calls)
	}
}

// Upstream caps the entry list and reports the cut rather than failing
// (src/index.ts:349-350); the handler applies it so a stub cannot decide.
func TestWorkspaceFilesListCapsEntriesAndReportsTheCut(t *testing.T) {
	f, scope, store := workspaceFileFixture(t)
	store.listing.Entries = make([]WorkspaceDirectoryEntry, WorkspaceDirMaxEntries+5)
	for index := range store.listing.Entries {
		store.listing.Entries[index] = WorkspaceDirectoryEntry{Name: fmt.Sprintf("file-%04d", index), Type: "file"}
	}
	var listing WorkspaceDirectoryListing
	decodeValue(t, f.post(t, "/api/workspaceFiles/list",
		rpcBody(t, "s1", "workspaceFiles/list", workspaceFileArgs(scope, `"path":""`))), &listing)
	if len(listing.Entries) != WorkspaceDirMaxEntries {
		t.Fatalf("entries = %d, want the cap %d", len(listing.Entries), WorkspaceDirMaxEntries)
	}
	if !listing.Truncated {
		t.Fatal("truncated = false, want true: the listing dropped entries")
	}
}

func TestWorkspaceFilesReadAppliesUpstreamsPageRules(t *testing.T) {
	f, scope, store := workspaceFileFixture(t)
	var page WorkspaceFileText
	decodeValue(t, f.post(t, "/api/workspaceFiles/read",
		rpcBody(t, "s1", "workspaceFiles/read",
			workspaceFileArgs(scope, `"path":"src/main.go","range":{"offset":2,"limit":1}`))), &page)
	if page.Text != "two" || page.Offset != 2 || page.EOF {
		t.Fatalf("page = %+v, want the second line and eof false", page)
	}
	// The default page is the maximum page; a larger one is refused rather than
	// silently cut, so the caller never reads a shortened page as the whole one.
	cases := []struct {
		name string
		body string
	}{
		{"an offset below one", `"path":"f","range":{"offset":0}`},
		{"a limit above the page cap", fmt.Sprintf(`"path":"f","range":{"limit":%d}`, WorkspacePageMaxLines+1)},
		{"a zero limit", `"path":"f","range":{"limit":0}`},
		{"a range that is not an object", `"path":"f","range":7`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store.calls = nil
			response := decodeResponse(t, f.post(t, "/api/workspaceFiles/read",
				rpcBody(t, "s1", "workspaceFiles/read", workspaceFileArgs(scope, testCase.body))))
			if response.Result.OK || response.Result.Error.Code != codeBadRequest {
				t.Fatalf("code = %q, want %q", response.Result.Error.Code, codeBadRequest)
			}
			if len(store.calls) != 0 {
				t.Fatalf("calls = %v, want the host untouched by a refused range", store.calls)
			}
		})
	}
	// A range that omits its fields takes the host's defaults.
	store.calls = nil
	response := decodeResponse(t, f.post(t, "/api/workspaceFiles/read",
		rpcBody(t, "s1", "workspaceFiles/read", workspaceFileArgs(scope, `"path":"f","range":{}`))))
	if !response.Result.OK {
		t.Fatalf("an empty range was refused: %s", response.Result.Value)
	}
	if len(store.calls) != 1 {
		t.Fatalf("calls = %v, want the host read once", store.calls)
	}
}

func TestWorkspaceFileRefusalsCarryUpstreamsCodes(t *testing.T) {
	f, scope, store := workspaceFileFixture(t)
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"a missing path", &WorkspaceFileError{Code: codeWorkspaceFileNotFound, Details: map[string]any{"path": "gone"}}, codeWorkspaceFileNotFound},
		{"a path outside the workspace", &WorkspaceFileError{Code: codeWorkspaceFileOutside, Details: map[string]any{"path": "../x"}}, codeWorkspaceFileOutside},
		{"a page over the cap", &WorkspaceFileError{Code: codeWorkspaceFileTooLarge, Details: map[string]any{"path": "big", "limit": WorkspacePageMaxBytes}}, codeWorkspaceFileTooLarge},
		{"binary content", &WorkspaceFileError{Code: codeWorkspaceFileNotText, Details: map[string]any{"path": "bin"}}, codeWorkspaceFileNotText},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store.err = testCase.err
			response := decodeResponse(t, f.post(t, "/api/workspaceFiles/read",
				rpcBody(t, "s1", "workspaceFiles/read", workspaceFileArgs(scope, `"path":"f"`))))
			if response.Result.OK || response.Result.Error.Code != testCase.code {
				t.Fatalf("code = %q, want %q", response.Result.Error.Code, testCase.code)
			}
		})
	}
	// An unclassified error is internal and, unlike a filesystem error, names no
	// path: the caller never asked about the host's own file layout.
	store.err = errors.New("stat /Users/operator/secret/place: permission denied")
	response := decodeResponse(t, f.post(t, "/api/workspaceFiles/stat",
		rpcBody(t, "s1", "workspaceFiles/stat", workspaceFileArgs(scope, `"path":"f"`))))
	if response.Result.Error.Code != codeInternal {
		t.Fatalf("code = %q, want %q", response.Result.Error.Code, codeInternal)
	}
	if strings.Contains(response.Result.Error.Message, "secret") {
		t.Fatalf("message = %q, want the host path kept out of it", response.Result.Error.Message)
	}
}

// A session the console created but has not prompted is an ordinary scope: the
// sidebar is opened on a new session before its first turn.
func TestWorkspaceFilesAcceptASessionThatHasNotStarted(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetWorkspaceFiles(&stubWorkspaceFiles{
		listing: WorkspaceDirectoryListing{Path: ".", Entries: []WorkspaceDirectoryEntry{}},
	})
	scope := f.createSession(t)
	var listing WorkspaceDirectoryListing
	decodeValue(t, f.post(t, "/api/workspaceFiles/list",
		rpcBody(t, "s1", "workspaceFiles/list", workspaceFileArgs(scope, `"path":"."`))), &listing)
	if listing.Path != "." {
		t.Fatalf("listing = %+v, want the created session accepted as a scope", listing)
	}
}

func TestWorkspaceFilesRefuseAnUnknownScope(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetWorkspaceFiles(&stubWorkspaceFiles{})
	// A session that exists but whose first turn has not started is still a
	// session the console can scope a request to, so it is not the case under
	// test here; this one names no session at all.
	for _, method := range []string{"workspaceFiles/list", "workspaceFiles/read", "workspaceFiles/stat"} {
		response := decodeResponse(t, f.post(t, "/api/"+method,
			rpcBody(t, "s1", method, `{"workspaceFileScopeId":"run_nobody","path":"."}`)))
		if response.Result.OK || response.Result.Error.Code != codeSessionNotFound {
			t.Fatalf("%s: code = %q, want %q", method, response.Result.Error.Code, codeSessionNotFound)
		}
	}
}

func TestWorkspaceFileSurfacesWithoutAFaceAnswerUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	scope := f.startSession(t)
	for _, method := range []string{"workspaceFiles/list", "workspaceFiles/stat", "workspaceFiles/read", "workspaceFiles/readAll", "workspaceFiles/readBytes"} {
		response := decodeResponse(t, f.post(t, "/api/"+method,
			rpcBody(t, "s1", method, workspaceFileArgs(scope, `"path":"."`))))
		if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want unimplemented", method, response.Result.Error.Code)
		}
		if response.Result.Error.Details["dependency"] != "WorkspaceFiles" {
			t.Fatalf("%s: details = %v, want the dependency named", method, response.Result.Error.Details)
		}
	}
}

func TestWorkspaceFileWatchAndRelationReadsAreNamedGaps(t *testing.T) {
	f, scope, _ := workspaceFileFixture(t)
	cases := map[string]string{
		"workspaceFiles/changes":     "workspace file watching",
		"workspaceFiles/readRelated": "workspace file relation reads",
	}
	for method, capability := range cases {
		response := decodeResponse(t, f.post(t, "/api/"+method,
			rpcBody(t, "s1", method, workspaceFileArgs(scope, `"path":"f","relativePath":"../x"`))))
		if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want unimplemented", method, response.Result.Error.Code)
		}
		if response.Result.Error.Details["capability"] != capability {
			t.Fatalf("%s: details = %v, want the capability named", method, response.Result.Error.Details)
		}
	}
}

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshstream"
)

// updateLog records everything a subscriber received, so a test can assert
// both the mutation and the frame the console would see.
type updateLog struct {
	mu      sync.Mutex
	updates []dshstream.WorkspaceUpdate
}

func (l *updateLog) observe(update dshstream.WorkspaceUpdate) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updates = append(l.updates, update)
}

func (l *updateLog) take() []dshstream.WorkspaceUpdate {
	l.mu.Lock()
	defer l.mu.Unlock()
	taken := l.updates
	l.updates = nil
	return taken
}

// newTestWorkspaces roots a registry in a fresh directory. t.TempDir is a real
// path on this filesystem, which matters: the registry canonicalizes paths, so
// a fake path would not exercise the same code as the serve command.
func newTestWorkspaces(t *testing.T) (*consoleWorkspaces, string) {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	workspaces, err := newConsoleWorkspaces(root)
	if err != nil {
		t.Fatalf("newConsoleWorkspaces: %v", err)
	}
	return workspaces, resolved
}

func workspaceErrorCode(t *testing.T, err error) string {
	t.Helper()
	var refusal *dshstream.WorkspaceError
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a WorkspaceError", err)
	}
	return refusal.Code
}

func TestConsoleWorkspacesRegistersTheHostWorkspace(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	if workspaces.Root() != root {
		t.Fatalf("Root = %q, want %q", workspaces.Root(), root)
	}
	baseline := workspaces.Baseline()
	if len(baseline.Items) != 1 {
		t.Fatalf("baseline has %d rows, want the host workspace", len(baseline.Items))
	}
	row := baseline.Items[0]
	if row.Path != root {
		t.Fatalf("row path = %q, want %q", row.Path, root)
	}
	if row.Title != filepath.Base(root) {
		t.Fatalf("row title = %q, want %q", row.Title, filepath.Base(root))
	}
	if len(row.SessionIDs) != 0 {
		t.Fatalf("row sessionIds = %v, want none", row.SessionIDs)
	}
	if baseline.ArchivedSessionIDs == nil {
		t.Fatal("archivedSessionIds is nil, want an empty list on the wire")
	}
	// Registering the same directory again reports the existing row instead of
	// adding a second one, which is what the picker's `created` flag is for.
	again, created, err := workspaces.Create(root)
	if err != nil {
		t.Fatalf("Create(same): %v", err)
	}
	if created {
		t.Fatal("Create(same) reported a new row")
	}
	if again.WorkspaceID != row.WorkspaceID {
		t.Fatalf("Create(same) id = %q, want %q", again.WorkspaceID, row.WorkspaceID)
	}
}

func TestConsoleWorkspacesCanonicalizesPaths(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	// A spelling with a redundant segment and a trailing separator is the same
	// directory, so it must not become a second row.
	spelling := filepath.Join(root, ".", "..", filepath.Base(root)) + string(filepath.Separator)
	row, created, err := workspaces.Create(spelling)
	if err != nil {
		t.Fatalf("Create(spelling): %v", err)
	}
	if created {
		t.Fatalf("Create(%q) added a second row for %q", spelling, root)
	}
	if row.Path != root {
		t.Fatalf("row path = %q, want %q", row.Path, root)
	}
}

func TestConsoleWorkspacesRejectsUnusableDirectories(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing"), "   "} {
		if _, _, err := workspaces.Create(path); err == nil {
			t.Fatalf("Create(%q) succeeded, want a refusal", path)
		} else if code := workspaceErrorCode(t, err); code != dshstream.WorkspaceCodeInvalidPath {
			t.Fatalf("Create(%q) code = %q, want %q", path, code, dshstream.WorkspaceCodeInvalidPath)
		}
	}
}

func TestConsoleWorkspacesKeepsTitlesDistinct(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	first := filepath.Join(root, "a", "api")
	second := filepath.Join(root, "b", "api")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	firstRow, created, err := workspaces.Create(first)
	if err != nil || !created {
		t.Fatalf("Create(first) = %v, created=%v", err, created)
	}
	secondRow, created, err := workspaces.Create(second)
	if err != nil || !created {
		t.Fatalf("Create(second) = %v, created=%v", err, created)
	}
	if firstRow.Title != "api" {
		t.Fatalf("first title = %q, want api", firstRow.Title)
	}
	if secondRow.Title != "api (2)" {
		t.Fatalf("second title = %q, want api (2)", secondRow.Title)
	}
}

func TestConsoleWorkspacesRenameRefusesDuplicateTitle(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	row, _, err := workspaces.Create(other)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	log := &updateLog{}
	unsubscribe := workspaces.Subscribe(log.observe)
	defer unsubscribe()

	renamed, err := workspaces.Rename(row.WorkspaceID, "renamed")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Title != "renamed" {
		t.Fatalf("title = %q, want renamed", renamed.Title)
	}
	if _, err := workspaces.Rename(row.WorkspaceID, filepath.Base(root)); err == nil {
		t.Fatal("Rename to the host workspace's title succeeded")
	} else if code := workspaceErrorCode(t, err); code != dshstream.WorkspaceCodeNameConflict {
		t.Fatalf("code = %q, want %q", code, dshstream.WorkspaceCodeNameConflict)
	}
	if _, err := workspaces.Rename("ws-unknown", "anything"); err == nil {
		t.Fatal("Rename of an unknown workspace succeeded")
	} else if code := workspaceErrorCode(t, err); code != dshstream.WorkspaceCodeNotFound {
		t.Fatalf("code = %q, want %q", code, dshstream.WorkspaceCodeNotFound)
	}
	updates := log.take()
	if len(updates) != 1 || updates[0].Kind != "upsert" || updates[0].Workspace == nil {
		t.Fatalf("updates = %+v, want one upsert", updates)
	}
	if updates[0].Workspace.Title != "renamed" {
		t.Fatalf("update title = %q, want renamed", updates[0].Workspace.Title)
	}
}

func TestConsoleWorkspacesDeleteRefusesTheHostWorkspace(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	hostRow := workspaces.Baseline().Items[0]
	if err := workspaces.Delete(hostRow.WorkspaceID); err == nil {
		t.Fatal("deleting the host workspace succeeded")
	} else if code := workspaceErrorCode(t, err); code != dshstream.WorkspaceCodeRootImmutable {
		t.Fatalf("code = %q, want %q", code, dshstream.WorkspaceCodeRootImmutable)
	}
	other := filepath.Join(root, "scratch")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	row, _, err := workspaces.Create(other)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	log := &updateLog{}
	unsubscribe := workspaces.Subscribe(log.observe)
	defer unsubscribe()
	if err := workspaces.Delete(row.WorkspaceID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := workspaces.Workspace(row.WorkspaceID); ok {
		t.Fatal("the deleted row is still registered")
	}
	if updates := log.take(); len(updates) != 1 || updates[0].Kind != "remove" || updates[0].WorkspaceID != row.WorkspaceID {
		t.Fatalf("updates = %+v, want one remove of %s", updates, row.WorkspaceID)
	}
	if err := workspaces.Delete(row.WorkspaceID); err == nil {
		t.Fatal("deleting twice succeeded")
	} else if code := workspaceErrorCode(t, err); code != dshstream.WorkspaceCodeNotFound {
		t.Fatalf("code = %q, want %q", code, dshstream.WorkspaceCodeNotFound)
	}
	// The host workspace is still there, so the registry still has a root.
	if workspaces.Baseline().Items[0].WorkspaceID != hostRow.WorkspaceID {
		t.Fatal("the host workspace is not the first row after a delete")
	}
}

func TestConsoleWorkspacesArchiveRoundTrip(t *testing.T) {
	workspaces, _ := newTestWorkspaces(t)
	log := &updateLog{}
	unsubscribe := workspaces.Subscribe(log.observe)
	defer unsubscribe()

	archived, err := workspaces.ArchiveSession("run-1")
	if err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if len(archived) != 1 || archived[0] != "run-1" {
		t.Fatalf("archived = %v, want [run-1]", archived)
	}
	// Archiving twice is not a second change: the console would otherwise
	// rebuild the same set for nothing.
	if _, err := workspaces.ArchiveSession("run-1"); err != nil {
		t.Fatalf("ArchiveSession twice: %v", err)
	}
	if updates := log.take(); len(updates) != 1 || updates[0].Kind != "archived" {
		t.Fatalf("updates = %+v, want one archived frame", updates)
	}
	remaining, err := workspaces.UnarchiveSession("run-1")
	if err != nil {
		t.Fatalf("UnarchiveSession: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("archived = %v, want none", remaining)
	}
	if updates := log.take(); len(updates) != 1 || updates[0].Kind != "archived" {
		t.Fatalf("updates = %+v, want one archived frame", updates)
	}
	// Unarchiving something that is not archived changes nothing.
	if _, err := workspaces.UnarchiveSession("run-1"); err != nil {
		t.Fatalf("UnarchiveSession twice: %v", err)
	}
	if updates := log.take(); len(updates) != 0 {
		t.Fatalf("updates = %+v, want none", updates)
	}
}

func TestConsoleWorkspacesAttachSession(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	hostRow := workspaces.Baseline().Items[0]
	log := &updateLog{}
	unsubscribe := workspaces.Subscribe(log.observe)
	defer unsubscribe()

	// An empty workspace id means the host's own workspace: a session created
	// without naming one still has to be visible in the list.
	if err := workspaces.AttachSession("", "run-1"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if err := workspaces.AttachSession(hostRow.WorkspaceID, "run-1"); err != nil {
		t.Fatalf("AttachSession twice: %v", err)
	}
	row, ok := workspaces.Workspace(hostRow.WorkspaceID)
	if !ok {
		t.Fatal("the host workspace vanished")
	}
	if len(row.SessionIDs) != 1 || row.SessionIDs[0] != "run-1" {
		t.Fatalf("sessionIds = %v, want [run-1] once", row.SessionIDs)
	}
	if updates := log.take(); len(updates) != 1 {
		t.Fatalf("updates = %+v, want one upsert for the first attach", updates)
	}
	if err := workspaces.AttachSession("ws-unknown", "run-2"); err == nil {
		t.Fatal("AttachSession to an unknown workspace succeeded")
	} else if code := workspaceErrorCode(t, err); code != dshstream.WorkspaceCodeNotFound {
		t.Fatalf("code = %q, want %q", code, dshstream.WorkspaceCodeNotFound)
	}
	if !strings.HasPrefix(hostRow.Path, root) {
		t.Fatalf("host row path = %q, want it under %q", hostRow.Path, root)
	}
}

func TestConsoleWorkspacesSubscribeStops(t *testing.T) {
	workspaces, root := newTestWorkspaces(t)
	log := &updateLog{}
	unsubscribe := workspaces.Subscribe(log.observe)
	other := filepath.Join(root, "later")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, _, err := workspaces.Create(other); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if updates := log.take(); len(updates) != 1 {
		t.Fatalf("updates = %+v, want one upsert", updates)
	}
	unsubscribe()
	second := filepath.Join(root, "after")
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, _, err := workspaces.Create(second); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if updates := log.take(); len(updates) != 0 {
		t.Fatalf("updates after unsubscribe = %+v, want none", updates)
	}
}

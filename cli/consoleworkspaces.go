package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/internal/dshstream"
)

// consoleWorkspaces is the console's workspace registry: one row per directory
// an operator has added, in the order they were added, the sessions accounted
// to each, and the archived session set. The unary workspace namespace and the
// workspace/follow stream are both views of this one table, so a row the
// console lists is the row a session is grouped under.
//
// The registry is process-local console state, exactly like the console's
// settings (ADR 0094): the repository has no durable home for a console
// preference, and a restart begins from the host's own workspace rather than
// from a file this host would have to invent. ADR 0101 records the decision and
// its limit.
//
// The host's own workspace -- the directory every session of this process runs
// in -- is registered at construction and cannot be deleted: removing it would
// hide the directory the sessions still open in.
type consoleWorkspaces struct {
	mu sync.Mutex

	root     string
	order    []string
	rows     map[string]*consoleWorkspaceRow
	byPath   map[string]string
	archived []string

	// subscribers receive every change after it is applied. They are called
	// with mu held, so a subscriber must neither block nor re-enter the
	// registry: the stream subscriber appends to its own queue and signals.
	subscribers map[int]func(dshstream.WorkspaceUpdate)
	nextSubID   int
}

// consoleWorkspaceRow is one registered directory.
type consoleWorkspaceRow struct {
	id         string
	path       string
	title      string
	sessionIDs []string
	createdAt  time.Time
	updatedAt  time.Time
}

// newConsoleWorkspaces builds the registry around the directory the host was
// started with. A workspace that cannot be opened is an error the serve command
// logs and degrades from (the namespace then answers unimplemented), because a
// registry whose own root is unusable could not honestly answer anything.
func newConsoleWorkspaces(root string) (*consoleWorkspaces, error) {
	canonical, err := canonicalWorkspacePath(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", canonical)
	}
	workspaces := &consoleWorkspaces{
		root:        canonical,
		rows:        map[string]*consoleWorkspaceRow{},
		byPath:      map[string]string{},
		subscribers: map[int]func(dshstream.WorkspaceUpdate){},
	}
	workspaces.registerLocked(canonical, time.Now().UTC())
	return workspaces, nil
}

// canonicalWorkspacePath makes two spellings of one directory compare equal:
// /tmp and /private/tmp are the same row, and so are a path with a trailing
// separator and one without. It resolves symlinks, so a registration through a
// link lands on the directory the link names -- the same directory the picker
// lists once it walks through it.
func canonicalWorkspacePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("the path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// workspaceIDForPath derives a stable id from the directory itself, so the same
// registration keeps one id across a restart of this process even though the
// table itself is process-local. The console treats the id as opaque.
func workspaceIDForPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "ws-" + hex.EncodeToString(sum[:])[:12]
}

// Create registers a directory, or reports the row that already holds it. The
// console's directory picker calls this the moment an operator confirms a
// directory, so every refusal has to be one the "could not open folder" dialog
// can show.
func (w *consoleWorkspaces) Create(path string) (dshstream.WorkspaceView, bool, error) {
	canonical, err := canonicalWorkspacePath(path)
	if err != nil {
		return dshstream.WorkspaceView{}, false, invalidWorkspacePath(path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return dshstream.WorkspaceView{}, false, invalidWorkspacePath(path, err)
	}
	if !info.IsDir() {
		return dshstream.WorkspaceView{}, false, invalidWorkspacePath(path, fmt.Errorf("it is not a directory"))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if id, ok := w.byPath[canonical]; ok {
		// Registered already: report the existing row and say so, exactly like
		// upstream's `created` flag, instead of adding a second row for one
		// directory.
		return w.viewLocked(w.rows[id]), false, nil
	}
	row := w.registerLocked(canonical, time.Now().UTC())
	view := w.viewLocked(row)
	w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "upsert", Workspace: &view})
	return view, true, nil
}

// Rename replaces one row's title. Two rows cannot share a title: the console
// renders a row by its title, and two identical rows would be one row an
// operator cannot tell apart.
func (w *consoleWorkspaces) Rename(workspaceID, title string) (dshstream.WorkspaceView, error) {
	title = strings.TrimSpace(title)
	w.mu.Lock()
	defer w.mu.Unlock()
	row, ok := w.rows[workspaceID]
	if !ok {
		return dshstream.WorkspaceView{}, workspaceNotFound(workspaceID)
	}
	for _, other := range w.rows {
		if other.id != row.id && other.title == title {
			return dshstream.WorkspaceView{}, &dshstream.WorkspaceError{
				Code:    dshstream.WorkspaceCodeNameConflict,
				Message: fmt.Sprintf("another workspace is already called %q", title),
				Details: map[string]any{"name": title},
			}
		}
	}
	row.title = title
	row.updatedAt = time.Now().UTC()
	view := w.viewLocked(row)
	w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "upsert", Workspace: &view})
	return view, nil
}

// Delete removes one registration. The host's own workspace is refused by name:
// this process keeps running sessions there, so a removed row would be a row the
// console cannot show while the sessions in it stay alive.
func (w *consoleWorkspaces) Delete(workspaceID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	row, ok := w.rows[workspaceID]
	if !ok {
		return workspaceNotFound(workspaceID)
	}
	if row.path == w.root {
		return &dshstream.WorkspaceError{
			Code: dshstream.WorkspaceCodeRootImmutable,
			Message: fmt.Sprintf("%s is the directory this host runs in, so its workspace cannot be removed; start the host with --workspace elsewhere to drop it",
				row.path),
			Details: map[string]any{"workspaceId": row.id, "path": row.path},
		}
	}
	delete(w.rows, workspaceID)
	delete(w.byPath, row.path)
	for i, id := range w.order {
		if id == workspaceID {
			w.order = append(w.order[:i], w.order[i+1:]...)
			break
		}
	}
	w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "remove", WorkspaceID: workspaceID})
	return nil
}

// ArchiveSession adds a session to the archived set, and UnarchiveSession
// removes it. Both return the whole set, which is what the protocol's value
// carries: the console replaces its set instead of applying a delta, so the
// answer cannot drift from the host.
func (w *consoleWorkspaces) ArchiveSession(sessionID string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !hasString(w.archived, sessionID) {
		w.archived = append(w.archived, sessionID)
		w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "archived", ArchivedSessionIDs: w.archivedLocked()})
	}
	return w.archivedLocked(), nil
}

func (w *consoleWorkspaces) UnarchiveSession(sessionID string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, id := range w.archived {
		if id == sessionID {
			w.archived = append(w.archived[:i], w.archived[i+1:]...)
			w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "archived", ArchivedSessionIDs: w.archivedLocked()})
			break
		}
	}
	return w.archivedLocked(), nil
}

// AttachSession accounts a session to a workspace. An empty workspace id means
// the host's own workspace, which is where a session created without naming one
// runs. Re-attaching the same session is a no-op, so adopting a session twice
// does not duplicate its row.
func (w *consoleWorkspaces) AttachSession(workspaceID, sessionID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := workspaceID
	if strings.TrimSpace(id) == "" {
		id = w.byPath[w.root]
	}
	row, ok := w.rows[id]
	if !ok {
		return workspaceNotFound(id)
	}
	if hasString(row.sessionIDs, sessionID) {
		return nil
	}
	row.sessionIDs = append(row.sessionIDs, sessionID)
	row.updatedAt = time.Now().UTC()
	view := w.viewLocked(row)
	w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "upsert", Workspace: &view})
	return nil
}

// InsertBefore moves one registration within the display order and returns the
// complete resulting order, which is what the protocol's value carries: the
// console replaces its order instead of applying a delta, so the answer cannot
// drift from the host. An empty anchor appends, an anchor naming the moved row
// is a no-op, and either id being unknown is the namespace's not-found -- the
// reference's own mapping for a reorder it cannot perform
// (api/workspace-controller/lib/index.js:247-253).
func (w *consoleWorkspaces) InsertBefore(workspaceID, beforeWorkspaceID string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.rows[workspaceID]; !ok {
		return nil, workspaceNotFound(workspaceID)
	}
	if beforeWorkspaceID != "" {
		if _, ok := w.rows[beforeWorkspaceID]; !ok {
			return nil, workspaceNotFound(beforeWorkspaceID)
		}
	}
	if beforeWorkspaceID == workspaceID {
		return append([]string(nil), w.order...), nil
	}
	w.order = movedBefore(w.order, workspaceID, beforeWorkspaceID)
	w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "order", WorkspaceIDs: append([]string(nil), w.order...)})
	return append([]string(nil), w.order...), nil
}

// InsertSessionBefore moves one accounted session within a workspace's manual
// order and returns the updated row. The console's session list renders the
// row's sessionIds in order, so this is what drag-to-reorder inside a workspace
// calls. A session or anchor that is not accounted to that workspace is
// upstream's move-invalid, with its sentence and details; an empty anchor
// appends.
func (w *consoleWorkspaces) InsertSessionBefore(workspaceID, sessionID, beforeSessionID string) (dshstream.WorkspaceView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	row, ok := w.rows[workspaceID]
	if !ok {
		return dshstream.WorkspaceView{}, workspaceNotFound(workspaceID)
	}
	if !hasString(row.sessionIDs, sessionID) {
		return dshstream.WorkspaceView{}, sessionMoveInvalid(row.path, workspaceID, sessionID, beforeSessionID,
			fmt.Sprintf("cannot move session %q in workspace %q: the session is not accounted", sessionID, row.path))
	}
	if beforeSessionID != "" && !hasString(row.sessionIDs, beforeSessionID) {
		return dshstream.WorkspaceView{}, sessionMoveInvalid(row.path, workspaceID, sessionID, beforeSessionID,
			fmt.Sprintf("cannot move session %q before %q in workspace %q: the anchor session is not accounted",
				sessionID, beforeSessionID, row.path))
	}
	if beforeSessionID == sessionID {
		return w.viewLocked(row), nil
	}
	row.sessionIDs = movedBefore(row.sessionIDs, sessionID, beforeSessionID)
	row.updatedAt = time.Now().UTC()
	view := w.viewLocked(row)
	w.notifyLocked(dshstream.WorkspaceUpdate{Kind: "upsert", Workspace: &view})
	return view, nil
}

// movedBefore splices one id immediately before another, appending when no
// anchor is named. It is the reference's own move
// (dsh-workspace/lib/index.js:409-428): the id leaves the list first, so an
// anchor that sat after it keeps its meaning, and an id already in place
// produces the same list.
func movedBefore(ids []string, id, beforeID string) []string {
	without := make([]string, 0, len(ids))
	for _, current := range ids {
		if current != id {
			without = append(without, current)
		}
	}
	at := len(without)
	if beforeID != "" {
		for index, current := range without {
			if current == beforeID {
				at = index
				break
			}
		}
	}
	moved := make([]string, 0, len(ids))
	moved = append(moved, without[:at]...)
	moved = append(moved, id)
	moved = append(moved, without[at:]...)
	return moved
}

// sessionMoveInvalid builds the refusal for a session move the workspace cannot
// perform, carrying the details upstream publishes for the code.
func sessionMoveInvalid(path, workspaceID, sessionID, beforeSessionID, message string) error {
	details := map[string]any{"workspaceId": workspaceID, "sessionId": sessionID}
	if beforeSessionID != "" {
		details["beforeSessionId"] = beforeSessionID
	}
	return &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeMoveInvalid,
		Message: message,
		Details: details,
	}
}

// Workspace looks one row up by id.
func (w *consoleWorkspaces) Workspace(workspaceID string) (dshstream.WorkspaceView, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	row, ok := w.rows[workspaceID]
	if !ok {
		return dshstream.WorkspaceView{}, false
	}
	return w.viewLocked(row), true
}

// Root is the canonical directory every session of this host runs in.
func (w *consoleWorkspaces) Root() string { return w.root }

// Baseline is the whole table, in registration order. The follow stream sends
// it as its first frame.
func (w *consoleWorkspaces) Baseline() dshstream.WorkspaceBaseline {
	w.mu.Lock()
	defer w.mu.Unlock()
	items := make([]dshstream.WorkspaceView, 0, len(w.order))
	for _, id := range w.order {
		items = append(items, w.viewLocked(w.rows[id]))
	}
	return dshstream.WorkspaceBaseline{Items: items, ArchivedSessionIDs: w.archivedLocked()}
}

// Subscribe delivers every later change until the returned function is called.
func (w *consoleWorkspaces) Subscribe(observe func(dshstream.WorkspaceUpdate)) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextSubID++
	id := w.nextSubID
	w.subscribers[id] = observe
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.subscribers, id)
	}
}

// registerLocked adds a row for a canonical directory and returns it. The
// caller holds mu.
func (w *consoleWorkspaces) registerLocked(path string, now time.Time) *consoleWorkspaceRow {
	row := &consoleWorkspaceRow{
		id:        workspaceIDForPath(path),
		path:      path,
		title:     w.uniqueTitleLocked(workspaceTitle(path)),
		createdAt: now,
		updatedAt: now,
	}
	w.rows[row.id] = row
	w.byPath[path] = row.id
	w.order = append(w.order, row.id)
	return row
}

// uniqueTitleLocked keeps titles distinct by suffixing the directory's own name
// rather than refusing a second directory that happens to share it: two
// checkouts called "api" are both legitimate rows.
func (w *consoleWorkspaces) uniqueTitleLocked(base string) string {
	taken := func(candidate string) bool {
		for _, row := range w.rows {
			if row.title == candidate {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)", base, n)
		if !taken(candidate) {
			return candidate
		}
	}
}

// workspaceTitle names a row after the directory it holds, which is what the
// console shows until an operator renames it. The filesystem root has no base
// name of its own, so it keeps its path.
func workspaceTitle(path string) string {
	base := filepath.Base(path)
	if base == string(filepath.Separator) || base == "." || base == "" {
		return path
	}
	return base
}

// viewLocked renders one row as the wire value. The caller holds mu.
func (w *consoleWorkspaces) viewLocked(row *consoleWorkspaceRow) dshstream.WorkspaceView {
	sessions := row.sessionIDs
	if sessions == nil {
		sessions = []string{}
	}
	return dshstream.WorkspaceView{
		WorkspaceID: row.id,
		Path:        row.path,
		Title:       row.title,
		SessionIDs:  append([]string{}, sessions...),
		CreatedAt:   row.createdAt.Format(time.RFC3339),
		UpdatedAt:   row.updatedAt.Format(time.RFC3339),
	}
}

// archivedLocked returns the archived set in a stable order, so the same set is
// the same value on the wire however it was assembled.
func (w *consoleWorkspaces) archivedLocked() []string {
	ids := append([]string{}, w.archived...)
	sort.Strings(ids)
	return ids
}

// notifyLocked hands one change to every subscriber. The caller holds mu; see
// the field's contract.
func (w *consoleWorkspaces) notifyLocked(update dshstream.WorkspaceUpdate) {
	for _, observe := range w.subscribers {
		observe(update)
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func workspaceNotFound(workspaceID string) error {
	return &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeNotFound,
		Message: fmt.Sprintf("workspace %q is not registered on this host", workspaceID),
		Details: map[string]any{"workspaceId": workspaceID},
	}
}

func invalidWorkspacePath(path string, reason error) error {
	return &dshstream.WorkspaceError{
		Code:    dshstream.WorkspaceCodeInvalidPath,
		Message: fmt.Sprintf("%s cannot back a workspace: %v", path, reason),
		Details: map[string]any{"path": path},
	}
}

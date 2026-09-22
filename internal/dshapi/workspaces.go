package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/feiyu912/zenforge/internal/dshstream"
)

// Error codes this namespace adds. They mirror the detail codes upstream's
// workspace controller publishes (api/workspace-controller/lib/types/types.d.ts
// RemoteErrorDetailsMap: workspace/invalid-path, workspace/name-conflict), so
// the console maps them to the same message it shows against upstream's host.
const (
	codeWorkspaceInvalidPath = dshstream.WorkspaceCodeInvalidPath
	codeWorkspaceNameClash   = dshstream.WorkspaceCodeNameConflict
	codeWorkspaceNotFound    = dshstream.WorkspaceCodeNotFound
	// codeWorkspaceRootImmutable is this host's own addition. The host runs
	// every session in the directory it was started with, so that one
	// registration cannot be removed: deleting the row would hide the directory
	// the sessions still open in.
	codeWorkspaceRootImmutable = dshstream.WorkspaceCodeRootImmutable
)

// WorkspaceRegistry is the console's workspace registry: the directories an
// operator has added, their titles, and which sessions are accounted to each.
// The serve command owns the implementation (the registry is process-local
// console state, ADR 0094), the unary namespace drives it, and the
// workspace/follow stream publishes its changes.
//
// Implementations return *dshstream.WorkspaceError for the refusals this
// namespace must report by name; any other error is reported as an internal
// failure.
type WorkspaceRegistry interface {
	// Create registers a directory, returning the row and whether this call
	// created it (false means the directory was already registered).
	Create(path string) (dshstream.WorkspaceView, bool, error)
	// Rename replaces one row's title.
	Rename(workspaceID, title string) (dshstream.WorkspaceView, error)
	// Delete removes one registration.
	Delete(workspaceID string) error
	// ArchiveSession and UnarchiveSession move a session in and out of the
	// archived set and return the whole set.
	ArchiveSession(sessionID string) ([]string, error)
	UnarchiveSession(sessionID string) ([]string, error)
	// Workspace looks one row up. The session namespace needs it to tell
	// whether a requested workspace is the directory this host actually runs
	// in.
	Workspace(workspaceID string) (dshstream.WorkspaceView, bool)
	// Root is the canonical directory every session of this host runs in.
	Root() string
	// AttachSession accounts a session to a workspace. The session namespace
	// calls it for every session it creates, so a session the console opens is
	// visible under the workspace that was selected for it.
	AttachSession(workspaceID, sessionID string) error
	// InsertBefore moves one registration within the display order and returns
	// the complete resulting order. The console's drag-to-reorder on the
	// workspace list calls it with an anchor, and with none to append.
	InsertBefore(workspaceID, beforeWorkspaceID string) ([]string, error)
	// InsertSessionBefore moves one accounted session within a workspace's
	// manual order and returns the updated row, which is what the console
	// renders after a session row is dragged.
	InsertSessionBefore(workspaceID, sessionID, beforeSessionID string) (dshstream.WorkspaceView, error)
}

// SetWorkspaces installs the workspace registry after New, like the other
// injected faces: the registry lives in the serve command.
func (h *Handler) SetWorkspaces(workspaces WorkspaceRegistry) {
	h.workspacesMu.Lock()
	h.workspaces = workspaces
	h.workspacesMu.Unlock()
}

func (h *Handler) workspaceRegistry() WorkspaceRegistry {
	h.workspacesMu.RLock()
	defer h.workspacesMu.RUnlock()
	return h.workspaces
}

// workspaceRPC turns one operation's refusal into the method error the console
// expects. A registry is optional: a host built without one answers every
// method in this namespace with unimplemented, naming the missing dependency
// instead of pretending an empty registry.
func workspaceRPC(workspaces WorkspaceRegistry) *methodError {
	if workspaces == nil {
		return fail(codeUnimplemented,
			"the console workspace registry is not configured on this host",
			map[string]any{"capability": "a workspace registry"})
	}
	return nil
}

// workspaceFailure maps a registry error onto the wire: a refusal the registry
// named (invalid path, duplicate title, unknown row, the immutable root) keeps
// its code and details, and anything else is an internal failure.
func workspaceFailure(err error) *methodError {
	var refusal *dshstream.WorkspaceError
	if errors.As(err, &refusal) {
		return fail(refusal.Code, refusal.Message, refusal.Details)
	}
	return fail(codeInternal, err.Error(), nil)
}

// workspaceCreate answers POST /api/workspace/create: adopt one host directory
// as a workspace. The console's directory picker confirms a directory and calls
// exactly this method, so a refusal here is what the "could not open folder"
// dialog shows.
func (h *Handler) workspaceCreate(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return nil, failure
	}
	if failure := rejectUnknownArguments(args, "path"); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(path) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/create needs a path", map[string]any{"argument": "path"})
	}
	view, created, err := workspaces.Create(path)
	if err != nil {
		return nil, workspaceFailure(err)
	}
	return map[string]any{"workspace": view, "created": created}, nil
}

// workspaceRename answers POST /api/workspace/rename.
func (h *Handler) workspaceRename(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return nil, failure
	}
	if failure := rejectUnknownArguments(args, "workspaceId", "title"); failure != nil {
		return nil, failure
	}
	workspaceID, _, failure := stringArg(args, "workspaceId")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/rename needs a workspaceId", map[string]any{"argument": "workspaceId"})
	}
	title, _, failure := stringArg(args, "title")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(title) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/rename needs a non-blank title", map[string]any{"argument": "title"})
	}
	view, err := workspaces.Rename(workspaceID, title)
	if err != nil {
		return nil, workspaceFailure(err)
	}
	return map[string]any{"workspace": view}, nil
}

// workspaceDelete answers POST /api/workspace/delete.
func (h *Handler) workspaceDelete(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return nil, failure
	}
	if failure := rejectUnknownArguments(args, "workspaceId"); failure != nil {
		return nil, failure
	}
	workspaceID, _, failure := stringArg(args, "workspaceId")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/delete needs a workspaceId", map[string]any{"argument": "workspaceId"})
	}
	if err := workspaces.Delete(workspaceID); err != nil {
		return nil, workspaceFailure(err)
	}
	return map[string]any{"deleted": true}, nil
}

// workspaceInsertBefore answers POST /api/workspace/insertBefore: move one
// registration within the display order. The answer is the complete order, not
// a delta, which is what the protocol's value carries -- the console replaces
// its list rather than applying an edit it would have to merge.
func (h *Handler) workspaceInsertBefore(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return nil, failure
	}
	if failure := rejectUnknownArguments(args, "workspaceId", "beforeWorkspaceId"); failure != nil {
		return nil, failure
	}
	workspaceID, _, failure := stringArg(args, "workspaceId")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/insertBefore needs a workspaceId", map[string]any{"argument": "workspaceId"})
	}
	// An absent anchor appends, which is the reference's own reading of the
	// optional field; a present but non-string one is a malformed request.
	beforeWorkspaceID, _, failure := stringArg(args, "beforeWorkspaceId")
	if failure != nil {
		return nil, failure
	}
	order, err := workspaces.InsertBefore(workspaceID, beforeWorkspaceID)
	if err != nil {
		return nil, workspaceFailure(err)
	}
	if order == nil {
		order = []string{}
	}
	return map[string]any{"workspaceIds": order}, nil
}

// workspaceInsertSessionBefore answers POST /api/workspace/insertSessionBefore:
// move one accounted session within a workspace's manual order.
func (h *Handler) workspaceInsertSessionBefore(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return nil, failure
	}
	if failure := rejectUnknownArguments(args, "workspaceId", "sessionId", "beforeSessionId"); failure != nil {
		return nil, failure
	}
	workspaceID, _, failure := stringArg(args, "workspaceId")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/insertSessionBefore needs a workspaceId", map[string]any{"argument": "workspaceId"})
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, fail(codeArgumentsInvalid, "workspace/insertSessionBefore needs a sessionId", map[string]any{"argument": "sessionId"})
	}
	beforeSessionID, _, failure := stringArg(args, "beforeSessionId")
	if failure != nil {
		return nil, failure
	}
	view, err := workspaces.InsertSessionBefore(workspaceID, sessionID, beforeSessionID)
	if err != nil {
		return nil, workspaceFailure(err)
	}
	return map[string]any{"workspace": view}, nil
}

// workspaceArchiveSession answers POST /api/workspace/archiveSession, and
// workspaceUnarchiveSession its inverse. The archived set is grouping state
// only: archiving a session does not hide its durable log or stop a run, which
// is what the console's "show archived" toggle means.
func (h *Handler) workspaceArchiveSession(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.workspaceArchiveCommand(ctx, args, true)
}

func (h *Handler) workspaceUnarchiveSession(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.workspaceArchiveCommand(ctx, args, false)
}

func (h *Handler) workspaceArchiveCommand(ctx context.Context, args map[string]json.RawMessage, archive bool) (any, *methodError) {
	method := "workspace/unarchiveSession"
	if archive {
		method = "workspace/archiveSession"
	}
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return nil, failure
	}
	if failure := rejectUnknownArguments(args, "sessionId"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, fail(codeArgumentsInvalid, method+" needs a sessionId", map[string]any{"argument": "sessionId"})
	}
	// A session this host has never seen would be a phantom row in the archived
	// list, so the same check the workspace file namespace applies decides it.
	if !h.sessionKnown(ctx, sessionID) {
		return nil, fail(codeSessionNotFound,
			fmt.Sprintf("session %q is not a session this host knows", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	var archived []string
	var err error
	if archive {
		archived, err = workspaces.ArchiveSession(sessionID)
	} else {
		archived, err = workspaces.UnarchiveSession(sessionID)
	}
	if err != nil {
		return nil, workspaceFailure(err)
	}
	if archived == nil {
		archived = []string{}
	}
	return map[string]any{"archivedSessionIds": archived}, nil
}

// workspaceViewForSession resolves the workspace a session/create call asked
// for. It returns the row, a nil failure for "no workspace requested", or the
// refusal the console shows.
func (h *Handler) workspaceViewForSession(workspaceID string) (dshstream.WorkspaceView, *methodError) {
	workspaces := h.workspaceRegistry()
	if failure := workspaceRPC(workspaces); failure != nil {
		return dshstream.WorkspaceView{}, failure
	}
	view, ok := workspaces.Workspace(workspaceID)
	if !ok {
		return dshstream.WorkspaceView{}, fail(codeWorkspaceNotFound,
			fmt.Sprintf("workspace %q is not registered on this host", workspaceID),
			map[string]any{"workspaceId": workspaceID})
	}
	// This host runs every session in the directory it was started with
	// (Agent config carries one workspace), so a session can only be grouped
	// under a workspace that points at that directory. Saying so is the honest
	// answer: the alternative is a session the console believes is editing one
	// directory while the tools edit another.
	if !sameDirectory(view.Path, workspaces.Root()) {
		return dshstream.WorkspaceView{}, fail(codeUnimplemented,
			fmt.Sprintf("this host runs every session in %s; a session cannot be opened in %s. Start the host with --workspace %s to work there",
				workspaces.Root(), view.Path, view.Path),
			map[string]any{"field": "workspaceId", "workspacePath": view.Path, "hostWorkspace": workspaces.Root()})
	}
	return view, nil
}

// sameDirectory compares two directory paths after cleaning them, so the same
// directory spelled with a trailing separator or a redundant element still
// matches. It deliberately does not resolve symlinks: the registry canonicalizes
// what it registers, and resolving here would silently accept a link that points
// somewhere the operator did not name.
func sameDirectory(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

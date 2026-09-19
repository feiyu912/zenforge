package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Wire shapes for POST /api/workspaceFiles/*, the namespace the console's file
// sidebar and document preview read: ui-sidebar-files calls list
// (src/client/face.ts:55) and ui-sidebar-documentpreview calls read and readAll
// (src/client/rpc.ts:81, src/client/index.ts:99). Types mirror
// packages/api/workspace-files/lib/types/types.d.ts:1-34 field for field.
//
// The page semantics are upstream's: offset and limit count lines, offset is
// 1-based, a page over the byte cap is refused rather than silently cut, and a
// page that stops early reports eof false (src/index.ts:106-158, 369-380).

// Workspace page and directory limits. These are upstream's own defaults
// (src/index.ts:186-189), kept equal so a console asking for a page gets the
// answer it was written against rather than a host-specific one.
const (
	// WorkspacePageMaxBytes is the inclusive byte cap on one text page.
	WorkspacePageMaxBytes = 2 << 20
	// WorkspaceFileMaxBytes is the inclusive cap on a complete-file read.
	WorkspaceFileMaxBytes = 32 << 20
	// WorkspacePageMaxLines is the default and largest page size in lines.
	WorkspacePageMaxLines = 5000
	// WorkspaceDirMaxEntries caps returned directory entries; the rest is
	// dropped and the listing reports itself truncated.
	WorkspaceDirMaxEntries = 2000
)

// Upstream's workspace-file error codes (types.d.ts:36-58). They are method
// errors, so they arrive with HTTP 200 and the console branches on the code and
// its details rather than on a transport status.
const (
	codeWorkspaceFileNotFound   = "workspace-file/not-found"
	codeWorkspaceFileOutside    = "workspace-file/outside-workspace"
	codeWorkspaceFileTooLarge   = "workspace-file/too-large"
	codeWorkspaceFileNotText    = "workspace-file/not-text"
	codeWorkspaceFileNotRegular = "workspace-file/not-regular-file"
	codeWorkspaceFileNotDir     = "workspace-file/not-directory"
)

// WorkspaceFileStat identifies one file's version. Upstream types.d.ts:1-5.
type WorkspaceFileStat struct {
	// AbsolutePath is the host's absolute path for the file.
	AbsolutePath string `json:"absolutePath"`
	// Version changes when the content does; the console uses it to decide
	// whether a cached page is still current.
	Version string `json:"version"`
	// Bytes is the file size, omitted for directories.
	Bytes *int64 `json:"bytes,omitempty"`
}

// WorkspaceDirectoryEntry is one child of a listed directory. Upstream
// types.d.ts:22-26.
type WorkspaceDirectoryEntry struct {
	Name string `json:"name"`
	// Type is 'file', 'directory' or 'other'.
	Type string `json:"type"`
	Size *int64 `json:"size,omitempty"`
}

// WorkspaceDirectoryListing is the answer to workspaceFiles/list. Upstream
// types.d.ts:28-32.
type WorkspaceDirectoryListing struct {
	Path      string                    `json:"path"`
	Entries   []WorkspaceDirectoryEntry `json:"entries"`
	Truncated bool                      `json:"truncated"`
}

// WorkspaceFileRange is a line range: offset is 1-based and both fields are
// optional. Upstream types.d.ts:7-10.
type WorkspaceFileRange struct {
	Offset *int `json:"offset,omitempty"`
	Limit  *int `json:"limit,omitempty"`
}

// WorkspaceByteRange is a byte window. Upstream types.d.ts:17-20.
type WorkspaceByteRange struct {
	Offset *int `json:"offset,omitempty"`
	Length *int `json:"length,omitempty"`
}

// WorkspaceFileText is one page of a text file. Upstream types.d.ts:12-18.
type WorkspaceFileText struct {
	WorkspaceFileStat
	// Offset is the 1-based line the page starts at.
	Offset int `json:"offset"`
	// Text is the page's lines joined without a trailing newline.
	Text string `json:"text"`
	// Lines is the number of lines in Text; zero for a page past the end.
	Lines int `json:"lines"`
	// EOF reports whether the page reached the last line.
	EOF bool `json:"eof"`
}

// WorkspaceFileBytes is one byte window in base64. Upstream types.d.ts:22-26.
type WorkspaceFileBytes struct {
	WorkspaceFileStat
	Offset int    `json:"offset"`
	Data   string `json:"data"`
	EOF    bool   `json:"eof"`
}

// WorkspaceFileError is a refusal an adapter has already classified with one of
// upstream's codes and details. The adapter classifies because only it can tell a
// missing path from a path outside the root; this package would have to guess
// from an error string.
type WorkspaceFileError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *WorkspaceFileError) Error() string { return e.Message }

// WorkspaceFiles is the injected read-only file face. It is deliberately small:
// each method returns the wire value the console expects, so the file handling
// lives behind one boundary that a test can replace and the handler stays free
// of filesystem policy.
type WorkspaceFiles interface {
	// List returns one directory's children, unsorted as the host gives them.
	List(path string) (WorkspaceDirectoryListing, error)
	// Stat reports one path's version and size.
	Stat(path string) (WorkspaceFileStat, error)
	// ReadPage returns lines offset through offset+limit-1, refusing a page over
	// WorkspacePageMaxBytes rather than shortening it.
	ReadPage(path string, offset, limit int) (WorkspaceFileText, error)
	// ReadAll returns the whole file, refusing anything over
	// WorkspaceFileMaxBytes rather than truncating it.
	ReadAll(path string) (WorkspaceFileText, error)
	// ReadBytes returns one byte window, refusing a window over
	// WorkspacePageMaxBytes.
	ReadBytes(path string, offset, length int) (WorkspaceFileBytes, error)
}

// SetWorkspaceFiles installs the file face the workspaceFiles methods answer from.
func (h *Handler) SetWorkspaceFiles(files WorkspaceFiles) {
	h.workspaceMu.Lock()
	h.workspaceFiles = files
	h.workspaceMu.Unlock()
}

func (h *Handler) workspaceFilesStore() WorkspaceFiles {
	h.workspaceMu.RLock()
	defer h.workspaceMu.RUnlock()
	return h.workspaceFiles
}

// workspaceFileScope reads and validates the session a file request is scoped to.
// The console scopes every read to a session so a host with several workspaces
// can answer per session; this host serves one workspace root, but an unknown
// scope is still refused by name rather than silently answered from the root,
// which would show a file tree the caller never asked for.
func (h *Handler) workspaceFileScope(ctx context.Context, args map[string]json.RawMessage) (string, *methodError) {
	scope, _, failure := stringArg(args, "workspaceFileScopeId")
	if failure != nil {
		return "", failure
	}
	if !h.sessionKnown(ctx, scope) {
		return "", fail(codeSessionNotFound,
			fmt.Sprintf("workspace file scope %q is not a session this host knows", scope),
			map[string]any{"sessionId": scope})
	}
	return scope, nil
}

// workspaceFilesList answers POST /api/workspaceFiles/list.
func (h *Handler) workspaceFilesList(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, failure := h.workspaceFileScope(ctx, args); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	store := h.workspaceFilesStore()
	if store == nil {
		return nil, workspaceDependencyMissing("workspaceFiles/list")
	}
	listing, err := store.List(path)
	if err != nil {
		return nil, workspaceFileFailure(err)
	}
	if listing.Entries == nil {
		listing.Entries = []WorkspaceDirectoryEntry{}
	}
	// The cap is upstream's: the rest is dropped and the listing says so, which
	// is the one case a short answer is not a lie (src/index.ts:349-350).
	if len(listing.Entries) > WorkspaceDirMaxEntries {
		listing.Entries = listing.Entries[:WorkspaceDirMaxEntries]
		listing.Truncated = true
	}
	return listing, nil
}

// workspaceFilesStat answers POST /api/workspaceFiles/stat.
func (h *Handler) workspaceFilesStat(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, failure := h.workspaceFileScope(ctx, args); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	store := h.workspaceFilesStore()
	if store == nil {
		return nil, workspaceDependencyMissing("workspaceFiles/stat")
	}
	stat, err := store.Stat(path)
	if err != nil {
		return nil, workspaceFileFailure(err)
	}
	return stat, nil
}

// workspaceFilesRead answers POST /api/workspaceFiles/read with one page of
// lines.
func (h *Handler) workspaceFilesRead(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, failure := h.workspaceFileScope(ctx, args); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	offset, limit, failure := workspacePageRange(args)
	if failure != nil {
		return nil, failure
	}
	store := h.workspaceFilesStore()
	if store == nil {
		return nil, workspaceDependencyMissing("workspaceFiles/read")
	}
	page, err := store.ReadPage(path, offset, limit)
	if err != nil {
		return nil, workspaceFileFailure(err)
	}
	return page, nil
}

// workspaceFilesReadAll answers POST /api/workspaceFiles/readAll: the whole file,
// refused rather than truncated when it is over the cap.
func (h *Handler) workspaceFilesReadAll(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, failure := h.workspaceFileScope(ctx, args); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	store := h.workspaceFilesStore()
	if store == nil {
		return nil, workspaceDependencyMissing("workspaceFiles/readAll")
	}
	page, err := store.ReadAll(path)
	if err != nil {
		return nil, workspaceFileFailure(err)
	}
	return page, nil
}

// workspaceFilesReadBytes answers POST /api/workspaceFiles/readBytes.
func (h *Handler) workspaceFilesReadBytes(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, failure := h.workspaceFileScope(ctx, args); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	offset, length, failure := workspaceByteRange(args)
	if failure != nil {
		return nil, failure
	}
	store := h.workspaceFilesStore()
	if store == nil {
		return nil, workspaceDependencyMissing("workspaceFiles/readBytes")
	}
	window, err := store.ReadBytes(path, offset, length)
	if err != nil {
		return nil, workspaceFileFailure(err)
	}
	return window, nil
}

// workspacePageRange resolves a text page's line range, applying upstream's
// defaults and refusing a page larger than the cap rather than cutting it
// (src/index.ts:369-380).
func workspacePageRange(args map[string]json.RawMessage) (int, int, *methodError) {
	offset, limit := 1, WorkspacePageMaxLines
	raw, ok := args["range"]
	if !ok {
		return offset, limit, nil
	}
	var requested WorkspaceFileRange
	if err := json.Unmarshal(raw, &requested); err != nil {
		return 0, 0, fail(codeBadRequest, `argument "range" must be an object with optional offset and limit`,
			map[string]any{"argument": "range"})
	}
	if requested.Offset != nil {
		if *requested.Offset < 1 {
			return 0, 0, fail(codeBadRequest, "range.offset must be at least 1: lines are numbered from 1",
				map[string]any{"argument": "range.offset"})
		}
		offset = *requested.Offset
	}
	if requested.Limit != nil {
		if *requested.Limit < 1 {
			return 0, 0, fail(codeBadRequest, "range.limit must be at least 1",
				map[string]any{"argument": "range.limit"})
		}
		if *requested.Limit > WorkspacePageMaxLines {
			return 0, 0, fail(codeBadRequest,
				fmt.Sprintf("range.limit must be at most %d lines", WorkspacePageMaxLines),
				map[string]any{"argument": "range.limit", "limit": WorkspacePageMaxLines})
		}
		limit = *requested.Limit
	}
	return offset, limit, nil
}

// workspaceByteRange resolves a byte window, applying upstream's defaults.
func workspaceByteRange(args map[string]json.RawMessage) (int, int, *methodError) {
	offset, length := 0, WorkspacePageMaxBytes
	raw, ok := args["range"]
	if !ok {
		return offset, length, nil
	}
	var requested WorkspaceByteRange
	if err := json.Unmarshal(raw, &requested); err != nil {
		return 0, 0, fail(codeBadRequest, `argument "range" must be an object with optional offset and length`,
			map[string]any{"argument": "range"})
	}
	if requested.Offset != nil {
		if *requested.Offset < 0 {
			return 0, 0, fail(codeBadRequest, "range.offset must not be negative",
				map[string]any{"argument": "range.offset"})
		}
		offset = *requested.Offset
	}
	if requested.Length != nil {
		if *requested.Length < 1 {
			return 0, 0, fail(codeBadRequest, "range.length must be at least 1",
				map[string]any{"argument": "range.length"})
		}
		if *requested.Length > WorkspacePageMaxBytes {
			return 0, 0, fail(codeBadRequest,
				fmt.Sprintf("range.length must be at most %d bytes", WorkspacePageMaxBytes),
				map[string]any{"argument": "range.length", "limit": WorkspacePageMaxBytes})
		}
		length = *requested.Length
	}
	return offset, length, nil
}

// workspaceFilesChanges is the streaming watch this host does not implement. The
// refusal names the capability: no shipped panel calls it, so it degrades to an
// error a console can show rather than a stream that never opens.
func (h *Handler) workspaceFilesChanges(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented,
		"this host does not watch workspace files; reopen the file to see its current content",
		map[string]any{"capability": "workspace file watching"})
}

// workspaceFilesReadRelated reads a file named relative to another one. This host
// does not implement it, and no shipped panel calls it: the document preview
// reads the file it was opened with.
func (h *Handler) workspaceFilesReadRelated(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented,
		"this host does not read a file relative to another; read the file by its own path",
		map[string]any{"capability": "workspace file relation reads"})
}

// workspaceFileFailure turns an adapter's refusal into the method error the
// console expects, and anything unclassified into an internal error that names
// no path -- a raw filesystem error can carry a host path the caller never asked
// about.
func workspaceFileFailure(err error) *methodError {
	var refusal *WorkspaceFileError
	if errors.As(err, &refusal) {
		return fail(refusal.Code, refusal.Message, refusal.Details)
	}
	return fail(codeInternal, "workspace file: the host could not complete the request", nil)
}

// workspaceDependencyMissing is the honest answer when no file face is installed.
func workspaceDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no workspace file face; the serve command must install one with Handler.SetWorkspaceFiles",
		map[string]any{"dependency": "WorkspaceFiles"})
}

package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
)

// FileReference is one candidate the console's `@` menu offers: a workspace-
// relative slash path and whether it is a file or a directory. The shape is the
// vendored schema's own
// (`@deepseek-ai/dsh-api-session-controller#fileReferences/list:result`), an
// array of `{path, kind}` with no envelope around it -- these two methods answer
// their list directly.
type FileReference struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// FileReferenceSource lists the candidates one session's `@` query matches. It is
// a seam rather than a walker in this package because the walk belongs to the
// directory the host actually serves, which only the CLI knows (ADR 0134); a nil
// source leaves the namespace answering `unimplemented` with the dependency
// named, which is what a host with no readable workspace does.
type FileReferenceSource interface {
	FileReferenceCandidates(ctx context.Context, sessionID, query string) ([]FileReference, error)
}

// SetFileReferences installs the candidate source.
func (h *Handler) SetFileReferences(source FileReferenceSource) {
	h.fileReferencesMu.Lock()
	h.fileReferences = source
	h.fileReferencesMu.Unlock()
}

func (h *Handler) fileReferencesSource() FileReferenceSource {
	h.fileReferencesMu.RLock()
	defer h.fileReferencesMu.RUnlock()
	return h.fileReferences
}

// fileReferencesList answers POST /api/fileReferences/list. Its scope is the
// reference's `{context: "agent", wire: "agentId"}` -- the same scope the goals
// namespace is served under -- and its query is the path text following `@`.
//
// Two things are worth stating because they differ from the other namespaces
// this package serves: the result is a bare array (there is no `{ok, value}`
// union and no items wrapper), and the source is asked for the *session's*
// candidates rather than the caller's, because the console resolves the agent on
// the wire.
func (h *Handler) fileReferencesList(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "agentId", "query"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.fileReferenceSession(ctx, args)
	if failure != nil {
		return nil, failure
	}
	query, present, failure := stringArg(args, "query")
	if failure != nil {
		return nil, failure
	}
	if !present {
		return nil, argumentRequired("query")
	}
	source := h.fileReferencesSource()
	if source == nil {
		return nil, fileReferencesDependencyMissing()
	}
	candidates, err := source.FileReferenceCandidates(ctx, sessionID, query)
	if err != nil {
		return nil, fail(codeInternal, "list file references: "+err.Error(), map[string]any{"sessionId": sessionID})
	}
	if candidates == nil {
		// The console maps the array straight into its menu, so an empty answer is
		// `[]` rather than null.
		candidates = []FileReference{}
	}
	return candidates, nil
}

// fileReferenceSession reads the `agentId` this method is scoped by and refuses a
// session this host does not serve. The upstream resolution happens in the remote
// binder, which names no business code for a miss; this host answers with the
// session-not-found sentence every other session-scoped namespace uses, rather
// than inventing a code.
func (h *Handler) fileReferenceSession(ctx context.Context, args map[string]json.RawMessage) (string, *methodError) {
	sessionID, present, failure := stringArg(args, "agentId")
	if failure != nil {
		return "", failure
	}
	if !present {
		return "", argumentRequired("agentId")
	}
	if sessionID == "" {
		return "", argumentRequired("agentId")
	}
	if !h.sessionKnown(ctx, sessionID) {
		return "", fail(codeSessionNotFound,
			fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	return sessionID, nil
}

// fileReferencesDependencyMissing is the refusal a host with no readable
// workspace answers: `unimplemented` naming the seam, the same shape the skills
// and commands namespaces use for a dependency they were not given.
func fileReferencesDependencyMissing() *methodError {
	return fail(codeUnimplemented,
		"fileReferences/list is not configured: the host has no readable workspace; the serve command must install one with Handler.SetFileReferences",
		map[string]any{"dependency": "FileReferenceSource"})
}

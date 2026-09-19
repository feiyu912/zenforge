package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

// consoleWorkspaceFiles adapts the host's workspace to the console's
// workspaceFiles namespace (ADR 0089).
//
// Containment is the workspace's own: every path resolves under the root or is
// refused as outside-workspace, so the console's file sidebar cannot list or read
// past the directory the server was started with. The read cap is upstream's
// complete-file cap rather than the agent's smaller text cap, because the console
// asks for pages of a file and only the page cap should bound a page.
type consoleWorkspaceFiles struct {
	root      string
	workspace *workspacelocal.Workspace
}

// newConsoleWorkspaceFiles builds the read-only face over one workspace root.
func newConsoleWorkspaceFiles(root string) (*consoleWorkspaceFiles, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	ws, err := workspacelocal.New(workspacelocal.Config{
		Root: absolute,
		// The console reads text pages and byte windows from one face, so binary
		// reads are allowed here and the text path classifies binary content as
		// not-text -- the code the console renders an error from.
		AllowBinaryRead: true,
		MaxReadBytes:    dshapi.WorkspaceFileMaxBytes,
	})
	if err != nil {
		return nil, err
	}
	return &consoleWorkspaceFiles{root: absolute, workspace: ws}, nil
}

// consoleFileRefusal classifies a workspace error with upstream's code and
// details. It returns nil when the error is not one this adapter recognises, so
// the caller falls through to the internal error rather than guessing a code.
func consoleFileRefusal(target string, err error, limit int64) error {
	var refusal *dshapi.WorkspaceFileError
	switch {
	case errors.Is(err, workspace.ErrPathEscape):
		refusal = &dshapi.WorkspaceFileError{
			Code:    "workspace-file/outside-workspace",
			Message: fmt.Sprintf("%q is outside the workspace this host serves", target),
			Details: map[string]any{"path": target},
		}
	case errors.Is(err, workspace.ErrPathNotFound), errors.Is(err, os.ErrNotExist):
		refusal = &dshapi.WorkspaceFileError{
			Code:    "workspace-file/not-found",
			Message: fmt.Sprintf("%q does not exist in the workspace", target),
			Details: map[string]any{"path": target},
		}
	case errors.Is(err, workspace.ErrReadTooLarge):
		refusal = &dshapi.WorkspaceFileError{
			Code:    "workspace-file/too-large",
			Message: fmt.Sprintf("%q is larger than the %d byte limit this read allows", target, limit),
			Details: map[string]any{"path": target, "limit": limit},
		}
	case errors.Is(err, workspace.ErrBinaryFile):
		refusal = &dshapi.WorkspaceFileError{
			Code:    "workspace-file/not-text",
			Message: fmt.Sprintf("%q is not a text file", target),
			Details: map[string]any{"path": target},
		}
	case errors.Is(err, workspace.ErrUnsupportedFile):
		refusal = &dshapi.WorkspaceFileError{
			Code:    "workspace-file/not-regular-file",
			Message: fmt.Sprintf("%q is not a regular file", target),
			Details: map[string]any{"path": target, "kind": "other"},
		}
	default:
		return err
	}
	return refusal
}

// consoleNotRegularFile and consoleNotDirectory are the two classifications that
// need a stat the workspace already answered, rather than an error it raised.
func consoleNotRegularFile(target, kind string) error {
	return &dshapi.WorkspaceFileError{
		Code:    "workspace-file/not-regular-file",
		Message: fmt.Sprintf("%q is a %s, not a regular file", target, kind),
		Details: map[string]any{"path": target, "kind": kind},
	}
}

func consoleNotDirectory(target string) error {
	return &dshapi.WorkspaceFileError{
		Code:    "workspace-file/not-directory",
		Message: fmt.Sprintf("%q is a file, not a directory", target),
		Details: map[string]any{"path": target, "kind": "file"},
	}
}

// statOf builds the wire stat, with the absolute path the sidebar shows and a
// version that changes when the content does.
func (c *consoleWorkspaceFiles) statOf(info workspace.FileInfo) dshapi.WorkspaceFileStat {
	stat := dshapi.WorkspaceFileStat{
		AbsolutePath: filepath.Join(c.root, filepath.FromSlash(info.Path)),
		Version:      info.SHA256,
	}
	if !info.IsDir {
		size := info.Size
		stat.Bytes = &size
	}
	if stat.Version == "" {
		// Directories have no content hash; their version is what changes when
		// their contents do.
		stat.Version = fmt.Sprintf("%d-%d", info.ModTime, info.Size)
	}
	return stat
}

// List returns one directory's children.
func (c *consoleWorkspaceFiles) List(target string) (dshapi.WorkspaceDirectoryListing, error) {
	listing := dshapi.WorkspaceDirectoryListing{Path: target, Entries: []dshapi.WorkspaceDirectoryEntry{}}
	ctx := context.Background()
	info, err := c.workspace.Stat(ctx, target)
	if err != nil {
		return listing, consoleFileRefusal(target, err, 0)
	}
	if !info.IsDir {
		return listing, consoleNotDirectory(target)
	}
	children, err := c.workspace.List(ctx, target)
	if err != nil {
		return listing, consoleFileRefusal(target, err, 0)
	}
	for _, child := range children {
		entry := dshapi.WorkspaceDirectoryEntry{
			Name: path.Base(child.Path),
			Type: "file",
		}
		if child.IsDir {
			entry.Type = "directory"
		} else {
			size := child.Size
			entry.Size = &size
		}
		listing.Entries = append(listing.Entries, entry)
	}
	return listing, nil
}

// Stat reports one path's version and size.
func (c *consoleWorkspaceFiles) Stat(target string) (dshapi.WorkspaceFileStat, error) {
	info, err := c.workspace.Stat(context.Background(), target)
	if err != nil {
		return dshapi.WorkspaceFileStat{}, consoleFileRefusal(target, err, 0)
	}
	return c.statOf(info), nil
}

// readText reads a file's bytes and refuses anything the text surface cannot
// carry, with the code the console renders.
func (c *consoleWorkspaceFiles) readText(target string) (string, workspace.FileInfo, error) {
	ctx := context.Background()
	info, err := c.workspace.Stat(ctx, target)
	if err != nil {
		return "", workspace.FileInfo{}, consoleFileRefusal(target, err, 0)
	}
	if info.IsDir {
		return "", workspace.FileInfo{}, consoleNotRegularFile(target, "directory")
	}
	data, err := c.workspace.Read(ctx, target)
	if err != nil {
		return "", workspace.FileInfo{}, consoleFileRefusal(target, err, dshapi.WorkspaceFileMaxBytes)
	}
	// Upstream marks a file binary by the NUL byte and refuses anything that is
	// not valid UTF-8 with the same code, because a page of mojibake is worse
	// than an error the console can show.
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", workspace.FileInfo{}, &dshapi.WorkspaceFileError{
			Code:    "workspace-file/not-text",
			Message: fmt.Sprintf("%q is not a text file", target),
			Details: map[string]any{"path": target},
		}
	}
	return string(data), info, nil
}

// ReadPage returns lines offset through offset+limit-1.
func (c *consoleWorkspaceFiles) ReadPage(target string, offset, limit int) (dshapi.WorkspaceFileText, error) {
	page := dshapi.WorkspaceFileText{Offset: offset}
	text, info, err := c.readText(target)
	if err != nil {
		return page, err
	}
	cut, lines, eof, over := cutTextPage(text, offset, limit, dshapi.WorkspacePageMaxBytes)
	if over {
		return page, &dshapi.WorkspaceFileError{
			Code: "workspace-file/too-large",
			Message: fmt.Sprintf("lines %d-%d of %q exceed the %d byte page cap; ask for a smaller page",
				offset, offset+limit-1, target, dshapi.WorkspacePageMaxBytes),
			Details: map[string]any{"path": target, "limit": dshapi.WorkspacePageMaxBytes},
		}
	}
	page.WorkspaceFileStat = c.statOf(info)
	page.Text = cut
	page.Lines = lines
	page.EOF = eof
	return page, nil
}

// ReadAll returns the whole file. The workspace's own cap refuses a file over the
// complete-file limit rather than truncating it, which is upstream's rule too.
func (c *consoleWorkspaceFiles) ReadAll(target string) (dshapi.WorkspaceFileText, error) {
	page := dshapi.WorkspaceFileText{Offset: 1}
	text, info, err := c.readText(target)
	if err != nil {
		return page, err
	}
	page.WorkspaceFileStat = c.statOf(info)
	page.Text = text
	page.Lines = countLines(text)
	page.EOF = true
	return page, nil
}

// ReadBytes returns one byte window in base64.
func (c *consoleWorkspaceFiles) ReadBytes(target string, offset, length int) (dshapi.WorkspaceFileBytes, error) {
	window := dshapi.WorkspaceFileBytes{Offset: offset}
	ctx := context.Background()
	info, err := c.workspace.Stat(ctx, target)
	if err != nil {
		return window, consoleFileRefusal(target, err, 0)
	}
	if info.IsDir {
		return window, consoleNotRegularFile(target, "directory")
	}
	data, err := c.workspace.Read(ctx, target)
	if err != nil {
		return window, consoleFileRefusal(target, err, dshapi.WorkspaceFileMaxBytes)
	}
	if offset > len(data) {
		offset = len(data)
	}
	end := offset + length
	if end > len(data) {
		end = len(data)
	}
	window.WorkspaceFileStat = c.statOf(info)
	window.Offset = offset
	window.Data = base64.StdEncoding.EncodeToString(data[offset:end])
	window.EOF = end >= len(data)
	return window, nil
}

// countLines counts the lines a text page would number, so a file ending in a
// newline does not report an extra empty line.
func countLines(text string) int {
	if text == "" {
		return 0
	}
	trimmed := strings.TrimSuffix(text, "\n")
	return strings.Count(trimmed, "\n") + 1
}

// cutTextPage takes lines offset through offset+limit-1 (1-based, inclusive),
// reporting how many lines the page holds, whether it reached the end of the file
// and whether the page exceeded the byte cap.
//
// The cap is checked while collecting, so one enormous line cannot grow the page
// past it -- upstream's reasoning in src/index.ts:106-112 -- and the page is
// refused rather than cut, because a silently shortened page reads as the whole
// page.
func cutTextPage(text string, offset, limit, maxBytes int) (string, int, bool, bool) {
	if text == "" {
		return "", 0, true, false
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if offset > len(lines) {
		// A page past the last line is empty and final, not an error.
		return "", 0, true, false
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	selected := lines[offset-1 : end]
	bytes := 0
	for _, line := range selected {
		bytes += len(line) + 1
		if bytes > maxBytes {
			return "", 0, false, true
		}
	}
	return strings.Join(selected, "\n"), len(selected), end >= len(lines), false
}

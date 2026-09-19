package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Wire shapes for POST /api/directoryPicker/*, the namespace the console's
// workspace picker browses with. ui-workspace's service calls
// list/createDirectory through ctx.uiWorkspace, which
// ui-directory-picker-browse fills (packages/client/ui-directory-picker-browse/
// src/client/flow.ts), and its navigation face offers pick for a native dialog
// (packages/client/ui-workspace/src/client/navigation.ts:207-222). The types
// mirror packages/host/directory-picker/lib/types/types.d.ts:15-48 field for
// field.
//
// The picking seam upstream is a *capability union*: a host either opens one OS
// chooser on its own display (native) or serves listing and creation
// primitives (browse). This host is a server with no operator display, so it
// answers the browse half and refuses the native half by name. The console's
// in-app browser is then the whole interaction.

// Upstream's directory-picker error codes (src/index.ts:75). They are method
// errors, so they arrive with HTTP 200 and the console branches on the code and
// its message (navigation.ts:94-101).
const (
	codeDirectoryUnreadable   = "directory-unreadable"
	codeDirectoryExists       = "directory-exists"
	codeDirectoryCreateFailed = "directory-create-failed"
)

// DirectoryPickerMaxEntries bounds one listing level, as upstream's browse
// backend bounds its own complete result: the name-sorted tail is dropped and
// the listing reports itself truncated (types.d.ts:44-48).
const DirectoryPickerMaxEntries = 2000

// directoryPickerMaxEntries is the bound the handler reads. It is a variable so
// a test can lower it instead of materializing thousands of directories; the
// exported constant above is the documented bound a caller can rely on.
var directoryPickerMaxEntries = DirectoryPickerMaxEntries

// DirectoryPickerEntry is one directory row: a listing child or a breadcrumb
// ancestor. Upstream types.d.ts:15-23.
type DirectoryPickerEntry struct {
	// Name is the base name shown in a browser row; a root crumb carries its
	// full path instead.
	Name string `json:"name"`
	// Path is the absolute host path, so a client never joins segments itself.
	Path string `json:"path"`
	// Hidden is the host platform's convention: dot-prefixed on POSIX.
	Hidden bool `json:"hidden"`
}

// DirectoryPickerListing is the answer to directoryPicker/list: one level plus
// its ancestry. Upstream types.d.ts:25-48.
type DirectoryPickerListing struct {
	// Path is the absolute path of the listed directory.
	Path string `json:"path"`
	// Home is the host account's home directory, for the browser's "Home" root.
	Home string `json:"home"`
	// Crumbs run from the filesystem root to the listed directory inclusive;
	// every crumb is a jump target.
	Crumbs []DirectoryPickerEntry `json:"crumbs"`
	// Entries are the direct child directories, name-sorted; symlinks to
	// directories are included and files are not.
	Entries []DirectoryPickerEntry `json:"entries"`
	// Truncated reports that the level had more child directories than
	// DirectoryPickerMaxEntries and the tail was dropped.
	Truncated bool `json:"truncated"`
}

// directoryPickerList answers POST /api/directoryPicker/list. An absent path
// lists the host account's home directory; anything else must be absolute,
// because a wire value must never resolve against the host's working directory
// (src/index.ts:41-47).
func (h *Handler) directoryPickerList(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "path"); failure != nil {
		return nil, failure
	}
	path, present, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	home, err := os.UserHomeDir()
	if !present || strings.TrimSpace(path) == "" {
		if err != nil || home == "" {
			return nil, fail(codeDirectoryUnreadable,
				"this host has no home directory to list; pass an absolute path",
				map[string]any{"path": path})
		}
		path = home
	}
	if !filepath.IsAbs(path) {
		return nil, fail(codeDirectoryUnreadable,
			fmt.Sprintf("path %q is not a fully qualified path; the picker lists absolute directories only", path),
			map[string]any{"path": path})
	}
	listed := filepath.Clean(path)
	entries, truncated, failure := directoryPickerChildren(listed)
	if failure != nil {
		return nil, failure
	}
	return DirectoryPickerListing{
		Path:      listed,
		Home:      home,
		Crumbs:    directoryPickerCrumbs(listed),
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

// directoryPickerCreateDirectory answers POST /api/directoryPicker/createDirectory:
// one child directory under an existing parent. Upstream's browse backend
// refuses an existing child and any parent failure distinctly (src/index.ts:48-57).
func (h *Handler) directoryPickerCreateDirectory(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "path", "name"); failure != nil {
		return nil, failure
	}
	path, _, failure := stringArg(args, "path")
	if failure != nil {
		return nil, failure
	}
	name, _, failure := stringArg(args, "name")
	if failure != nil {
		return nil, failure
	}
	if !filepath.IsAbs(path) {
		return nil, fail(codeDirectoryCreateFailed,
			fmt.Sprintf("path %q is not a fully qualified path; a directory can only be created under an absolute parent", path),
			map[string]any{"path": path, "name": name})
	}
	if !directoryPickerNameValid(name) {
		return nil, fail(codeDirectoryCreateFailed,
			fmt.Sprintf("%q is not a single non-blank directory name", name),
			map[string]any{"path": path, "name": name})
	}
	parent := filepath.Clean(path)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return nil, fail(codeDirectoryCreateFailed,
			fmt.Sprintf("%q is not an existing directory", parent),
			map[string]any{"path": parent, "name": name})
	}
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fail(codeDirectoryExists,
				fmt.Sprintf("%q already exists", target),
				map[string]any{"path": target, "name": name})
		}
		return nil, fail(codeDirectoryCreateFailed,
			fmt.Sprintf("this host cannot create %q", target),
			map[string]any{"path": parent, "name": name})
	}
	return target, nil
}

// directoryPickerPick refuses the native half of the picking seam by name: the
// chooser upstream opens renders on the *host's* display, and this host is a
// server with none. Saying so is what lets the console keep its in-app browser
// as the whole interaction instead of waiting on a dialog that never appears.
func (h *Handler) directoryPickerPick(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args); failure != nil {
		return nil, failure
	}
	return nil, fail(codeUnimplemented,
		"this host has no operator display to open a native directory chooser on; directoryPicker/list and directoryPicker/createDirectory serve the console's in-app browser instead",
		map[string]any{"capability": "a native directory chooser"})
}

// directoryPickerChildren lists one level's child directories. A file is not a
// row, a symlink is decided by what it points at, and the cap reports itself
// rather than cutting the level silently.
func directoryPickerChildren(dir string) ([]DirectoryPickerEntry, bool, *methodError) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, false, fail(codeDirectoryUnreadable,
			fmt.Sprintf("%q is not a directory this host can list", dir),
			map[string]any{"path": dir})
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fail(codeDirectoryUnreadable,
			fmt.Sprintf("%q is not a directory this host can list", dir),
			map[string]any{"path": dir})
	}
	entries := make([]DirectoryPickerEntry, 0, len(children))
	for _, child := range children {
		if !directoryPickerIsDirectory(filepath.Join(dir, child.Name()), child) {
			continue
		}
		entries = append(entries, DirectoryPickerEntry{
			Name:   child.Name(),
			Path:   filepath.Join(dir, child.Name()),
			Hidden: strings.HasPrefix(child.Name(), "."),
		})
	}
	truncated := false
	if len(entries) > directoryPickerMaxEntries {
		entries = entries[:directoryPickerMaxEntries]
		truncated = true
	}
	return entries, truncated, nil
}

// directoryPickerIsDirectory reports whether a listing child is a directory,
// following symlinks to their target so a symlinked directory is a row.
func directoryPickerIsDirectory(path string, child fs.DirEntry) bool {
	if child.IsDir() {
		return true
	}
	if child.Type()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := os.Stat(path)
	return err == nil && target.IsDir()
}

// directoryPickerCrumbs builds the ancestor chain from the filesystem root to
// the listed directory inclusive; the root crumb carries its full path as its
// name, every crumb is visible, and each is a jump target (types.d.ts:31-36).
func directoryPickerCrumbs(dir string) []DirectoryPickerEntry {
	reversed := make([]DirectoryPickerEntry, 0, 8)
	for current := dir; ; {
		name := filepath.Base(current)
		if current == string(filepath.Separator) || name == string(filepath.Separator) {
			name = current
		}
		reversed = append(reversed, DirectoryPickerEntry{Name: name, Path: current, Hidden: false})
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	crumbs := make([]DirectoryPickerEntry, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		crumbs = append(crumbs, reversed[i])
	}
	return crumbs
}

// directoryPickerNameValid reports whether a name is one path segment: non-blank,
// no separator, and neither "." nor ".." (src/index.ts:53-56).
func directoryPickerNameValid(name string) bool {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return false
	}
	return !strings.ContainsRune(name, '/')
}

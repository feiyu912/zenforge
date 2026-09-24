package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workspace confines every file tool to BENCH_WORKSPACE.
//
// The check is lexical (filepath.Clean + filepath.Rel) and rejects both
// relative escapes ("../x") and absolute paths outside the root. Symlinks
// inside the workspace are not resolved; the harness creates a fresh tree, and
// resolving them would make the accepted path set depend on the host's /tmp
// symlink layout (macOS /tmp -> /private/tmp) rather than on the input.
type workspace struct {
	root string
}

var (
	errEmptyPath        = errors.New("path is required")
	errOutsideWorkspace = errors.New("path escapes BENCH_WORKSPACE")
)

func newWorkspace(root string) (*workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace %q: %w", root, err)
	}
	return &workspace{root: filepath.Clean(abs)}, nil
}

func (w *workspace) Root() string { return w.root }

// Resolve turns a tool-supplied path into an absolute path inside the
// workspace, or fails.
func (w *workspace) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errEmptyPath
	}
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(w.root, p))
	}
	if !within(w.root, abs) {
		return "", fmt.Errorf("%w: %q", errOutsideWorkspace, p)
	}
	return abs, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (w *workspace) ReadFile(p string) (string, error) {
	abs, err := w.Resolve(p)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (w *workspace) WriteFile(p, content string) error {
	abs, err := w.Resolve(p)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(abs); dir != w.root {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

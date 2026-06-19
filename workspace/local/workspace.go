package local

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/workspace"
)

type Config struct {
	Root            string
	FollowSymlink   bool
	MaxReadBytes    int64
	MaxWriteBytes   int64
	CreateParentDir bool
	AllowBinaryRead bool
}

type Workspace struct {
	root            string
	followSymlink   bool
	maxReadBytes    int64
	maxWriteBytes   int64
	createParentDir bool
	allowBinaryRead bool
}

func New(config Config) (*Workspace, error) {
	root := config.Root
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &Workspace{
		root:            resolved,
		followSymlink:   config.FollowSymlink,
		maxReadBytes:    config.MaxReadBytes,
		maxWriteBytes:   config.MaxWriteBytes,
		createParentDir: config.CreateParentDir,
		allowBinaryRead: config.AllowBinaryRead,
	}, nil
}

func (w *Workspace) Read(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, _, err := w.resolve(path, true)
	if err != nil {
		return nil, err
	}
	if workspace.IsBlockedDevicePath(resolved) {
		return nil, workspace.ErrUnsupportedFile
	}
	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil, workspace.ErrPathNotFound
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("workspace read %q: is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return nil, workspace.ErrUnsupportedFile
	}
	if !w.allowBinaryRead && workspace.IsBinaryPath(resolved) {
		return nil, workspace.ErrBinaryFile
	}
	if w.maxReadBytes > 0 && info.Size() > w.maxReadBytes {
		return nil, workspace.ErrReadTooLarge
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	if !w.allowBinaryRead && isBinary(data) {
		return nil, workspace.ErrBinaryFile
	}
	return data, nil
}

func (w *Workspace) Write(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.maxWriteBytes > 0 && int64(len(data)) > w.maxWriteBytes {
		return workspace.ErrWriteTooLarge
	}
	resolved, _, err := w.resolve(path, false)
	if err != nil {
		return err
	}
	if info, err := os.Stat(resolved); err == nil && !info.Mode().IsRegular() {
		return workspace.ErrUnsupportedFile
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if w.createParentDir {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(resolved, data, 0o644)
}

func (w *Workspace) List(ctx context.Context, path string) ([]workspace.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, rel, err := w.resolve(path, true)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil, workspace.ErrPathNotFound
	}
	if err != nil {
		return nil, err
	}
	out := make([]workspace.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		child := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		if child == "." {
			child = entry.Name()
		}
		out = append(out, fileInfo(child, info))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func (w *Workspace) Grep(ctx context.Context, query workspace.GrepQuery) ([]workspace.Match, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pattern, err := regexp.Compile(query.Pattern)
	if err != nil {
		return nil, workspace.ErrInvalidPattern
	}
	resolved, rootRel, err := w.resolve(query.Path, true)
	if err != nil {
		return nil, err
	}
	max := query.MaxMatches
	if max <= 0 {
		max = 100
	}
	var matches []workspace.Match
	err = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if workspace.IsBinaryPath(path) {
			return nil
		}
		if len(matches) >= max {
			return filepath.SkipAll
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(data) {
			return nil
		}
		rel, err := filepath.Rel(resolved, path)
		if err != nil {
			return err
		}
		displayPath := filepath.ToSlash(filepath.Join(rootRel, rel))
		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if pattern.MatchString(line) {
				matches = append(matches, workspace.Match{Path: displayPath, Line: lineNo, Text: line})
				if len(matches) >= max {
					return filepath.SkipAll
				}
			}
		}
		return scanner.Err()
	})
	if errors.Is(err, filepath.SkipAll) {
		err = nil
	}
	return matches, err
}

func (w *Workspace) Stat(ctx context.Context, path string) (workspace.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return workspace.FileInfo{}, err
	}
	resolved, rel, err := w.resolve(path, true)
	if err != nil {
		return workspace.FileInfo{}, err
	}
	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return workspace.FileInfo{}, workspace.ErrPathNotFound
	}
	if err != nil {
		return workspace.FileInfo{}, err
	}
	out := fileInfo(rel, info)
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(resolved)
		if err != nil {
			return workspace.FileInfo{}, err
		}
		sum := sha256.Sum256(data)
		out.SHA256 = hex.EncodeToString(sum[:])
	}
	return out, nil
}

func (w *Workspace) resolve(raw string, mustExist bool) (string, string, error) {
	if raw == "" {
		raw = "."
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, string(filepath.Separator))
	}
	candidate := filepath.Join(w.root, clean)
	if !inside(w.root, candidate) {
		return "", "", workspace.ErrPathEscape
	}
	checkPath := candidate
	if mustExist || w.followSymlink {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			if mustExist || !errors.Is(err, os.ErrNotExist) {
				return "", "", err
			}
		} else {
			checkPath = resolved
		}
	} else if _, err := os.Lstat(candidate); err == nil {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", "", err
		}
		checkPath = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	} else {
		parent := filepath.Dir(candidate)
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", "", err
			}
		} else {
			checkPath = filepath.Join(resolvedParent, filepath.Base(candidate))
		}
	}
	if !inside(w.root, checkPath) {
		return "", "", workspace.ErrPathEscape
	}
	rel, err := filepath.Rel(w.root, candidate)
	if err != nil {
		return "", "", err
	}
	return candidate, filepath.ToSlash(rel), nil
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func fileInfo(path string, info os.FileInfo) workspace.FileInfo {
	return workspace.FileInfo{
		Path:    filepath.ToSlash(path),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().UnixMilli(),
	}
}

func isBinary(data []byte) bool {
	for i, b := range data {
		if i >= 8000 {
			break
		}
		if b == 0 {
			return true
		}
	}
	return false
}

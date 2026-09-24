package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceResolve(t *testing.T) {
	root := t.TempDir()
	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatalf("newWorkspace: %v", err)
	}

	ok := []struct{ in, want string }{
		{"out.txt", filepath.Join(root, "out.txt")},
		{"sub/dir/out.txt", filepath.Join(root, "sub", "dir", "out.txt")},
		{"./out.txt", filepath.Join(root, "out.txt")},
		{"sub/../out.txt", filepath.Join(root, "out.txt")},
		{filepath.Join(root, "abs.txt"), filepath.Join(root, "abs.txt")},
		{"", ""}, // filled below: the workspace root itself is inside
	}
	ok[len(ok)-1].in = root
	ok[len(ok)-1].want = root

	for _, c := range ok {
		got, err := ws.Resolve(c.in)
		if err != nil {
			t.Errorf("Resolve(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{
		"../escape.txt",
		"sub/../../escape.txt",
		"/etc/passwd",
		filepath.Join(root, "..", "escape.txt"),
		"   ",
	}
	for _, in := range bad {
		if got, err := ws.Resolve(in); err == nil {
			t.Errorf("Resolve(%q) = %q, want an error", in, got)
		}
	}
}

func TestWorkspaceConfinementErrors(t *testing.T) {
	root := t.TempDir()
	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatalf("newWorkspace: %v", err)
	}

	if _, err := ws.ReadFile("../outside.txt"); !errors.Is(err, errOutsideWorkspace) {
		t.Errorf("read outside: err = %v, want errOutsideWorkspace", err)
	}
	if err := ws.WriteFile("/tmp/zenforge-eino-escape.txt", "no"); !errors.Is(err, errOutsideWorkspace) {
		t.Errorf("write outside: err = %v, want errOutsideWorkspace", err)
	}
	if _, err := os.Stat("/tmp/zenforge-eino-escape.txt"); err == nil {
		t.Fatal("write outside the workspace created a file")
	}
	if _, err := ws.ReadFile(""); !errors.Is(err, errEmptyPath) {
		t.Errorf("empty path: err = %v, want errEmptyPath", err)
	}
}

func TestWorkspaceReadWriteRoundTrip(t *testing.T) {
	root := t.TempDir()
	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatalf("newWorkspace: %v", err)
	}

	content := "line 1\nline 2 with \"quotes\"\n"
	if err := ws.WriteFile("sub/out.txt", content); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ws.ReadFile("sub/out.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != content {
		t.Errorf("content round trip = %q, want %q", got, content)
	}

	onDisk, err := os.ReadFile(filepath.Join(root, "sub", "out.txt"))
	if err != nil {
		t.Fatalf("reading on disk: %v", err)
	}
	if string(onDisk) != content {
		t.Errorf("on-disk content = %q, want %q", string(onDisk), content)
	}

	if _, err := ws.ReadFile("missing.txt"); err == nil {
		t.Error("reading a missing file should fail")
	} else if !strings.Contains(err.Error(), "missing.txt") {
		t.Errorf("missing-file error should name the file: %v", err)
	}
}

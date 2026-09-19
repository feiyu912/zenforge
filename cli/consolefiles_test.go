package cli

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshapi"
)

// consoleWorkspaceFixture builds a small tree and the console's face over it.
func consoleWorkspaceFixture(t *testing.T) (*consoleWorkspaceFiles, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"README.md":    "one\ntwo\nthree\n",
		"src/main.go":  "package main\n",
		"empty.txt":    "",
		"blob.bin":     "\x00\x01\x02",
		"separate.txt": "alpha\nbeta",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	face, err := newConsoleWorkspaceFiles(root)
	if err != nil {
		t.Fatalf("newConsoleWorkspaceFiles: %v", err)
	}
	return face, root
}

// consoleFileCode reports the refusal code an error carries, or "" when it is not
// one of the console's classified refusals.
func consoleFileCode(t *testing.T, err error) string {
	t.Helper()
	var refusal *dshapi.WorkspaceFileError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

func TestConsoleWorkspaceFilesListAndStat(t *testing.T) {
	face, root := consoleWorkspaceFixture(t)
	listing, err := face.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]dshapi.WorkspaceDirectoryEntry{}
	for _, entry := range listing.Entries {
		byName[entry.Name] = entry
	}
	for name, wantType := range map[string]string{
		"README.md": "file", "src": "directory", "blob.bin": "file",
	} {
		entry, ok := byName[name]
		if !ok {
			t.Fatalf("listing %+v is missing %s", listing.Entries, name)
		}
		if entry.Type != wantType {
			t.Fatalf("%s type = %q, want %q", name, entry.Type, wantType)
		}
	}
	if entry := byName["src"]; entry.Size != nil {
		t.Fatalf("directory size = %d, want none", *entry.Size)
	}
	if entry := byName["README.md"]; entry.Size == nil || *entry.Size != 14 {
		t.Fatalf("README.md size = %v, want 14", entry.Size)
	}
	if listing.Path != "" || listing.Truncated {
		t.Fatalf("listing = %+v, want the requested path and no truncation", listing)
	}

	stat, err := face.Stat("README.md")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Bytes == nil || *stat.Bytes != 14 {
		t.Fatalf("bytes = %v, want 14", stat.Bytes)
	}
	if stat.AbsolutePath != filepath.Join(root, "README.md") {
		t.Fatalf("absolutePath = %q, want the path under the workspace root", stat.AbsolutePath)
	}
	if len(stat.Version) != 64 {
		t.Fatalf("version = %q, want the content hash", stat.Version)
	}
	// Two files with the same content share a version, and different content does
	// not: that is what the console uses the version for.
	other, err := face.Stat("src/main.go")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if other.Version == stat.Version {
		t.Fatal("different content produced the same version")
	}
}

func TestConsoleWorkspaceFilesReadPages(t *testing.T) {
	face, _ := consoleWorkspaceFixture(t)
	cases := []struct {
		name      string
		path      string
		offset    int
		limit     int
		wantText  string
		wantLines int
		wantEOF   bool
	}{
		{"the first line", "README.md", 1, 1, "one", 1, false},
		{"the second line", "README.md", 2, 1, "two", 1, false},
		{"the last line", "README.md", 3, 10, "three", 1, true},
		{"past the last line", "README.md", 9, 10, "", 0, true},
		{"a page spanning lines", "README.md", 2, 2, "two\nthree", 2, true},
		{"a file with no trailing newline", "separate.txt", 1, 5, "alpha\nbeta", 2, true},
		{"an empty file", "empty.txt", 1, 5, "", 0, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			page, err := face.ReadPage(testCase.path, testCase.offset, testCase.limit)
			if err != nil {
				t.Fatalf("ReadPage: %v", err)
			}
			if page.Text != testCase.wantText || page.Lines != testCase.wantLines || page.EOF != testCase.wantEOF {
				t.Fatalf("page = %+v, want text %q, %d lines, eof %v",
					page, testCase.wantText, testCase.wantLines, testCase.wantEOF)
			}
			if page.Offset != testCase.offset {
				t.Fatalf("offset = %d, want %d", page.Offset, testCase.offset)
			}
		})
	}

	all, err := face.ReadAll("README.md")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if all.Text != "one\ntwo\nthree\n" || all.Lines != 3 || !all.EOF || all.Offset != 1 {
		t.Fatalf("readAll = %+v, want the whole file and three lines", all)
	}
}

func TestConsoleWorkspaceFilesReadBytes(t *testing.T) {
	face, _ := consoleWorkspaceFixture(t)
	window, err := face.ReadBytes("README.md", 4, 3)
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	if want := base64.StdEncoding.EncodeToString([]byte("two")); window.Data != want {
		t.Fatalf("data = %q, want %q", window.Data, want)
	}
	if window.Offset != 4 || window.EOF {
		t.Fatalf("window = %+v, want offset 4 and eof false", window)
	}
	// A window reaching past the end is clamped and reports eof, rather than
	// failing: the console reads the tail of a file this way.
	tail, err := face.ReadBytes("README.md", 10, 100)
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	// "one\ntwo\nthree\n": byte 10 is the third 'r', so the tail is "ree\n".
	if want := base64.StdEncoding.EncodeToString([]byte("ree\n")); tail.Data != want {
		t.Fatalf("tail = %q, want %q", tail.Data, want)
	}
	if !tail.EOF {
		t.Fatal("tail eof = false, want true")
	}
	// Binary content is fine for the byte surface; it is the text surface that
	// refuses it.
	if _, err := face.ReadBytes("blob.bin", 0, 3); err != nil {
		t.Fatalf("ReadBytes on binary content: %v", err)
	}
}

// Every refusal the console renders an error from is classified here, and the two
// that need a stat say what the path is instead of what it is not.
func TestConsoleWorkspaceFilesClassifiesRefusals(t *testing.T) {
	face, _ := consoleWorkspaceFixture(t)
	cases := []struct {
		name string
		call func() error
		code string
	}{
		{"a missing file", func() error { _, err := face.Stat("missing.txt"); return err }, "workspace-file/not-found"},
		{"a path outside the workspace", func() error { _, err := face.List("../secrets"); return err }, "workspace-file/outside-workspace"},
		{"listing a file", func() error { _, err := face.List("README.md"); return err }, "workspace-file/not-directory"},
		{"reading a directory", func() error { _, err := face.ReadAll("src"); return err }, "workspace-file/not-regular-file"},
		{"reading binary content as text", func() error { _, err := face.ReadPage("blob.bin", 1, 1); return err }, "workspace-file/not-text"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := consoleFileCode(t, testCase.call()); code != testCase.code {
				t.Fatalf("code = %q, want %q", code, testCase.code)
			}
		})
	}
	// A directory read names the kind, because a console that shows "src is a
	// directory" is more useful than one that shows "src is not a file".
	_, err := face.ReadAll("src")
	var refusal *dshapi.WorkspaceFileError
	if !errors.As(err, &refusal) || refusal.Details["kind"] != "directory" {
		t.Fatalf("details = %v, want the directory kind named", refusal)
	}
}

// The page cap is checked while collecting, so one enormous line cannot grow a
// page past it, and the page is refused rather than cut.
func TestCutTextPageRefusesAPageOverTheCap(t *testing.T) {
	text := strings.Repeat("0123456789\n", 20)
	if _, _, _, over := cutTextPage(text, 1, 20, 1024); over {
		t.Fatal("a page under the cap was refused")
	}
	if _, _, _, over := cutTextPage(text, 1, 20, 50); !over {
		t.Fatal("a page over the cap was accepted")
	}
	// One line larger than the cap is refused too, rather than being kept whole.
	if _, _, _, over := cutTextPage(strings.Repeat("x", 100)+"\n", 1, 1, 50); !over {
		t.Fatal("a single line over the cap was accepted")
	}
	page, lines, eof, over := cutTextPage("a\nb\nc\n", 2, 1, 1024)
	if over || page != "b" || lines != 1 || eof {
		t.Fatalf("page = %q lines = %d eof = %v over = %v, want the middle line and eof false", page, lines, eof, over)
	}
}

package applypatch

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

// memFS is an in-memory FS for the applier tests.
type memFS struct {
	files map[string]string
	dirs  map[string]bool
}

func newMemFS(seed map[string]string) *memFS {
	files := map[string]string{}
	for name, content := range seed {
		files[name] = content
	}
	return &memFS{files: files, dirs: map[string]bool{}}
}

func (m *memFS) ReadFile(name string) ([]byte, error) {
	content, ok := m.files[name]
	if !ok {
		return nil, fmt.Errorf("open %s: %w", name, fs.ErrNotExist)
	}
	return []byte(content), nil
}

func (m *memFS) WriteFile(name string, data []byte) error {
	m.files[name] = string(data)
	return nil
}

func (m *memFS) Remove(name string) error {
	if _, ok := m.files[name]; !ok {
		return fmt.Errorf("remove %s: %w", name, fs.ErrNotExist)
	}
	delete(m.files, name)
	return nil
}

func (m *memFS) MkdirAll(name string) error {
	if name != "" && name != "." {
		m.dirs[name] = true
	}
	return nil
}

func (m *memFS) IsDir(name string) (bool, error) {
	if m.dirs[name] {
		return true, nil
	}
	if _, ok := m.files[name]; ok {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", name, fs.ErrNotExist)
}

func wrapPatch(body string) string {
	return "*** Begin Patch\n" + body + "\n*** End Patch"
}

func parseHunks(t *testing.T, patch string) []Hunk {
	t.Helper()
	hunks, err := Parse(patch)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	return hunks
}

func TestParseAddDeleteUpdateAndMove(t *testing.T) {
	hunks := parseHunks(t, wrapPatch(`*** Add File: path/add.py
+abc
+def
*** Delete File: path/delete.py
*** Update File: path/update.py
*** Move to: path/update2.py
@@ def f():
-    pass
+    return 123`))

	if len(hunks) != 3 {
		t.Fatalf("hunks = %d, want 3", len(hunks))
	}
	add := hunks[0]
	if add.Kind != "add" || add.Path != "path/add.py" || add.Contents != "abc\ndef\n" {
		t.Fatalf("add hunk = %#v", add)
	}
	if hunks[1].Kind != "delete" || hunks[1].Path != "path/delete.py" {
		t.Fatalf("delete hunk = %#v", hunks[1])
	}
	update := hunks[2]
	if update.Kind != "update" || update.Path != "path/update.py" || update.MovePath != "path/update2.py" {
		t.Fatalf("update hunk = %#v", update)
	}
	if len(update.Chunks) != 1 {
		t.Fatalf("chunks = %#v", update.Chunks)
	}
	chunk := update.Chunks[0]
	if chunk.Context != "def f():" ||
		strings.Join(chunk.OldLines, "|") != "    pass" ||
		strings.Join(chunk.NewLines, "|") != "    return 123" {
		t.Fatalf("chunk = %#v", chunk)
	}
	if chunk.EndOfFile {
		t.Fatal("chunk unexpectedly anchored at end of file")
	}
}

func TestParseUpdateWithoutExplicitContextHeader(t *testing.T) {
	hunks := parseHunks(t, wrapPatch(`*** Update File: file2.py
 import foo
+bar`))
	chunk := hunks[0].Chunks[0]
	if chunk.Context != "" {
		t.Fatalf("context = %q, want none", chunk.Context)
	}
	if strings.Join(chunk.OldLines, "|") != "import foo" || strings.Join(chunk.NewLines, "|") != "import foo|bar" {
		t.Fatalf("chunk = %#v", chunk)
	}
	if len(chunk.ContextIndices) != 1 || chunk.ContextIndices[0] != [2]int{0, 0} {
		t.Fatalf("context indices = %#v", chunk.ContextIndices)
	}
}

func TestParseInsertOnlyChunkAndFollowOnHunk(t *testing.T) {
	hunks := parseHunks(t, wrapPatch(`*** Update File: file.py
@@
+line
*** Add File: other.py
+content`))
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(hunks))
	}
	chunk := hunks[0].Chunks[0]
	if chunk.Context != "" || len(chunk.OldLines) != 0 || strings.Join(chunk.NewLines, "|") != "line" {
		t.Fatalf("chunk = %#v", chunk)
	}
	if hunks[1].Kind != "add" || hunks[1].Contents != "content\n" {
		t.Fatalf("second hunk = %#v", hunks[1])
	}
}

func TestParseEndOfFileAnchor(t *testing.T) {
	hunks := parseHunks(t, wrapPatch(`*** Update File: tail.txt
@@
 last
*** End of File`))
	chunk := hunks[0].Chunks[0]
	if !chunk.EndOfFile {
		t.Fatalf("chunk = %#v, want EndOfFile", chunk)
	}
	if strings.Join(chunk.OldLines, "|") != "last" {
		t.Fatalf("old lines = %#v", chunk.OldLines)
	}
}

func TestParseEmptyPatchProducesNoHunks(t *testing.T) {
	if hunks := parseHunks(t, wrapPatch("")); len(hunks) != 0 {
		t.Fatalf("hunks = %#v, want none", hunks)
	}
}

func TestParseStripsHeredocWrapper(t *testing.T) {
	hunks := parseHunks(t, "<<'EOF'\n*** Begin Patch\n*** Add File: hello.txt\n+hello\n*** End Patch\nEOF")
	if len(hunks) != 1 || hunks[0].Path != "hello.txt" || hunks[0].Contents != "hello\n" {
		t.Fatalf("hunks = %#v", hunks)
	}
}

func TestParseErrorsMatchTheReferenceDiagnostics(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  string
	}{
		{"missing begin", "bad", "The first line of the patch must be '*** Begin Patch'"},
		{"missing end", "*** Begin Patch\nbad", "The last line of the patch must be '*** End Patch'"},
		{"empty update", "*** Begin Patch\n*** Update File: test.py\n*** End Patch", "Update file hunk for path 'test.py' is empty"},
		{"bad header", "*** Begin Patch\nnope\n*** End Patch", "is not a valid hunk header"},
		{"add without lines", "*** Begin Patch\n*** Add File: a.txt\n*** End Patch", "requires at least one line"},
		{"line after a changed line", "*** Begin Patch\n*** Update File: a.txt\n@@\n+ok\n?bad\n*** End Patch", "Expected update hunk to start with a @@ context marker"},
		{"unexpected line in an empty chunk", "*** Begin Patch\n*** Update File: a.txt\n@@\n?bad\n*** End Patch", "Unexpected line found in update hunk"},
		{"context after eof", "*** Begin Patch\n*** Update File: a.txt\n@@\n last\n*** End of File\nnope\n*** End Patch", "Expected update hunk to start with a @@ context marker"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Parse(testCase.patch)
			if err == nil {
				t.Fatalf("Parse accepted %q", testCase.patch)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want %q", err.Error(), testCase.want)
			}
		})
	}
}

func TestApplyAddDeleteUpdateAndMove(t *testing.T) {
	filesystem := newMemFS(map[string]string{
		"delete.txt": "gone\n",
		"update.txt": "def f():\n    pass\n",
		"move.txt":   "old name\n",
		"exists.txt": "overwritten\n",
		"keep.txt":   "untouched\n",
	})
	hunks := parseHunks(t, wrapPatch(`*** Add File: nested/dir/new.txt
+hello
*** Add File: exists.txt
+replaced
*** Delete File: delete.txt
*** Update File: update.txt
@@ def f():
-    pass
+    return 123
*** Update File: move.txt
*** Move to: renamed.txt`))

	result, err := Apply(hunks, filesystem)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if filesystem.files["nested/dir/new.txt"] != "hello\n" {
		t.Fatalf("added file = %q", filesystem.files["nested/dir/new.txt"])
	}
	if !filesystem.dirs["nested/dir"] {
		t.Fatalf("parent directories were not created: %#v", filesystem.dirs)
	}
	// The reference tool overwrites an existing file on add.
	if filesystem.files["exists.txt"] != "replaced\n" {
		t.Fatalf("existing file = %q", filesystem.files["exists.txt"])
	}
	if _, ok := filesystem.files["delete.txt"]; ok {
		t.Fatal("delete did not remove the file")
	}
	if filesystem.files["update.txt"] != "def f():\n    return 123\n" {
		t.Fatalf("updated file = %q", filesystem.files["update.txt"])
	}
	if _, ok := filesystem.files["move.txt"]; ok {
		t.Fatal("move did not remove the original")
	}
	if filesystem.files["renamed.txt"] != "old name\n" {
		t.Fatalf("moved file = %q", filesystem.files["renamed.txt"])
	}
	if filesystem.files["keep.txt"] != "untouched\n" {
		t.Fatal("an unrelated file changed")
	}

	sort.Strings(result.Added)
	if strings.Join(result.Added, ",") != "exists.txt,nested/dir/new.txt" {
		t.Fatalf("added = %#v", result.Added)
	}
	if strings.Join(result.Modified, ",") != "update.txt,move.txt" {
		t.Fatalf("modified = %#v", result.Modified)
	}
	if strings.Join(result.Deleted, ",") != "delete.txt" {
		t.Fatalf("deleted = %#v", result.Deleted)
	}

	summary := result.Summary()
	for _, want := range []string{
		"Success. Updated the following files:",
		"A exists.txt",
		"A nested/dir/new.txt",
		"M update.txt",
		"M move.txt",
		"D delete.txt",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q does not contain %q", summary, want)
		}
	}
}

func TestApplyMatchesContextLoosely(t *testing.T) {
	cases := []struct {
		name    string
		content string
		patch   string
		want    string
	}{
		{
			name:    "trailing whitespace",
			content: "foo   \nbar\t\t\nbaz\n",
			patch:   "*** Update File: f.txt\n@@\n foo\n-bar\n+bar2\n",
			// The matched region is written back from the patch text, so
			// the loose match also normalizes the whitespace it ignored.
			want: "foo\nbar2\nbaz\n",
		},
		{
			name:    "surrounding whitespace",
			content: "    foo   \n   bar\t\n",
			patch:   "*** Update File: f.txt\n@@\n foo\n-bar\n+bar2\n",
			want:    "foo\nbar2\n",
		},
		{
			name:    "typographic punctuation",
			content: "if a \u2014 b:\n    pass\n",
			patch:   "*** Update File: f.txt\n@@\n-if a - b:\n+if a - c:\n",
			want:    "if a - c:\n    pass\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			filesystem := newMemFS(map[string]string{"f.txt": testCase.content})
			hunks := parseHunks(t, "*** Begin Patch\n"+testCase.patch+"*** End Patch")
			if _, err := Apply(hunks, filesystem); err != nil {
				t.Fatalf("Apply returned error: %v", err)
			}
			if filesystem.files["f.txt"] != testCase.want {
				t.Fatalf("content = %q, want %q", filesystem.files["f.txt"], testCase.want)
			}
		})
	}
}

func TestApplyReportsMissingContextAndFiles(t *testing.T) {
	filesystem := newMemFS(map[string]string{"f.txt": "present\n"})

	hunks, err := Parse(wrapPatch(`*** Update File: f.txt
@@
-missing
+other`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, err := Apply(hunks, filesystem); err == nil ||
		!strings.Contains(err.Error(), "Failed to find expected lines in f.txt") {
		t.Fatalf("missing context error = %v", err)
	}

	hunks, err = Parse(wrapPatch(`*** Update File: f.txt
@@ missing context
+added`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, err := Apply(hunks, filesystem); err == nil ||
		!strings.Contains(err.Error(), "Failed to find context 'missing context' in f.txt") {
		t.Fatalf("missing context marker error = %v", err)
	}

	hunks, err = Parse(wrapPatch("*** Update File: absent.txt\n@@\n-old\n+new"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, err := Apply(hunks, filesystem); err == nil ||
		!strings.Contains(err.Error(), "Failed to read file to update absent.txt") {
		t.Fatalf("missing file error = %v", err)
	}

	hunks, err = Parse(wrapPatch("*** Delete File: absent.txt"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, err := Apply(hunks, filesystem); err == nil ||
		!strings.Contains(err.Error(), "Failed to delete file absent.txt") {
		t.Fatalf("missing delete error = %v", err)
	}

	if _, err := Apply(nil, filesystem); err == nil || !strings.Contains(err.Error(), "No files were modified.") {
		t.Fatalf("empty patch error = %v", err)
	}
}

func TestParseErrorCarriesTheLineNumber(t *testing.T) {
	_, err := Parse("*** Begin Patch\n*** Update File: a.txt\n@@\n+ok\n?bad\n*** End Patch")
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error %v is not a *ParseError", err)
	}
	if parseErr.Line != 5 {
		t.Fatalf("line = %d, want 5", parseErr.Line)
	}
	if !strings.HasPrefix(parseErr.Error(), "invalid hunk at line 5, ") {
		t.Fatalf("error = %q", parseErr.Error())
	}
	// The patch-boundary failure carries no line number.
	_, err = Parse("nope")
	if !errors.As(err, &parseErr) || parseErr.Line != 0 {
		t.Fatalf("boundary error = %#v", err)
	}
	if !strings.HasPrefix(parseErr.Error(), "invalid patch: ") {
		t.Fatalf("boundary error text = %q", parseErr.Error())
	}
}

func TestApplyEndOfFileInsertion(t *testing.T) {
	filesystem := newMemFS(map[string]string{"f.txt": "first\nlast\n"})
	hunks := parseHunks(t, wrapPatch(`*** Update File: f.txt
@@
 last
+appended
*** End of File`))
	if _, err := Apply(hunks, filesystem); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if filesystem.files["f.txt"] != "first\nlast\nappended\n" {
		t.Fatalf("content = %q", filesystem.files["f.txt"])
	}
}

func TestApplyMultipleChunksAdvanceTheCursor(t *testing.T) {
	filesystem := newMemFS(map[string]string{
		"f.txt": "one\ntwo\nthree\nfour\n",
	})
	hunks := parseHunks(t, wrapPatch(`*** Update File: f.txt
@@
-one
+ONE
@@
-three
+THREE`))
	if _, err := Apply(hunks, filesystem); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if filesystem.files["f.txt"] != "ONE\ntwo\nTHREE\nfour\n" {
		t.Fatalf("content = %q", filesystem.files["f.txt"])
	}
}

func TestLocalFSRoundTrip(t *testing.T) {
	dir := t.TempDir()
	target := path.Join(dir, "nested", "file.txt")

	hunks, err := Parse(wrapPatch("*** Add File: " + target + "\n+hello"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	result, err := Apply(hunks, LocalFS{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.FirstPath() != target {
		t.Fatalf("FirstPath = %q", result.FirstPath())
	}
	data, err := LocalFS{}.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("content = %q", string(data))
	}
	_, err = Apply([]Hunk{{Kind: "delete", Path: target}}, LocalFS{})
	if err != nil {
		t.Fatalf("delete returned error: %v", err)
	}
	_, err = LocalFS{}.ReadFile(target)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file survived deletion: %v", err)
	}
}

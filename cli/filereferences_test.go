package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshapi"
)

// writeFileReferenceTree builds a workspace with the shapes the ranking cares
// about: a nested directory, a hidden entry, an excluded directory, a symlink, and
// names that exercise every scoring band.
func writeFileReferenceTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"README.md",
		"main.go",
		"detail.go",
		"internal/dshapi/feedback.go",
		"internal/dshapi/feedback_test.go",
		"internal/dshapi/glob.go",
		"docs/guide.md",
		"docs/main-notes.md",
		".github/workflows/ci.yml",
		"node_modules/ignored/index.js",
		".hidden/secret.txt",
	}
	for _, name := range files {
		absolute := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "docs"), filepath.Join(root, "linkdocs")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	return root
}

func referencePaths(candidates []dshapi.FileReference) []string {
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.Path+"|"+candidate.Kind)
	}
	return paths
}

func candidatesFor(t *testing.T, source dshapi.FileReferenceSource, query string) []dshapi.FileReference {
	t.Helper()
	candidates, err := source.FileReferenceCandidates(context.Background(), "run-1", query)
	if err != nil {
		t.Fatalf("candidates(%q): %v", query, err)
	}
	return candidates
}

// The empty query lists the root's immediate entries, directories first, and the
// excluded and hidden ones are not among them.
func TestFileReferencesListTheRootDirectory(t *testing.T) {
	root := writeFileReferenceTree(t)
	source := consoleFileReferences(root)
	if source == nil {
		t.Fatal("consoleFileReferences returned no source for a readable directory")
	}
	candidates := candidatesFor(t, source, "")
	got := referencePaths(candidates)
	if !containsAll(got, []string{"docs|directory", "internal|directory", "main.go|file", "README.md|file"}) {
		t.Fatalf("candidates = %v, want the root's own entries", got)
	}
	for _, excluded := range []string{"node_modules|directory", ".github|directory", ".hidden|directory", "linkdocs|directory"} {
		if containsString(got, excluded) {
			t.Fatalf("candidates = %v, want no %s", got, excluded)
		}
	}
	if candidates[0].Kind != "directory" {
		t.Fatalf("first candidate = %+v, want a directory leading for an empty query", candidates[0])
	}
}

// A query with a slash lists that directory and ranks by the fragment, so typing
// `internal/dsh` narrows to the rows under `internal/`.
func TestFileReferencesListADirectoryWithAFragment(t *testing.T) {
	root := writeFileReferenceTree(t)
	source := consoleFileReferences(root)
	// `internal/dsh` has no trailing slash, so it lists `internal/` and ranks by the
	// fragment: the one directory whose name starts with it.
	if got := referencePaths(candidatesFor(t, source, "internal/dsh")); len(got) != 1 || got[0] != "internal/dshapi|directory" {
		t.Fatalf("candidates = %v, want the one directory named dshapi", got)
	}
	candidates := candidatesFor(t, source, "internal/dshapi/feedback")
	got := referencePaths(candidates)
	if len(got) != 2 || got[0] != "internal/dshapi/feedback.go|file" {
		t.Fatalf("candidates = %v, want the two feedback files with the exact prefix first", got)
	}
	if got[1] != "internal/dshapi/feedback_test.go|file" {
		t.Fatalf("candidates = %v, want the source file ranked above its test", got)
	}
}

// A bare query searches the whole tree: the name that matches exactly wins, and a
// path whose name merely contains the needle still appears.
func TestFileReferencesSearchRanksTheWholeTree(t *testing.T) {
	root := writeFileReferenceTree(t)
	source := consoleFileReferences(root)
	got := referencePaths(candidatesFor(t, source, "main"))
	if len(got) == 0 || got[0] != "main.go|file" {
		t.Fatalf("candidates = %v, want the root's main.go first", got)
	}
	if !containsString(got, "docs/main-notes.md|file") {
		t.Fatalf("candidates = %v, want the deeper prefix match too", got)
	}

	// A query that matches a directory's name outranks the files under it, which is
	// the directory bonus doing its job.
	got = referencePaths(candidatesFor(t, source, "dshapi"))
	if len(got) == 0 || got[0] != "internal/dshapi|directory" {
		t.Fatalf("candidates = %v, want the directory first", got)
	}
	if !containsString(got, "internal/dshapi/feedback.go|file") {
		t.Fatalf("candidates = %v, want the path-substring band too", got)
	}

	got = referencePaths(candidatesFor(t, source, "feedback"))
	if len(got) != 2 || got[0] != "internal/dshapi/feedback.go|file" {
		t.Fatalf("candidates = %v, want the two feedback files ranked by name", got)
	}

	// A fuzzy query reaches a file whose name is typed as initials.
	got = referencePaths(candidatesFor(t, source, "fg"))
	if !containsString(got, "internal/dshapi/feedback.go|file") {
		t.Fatalf("candidates = %v, want a subsequence match", got)
	}

	// Nothing matches: an empty answer, not an error.
	if got := candidatesFor(t, source, "no-such-file-anywhere"); len(got) != 0 {
		t.Fatalf("candidates = %v, want none", got)
	}
}

// Hidden entries are reachable only by asking for them, and a query cannot leave
// the workspace or traverse a symlink.
func TestFileReferencesRefuseToLeaveTheWorkspace(t *testing.T) {
	root := writeFileReferenceTree(t)
	source := consoleFileReferences(root)

	if got := referencePaths(candidatesFor(t, source, ".github/")); !containsString(got, ".github/workflows|directory") {
		t.Fatalf("candidates = %v, want the hidden directory when the query names it", got)
	}
	if got := referencePaths(candidatesFor(t, source, ".github/workflows/")); !containsString(got, ".github/workflows/ci.yml|file") {
		t.Fatalf("candidates = %v, want the hidden directory's own entries", got)
	}
	if got := referencePaths(candidatesFor(t, source, "../")); len(got) != 0 {
		t.Fatalf("candidates = %v, want nothing above the workspace", got)
	}
	if got := referencePaths(candidatesFor(t, source, "docs/../../")); len(got) != 0 {
		t.Fatalf("candidates = %v, want nothing for an escaping path", got)
	}
	// The symlinked directory is neither listed nor followed.
	if got := referencePaths(candidatesFor(t, source, "")); containsString(got, "linkdocs|directory") {
		t.Fatalf("candidates = %v, want the symlink left out", got)
	}
	if got := referencePaths(candidatesFor(t, source, "linkdocs/")); len(got) != 0 {
		t.Fatalf("candidates = %v, want no traversal through a symlink", got)
	}
	// An absent directory is an empty answer.
	if got := referencePaths(candidatesFor(t, source, "nope/nothing")); len(got) != 0 {
		t.Fatalf("candidates = %v, want none for an absent directory", got)
	}
}

// The exclusion list, the hidden rule and the subsequence band are the reference
// provider's, so the ported helpers are checked directly too.
func TestFileReferenceRankingMatchesTheReference(t *testing.T) {
	candidates := []dshapi.FileReference{
		{Path: "docs/guide.md", Kind: "file"},
		{Path: "guide.md", Kind: "file"},
		{Path: "docs/gu", Kind: "directory"},
	}
	got := rankFileReferences(candidates, "guide")
	if len(got) != 2 || got[0].Path != "guide.md" {
		t.Fatalf("ranked = %v, want the exact name first", referencePaths(got))
	}
	// A directory outranks a file at the same band: the 25-point bonus.
	got = rankFileReferences([]dshapi.FileReference{
		{Path: "guide", Kind: "file"},
		{Path: "guide", Kind: "directory"},
	}, "guide")
	if len(got) != 2 || got[0].Kind != "directory" {
		t.Fatalf("ranked = %v, want the directory first at an equal match", referencePaths(got))
	}
	if score, ok := scoreFileReference(dshapi.FileReference{Path: "docs/github", Kind: "file"}, "github"); !ok || score != 1000 {
		t.Fatalf("score = %d (%v), want the exact-name band", score, ok)
	}
	if score, ok := scoreFileReference(dshapi.FileReference{Path: "docs/github.md", Kind: "file"}, "github"); !ok || score != 900 {
		t.Fatalf("score = %d (%v), want the name-prefix band", score, ok)
	}
	if _, ok := scoreFileReference(dshapi.FileReference{Path: "docs/github.md", Kind: "file"}, "zzz"); ok {
		t.Fatal("a query no subsequence matches must not score")
	}
	if visibleForGlobalQuery("docs/.hidden/file.md", "file") {
		t.Fatal("a hidden segment must be invisible to a bare query")
	}
	if !visibleForGlobalQuery("docs/.hidden/file.md", ".hidden") {
		t.Fatal("a query naming the dot must see hidden entries")
	}
	if len(rankFileReferences(candidates, "")) > fileReferenceMaxResults {
		t.Fatal("the answer must be bounded by maxResults")
	}
	// A walk is bounded by the entry budget, exactly like the reference's index.
	many := make([]dshapi.FileReference, 0, fileReferenceMaxEntries)
	for index := 0; index < fileReferenceMaxEntries+10; index++ {
		many = append(many, dshapi.FileReference{Path: "p", Kind: "file"})
	}
	if len(rankFileReferences(many, "")) != fileReferenceMaxResults {
		t.Fatal("a huge candidate set must still answer maxResults rows")
	}
}

// A workspace that cannot be opened leaves the namespace unanswered, which is the
// honest answer rather than an empty menu.
func TestConsoleFileReferencesNeedsAReadableWorkspace(t *testing.T) {
	if source := consoleFileReferences(filepath.Join(t.TempDir(), "absent")); source != nil {
		t.Fatalf("source = %v, want nil for an absent directory", source)
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if source := consoleFileReferences(file); source != nil {
		t.Fatalf("source = %v, want nil for a path that is not a directory", source)
	}
}

func containsAll(values, wanted []string) bool {
	for _, want := range wanted {
		if !containsString(values, want) {
			return false
		}
	}
	return true
}

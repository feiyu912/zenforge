package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscoverRootFirstOrderAndFirstCandidateWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, "sub", "AGENTS.md"), "sub rules")
	writeFile(t, filepath.Join(root, "sub", "ZENFORGE.md"), "compat rules")

	loaded, err := Discover(filepath.Join(root, "sub"), Config{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if loaded.ProjectRoot != root {
		t.Fatalf("ProjectRoot = %q, want %q", loaded.ProjectRoot, root)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %+v, want root and sub", loaded.Entries)
	}
	if loaded.Entries[0].Content != "root rules" || loaded.Entries[1].Content != "sub rules" {
		t.Fatalf("unexpected order or precedence: %+v", loaded.Entries)
	}
	if loaded.Entries[0].Scope != "project" {
		t.Fatalf("unexpected scope: %+v", loaded.Entries[0])
	}
}

func TestDiscoverOverrideFileWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "standard")
	writeFile(t, filepath.Join(root, "AGENTS.override.md"), "override")

	loaded, err := Discover(root, Config{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].Content != "override" {
		t.Fatalf("override precedence broken: %+v", loaded.Entries)
	}
}

func TestDiscoverGlobalFileIsBroadestScope(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(t.TempDir(), "AGENTS.md")
	writeFile(t, global, "global rules")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "project rules")

	loaded, err := Discover(root, Config{GlobalPath: global})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(loaded.Entries) != 2 || loaded.Entries[0].Scope != "global" || loaded.Entries[0].Content != "global rules" {
		t.Fatalf("global entry misplaced: %+v", loaded.Entries)
	}
	rendered := loaded.Render()
	if !strings.Contains(rendered, "(user-global)") {
		t.Fatalf("render lacks global marker: %s", rendered)
	}
}

func TestDiscoverMissingGlobalFileIsSilent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "project rules")
	loaded, err := Discover(root, Config{GlobalPath: filepath.Join(root, "absent.md")})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(loaded.Entries) != 1 || len(loaded.Warnings) != 0 {
		t.Fatalf("missing global file should be silent: %+v", loaded)
	}
}

func TestDiscoverBudgetDropsBroaderBeforeTruncatingSpecific(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref")
	writeFile(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("b", 100))
	writeFile(t, filepath.Join(root, "mid", "AGENTS.md"), strings.Repeat("m", 100))
	writeFile(t, filepath.Join(root, "mid", "deep", "AGENTS.md"), strings.Repeat("d", 60))

	loaded, err := Discover(filepath.Join(root, "mid", "deep"), Config{MaxBytes: 180})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	// Deepest (60) and middle (100) fit; the broadest file no longer fits
	// and is dropped whole rather than shown as a fragment.
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %+v, want middle + deep", loaded.Entries)
	}
	if loaded.Entries[0].Content != strings.Repeat("m", 100) || loaded.Entries[1].Content != strings.Repeat("d", 60) {
		t.Fatalf("unexpected kept entries: %+v", loaded.Entries)
	}
	if loaded.Entries[0].Truncated || loaded.Entries[1].Truncated {
		t.Fatalf("kept entries should not be truncated: %+v", loaded.Entries)
	}
	joined := strings.Join(loaded.Warnings, "\n")
	if !strings.Contains(joined, "omitted") || !strings.Contains(joined, filepath.Join(root, "AGENTS.md")) {
		t.Fatalf("omission warning missing: %v", loaded.Warnings)
	}
	if strings.Contains(joined, "truncated") {
		t.Fatalf("unexpected truncation warning: %v", loaded.Warnings)
	}
}

func TestDiscoverTruncatesOnRuneBoundary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "日本語テキスト")
	loaded, err := Discover(root, Config{MaxBytes: 7})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(loaded.Entries) != 1 {
		t.Fatalf("entries = %+v", loaded.Entries)
	}
	content := loaded.Entries[0].Content
	if len(content) != 6 || content != "日本" {
		t.Fatalf("truncation split a rune: %q (%d bytes)", content, len(content))
	}
}

func TestDiscoverMaxSourceBytesSkipsLargeFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("x", 500))
	loaded, err := Discover(root, Config{MaxSourceBytes: 100})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if !loaded.Empty() {
		t.Fatalf("oversized file was kept: %+v", loaded.Entries)
	}
	if len(loaded.Warnings) == 0 || !strings.Contains(loaded.Warnings[0], "maxSourceBytes") {
		t.Fatalf("oversize warning missing: %v", loaded.Warnings)
	}
}

func TestDiscoverWithoutMarkerSearchesWorkingDirOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, "sub", "AGENTS.md"), "sub rules")

	loaded, err := Discover(filepath.Join(root, "sub"), Config{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].Content != "sub rules" {
		t.Fatalf("traversal without marker should stay in cwd: %+v", loaded.Entries)
	}
	if loaded.ProjectRoot != filepath.Join(root, "sub") {
		t.Fatalf("ProjectRoot = %q, want cwd", loaded.ProjectRoot)
	}
}

func TestDiscoverEmptyRootMarkersDisableTraversal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, "sub", "AGENTS.md"), "sub rules")

	loaded, err := Discover(filepath.Join(root, "sub"), Config{RootMarkers: []string{}})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].Content != "sub rules" {
		t.Fatalf("empty markers should disable traversal: %+v", loaded.Entries)
	}
}

func TestDiscoverRejectsPathSyntaxInNames(t *testing.T) {
	root := t.TempDir()
	if _, err := Discover(root, Config{FileNames: []string{"../evil.md"}}); err == nil {
		t.Fatalf("path traversal filename accepted")
	}
	if _, err := Discover(root, Config{FileNames: []string{"sub/dir.md"}}); err == nil {
		t.Fatalf("slash filename accepted")
	}
	if _, err := Discover(root, Config{RootMarkers: []string{".."}}); err == nil {
		t.Fatalf("parent marker accepted")
	}
	if _, err := Discover(root, Config{FileNames: []string{""}}); err == nil {
		t.Fatalf("empty filename accepted")
	}
}

func TestDiscoverRejectsMissingDir(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "absent"), Config{}); err == nil {
		t.Fatalf("missing directory accepted")
	}
}

func TestDiscoverCustomMarkersAndNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".project", "marker"), "")
	writeFile(t, filepath.Join(root, "RULES.md"), "custom root")
	writeFile(t, filepath.Join(root, "deep", "RULES.md"), "custom deep")

	loaded, err := Discover(filepath.Join(root, "deep"), Config{
		RootMarkers: []string{".project"},
		FileNames:   []string{"RULES.md"},
	})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if loaded.ProjectRoot != root || len(loaded.Entries) != 2 {
		t.Fatalf("custom discovery failed: root=%q entries=%+v", loaded.ProjectRoot, loaded.Entries)
	}
}

func TestRenderStatesPrecedenceContract(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "be careful")
	loaded, err := Discover(root, Config{})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	rendered := loaded.Render()
	for _, want := range []string{
		"# Project instructions",
		"More specific instructions take precedence over broader ones",
		"do not override system, developer, or direct user instructions",
		"## " + filepath.Join(root, "AGENTS.md"),
		"be careful",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("render missing %q:\n%s", want, rendered)
		}
	}
	if (Loaded{}).Render() != "" {
		t.Fatalf("empty discovery rendered content")
	}
}

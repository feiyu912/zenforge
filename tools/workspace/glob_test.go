package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/workspace/local"
)

type fakeSearchSpill struct {
	name    string
	content string
	err     error
}

func (f *fakeSearchSpill) SaveText(suggestedName, content string) (string, error) {
	f.name = suggestedName
	f.content = content
	if f.err != nil {
		return "", f.err
	}
	return filepath.Join("spill", suggestedName), nil
}

// seedGlobTree creates a nested tree with strictly increasing mtimes so
// modification-time order is deterministic: older files first on disk,
// newest file seeded last.
func seedGlobTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	files := []string{
		".git/internal.go",
		"src/deep/d.txt",
		"src/deep/c.go",
		".hidden/e.go",
		"src/b.go",
		"a.go",
	}
	for i, name := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	return root
}

func callGlob(t *testing.T, globTool tool.Tool, args string) globOutput {
	t.Helper()
	result, err := globTool.Call(context.Background(), json.RawMessage(args), tool.Context{})
	if err != nil {
		t.Fatalf("glob Call returned error: %v (args %s)", err, args)
	}
	var out globOutput
	encoded, err := json.Marshal(result.Structured)
	if err != nil {
		t.Fatalf("marshal structured: %v", err)
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal glob output: %v (%#v)", err, result.Structured)
	}
	return out
}

func TestGlobMatchesDoublestarAndBasenamePatterns(t *testing.T) {
	root := seedGlobTree(t)
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	globTool, err := Glob(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}

	cases := []struct {
		pattern string
		want    []string
	}{
		// Newest first: a.go, src/b.go, .hidden/e.go, src/deep/c.go.
		{"**/*.go", []string{"a.go", "src/b.go", ".hidden/e.go", "src/deep/c.go"}},
		{"*.go", []string{"a.go", "src/b.go", ".hidden/e.go", "src/deep/c.go"}},
		{"src/**/*.go", []string{"src/b.go", "src/deep/c.go"}},
		{"**/*.txt", []string{"src/deep/d.txt"}},
		{"src/deep/*", []string{"src/deep/c.go", "src/deep/d.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			out := callGlob(t, globTool, fmt.Sprintf(`{"pattern":%q}`, tc.pattern))
			if strings.Join(out.Paths, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("paths = %v, want %v (total %d, footer %q)", out.Paths, tc.want, out.Total, out.Footer)
			}
			if out.Total != len(tc.want) {
				t.Fatalf("total = %d, want %d", out.Total, len(tc.want))
			}
		})
	}

	// VCS directories are never descended into.
	out := callGlob(t, globTool, `{"pattern":"**/*"}`)
	for _, path := range out.Paths {
		if strings.HasPrefix(path, ".git/") {
			t.Fatalf("glob descended into .git: %v", out.Paths)
		}
	}
}

func TestGlobScopedToPathArgument(t *testing.T) {
	root := seedGlobTree(t)
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	globTool, err := Glob(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	out := callGlob(t, globTool, `{"pattern":"**/*.go","path":"src"}`)
	if strings.Join(out.Paths, ",") != "src/b.go,src/deep/c.go" {
		t.Fatalf("scoped paths = %v", out.Paths)
	}
}

func TestGlobRejectsInvalidPatterns(t *testing.T) {
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	globTool, err := Glob(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	for _, args := range []string{
		`{"pattern":""}`,
		`{"pattern":"../escape.go"}`,
		`{"pattern":"/abs/path.go"}`,
		`{"pattern":"a/./b.go"}`,
		`{"pattern":"[bad.go"}`,
	} {
		_, err := globTool.Call(context.Background(), json.RawMessage(args), tool.Context{})
		if !errors.Is(err, tool.ErrInvalidArguments) {
			t.Fatalf("Call(%s) error = %v, want ErrInvalidArguments", args, err)
		}
	}
}

func TestGlobCapsResultsAndSpillsCompleteList(t *testing.T) {
	root := seedGlobTree(t)
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	spill := &fakeSearchSpill{}
	globTool, err := Glob(Config{Workspace: ws, GlobMaxResults: 2, SearchSpill: spill})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	out := callGlob(t, globTool, `{"pattern":"**/*.go"}`)
	if len(out.Paths) != 2 || out.Paths[0] != "a.go" {
		t.Fatalf("capped paths = %v, want newest 2", out.Paths)
	}
	if out.Total != 4 {
		t.Fatalf("total = %d, want 4", out.Total)
	}
	if !strings.Contains(out.Footer, "modification-time order") || !strings.Contains(out.Footer, "complete sorted list was saved to") {
		t.Fatalf("footer missing spill pointer: %q", out.Footer)
	}
	lines := strings.Split(strings.TrimSpace(spill.content), "\n")
	if len(lines) != 4 || lines[0] != "a.go" || lines[3] != "src/deep/c.go" {
		t.Fatalf("spilled list = %v, want all 4 newest-first", lines)
	}
	if !strings.HasPrefix(spill.name, "glob-") || !strings.HasSuffix(spill.name, ".txt") {
		t.Fatalf("spill name = %q", spill.name)
	}

	// Without a spill store the footer degrades to a plain omission note.
	plainTool, err := Glob(Config{Workspace: ws, GlobMaxResults: 2})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	out = callGlob(t, plainTool, `{"pattern":"**/*.go"}`)
	if !strings.Contains(out.Footer, "2 more omitted") || strings.Contains(out.Footer, "saved to") {
		t.Fatalf("plain footer wrong: %q", out.Footer)
	}

	// A failing spill store also degrades instead of failing the call.
	broken := &fakeSearchSpill{err: errors.New("disk full")}
	brokenTool, err := Glob(Config{Workspace: ws, GlobMaxResults: 2, SearchSpill: broken})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	out = callGlob(t, brokenTool, `{"pattern":"**/*.go"}`)
	if !strings.Contains(out.Footer, "2 more omitted") {
		t.Fatalf("broken-spill footer wrong: %q", out.Footer)
	}
}

func TestGlobVisitBudgetStopsWalk(t *testing.T) {
	root := seedGlobTree(t)
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	globTool, err := Glob(Config{Workspace: ws, GlobMaxVisited: 3})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	out := callGlob(t, globTool, `{"pattern":"**/*"}`)
	if out.Total > 3 {
		t.Fatalf("visit budget exceeded: total %d", out.Total)
	}
}

func TestGrepCapsMatchesAndPreviewsLines(t *testing.T) {
	root := t.TempDir()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("needle line %d", i))
	}
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("seed many.txt: %v", err)
	}
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	grepTool, err := Grep(Config{Workspace: ws, GrepMaxMatches: 3})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}
	result, err := grepTool.Call(context.Background(), json.RawMessage(`{"pattern":"needle","path":"."}`), tool.Context{})
	if err != nil {
		t.Fatalf("grep Call returned error: %v", err)
	}
	var out grepOutput
	encoded, _ := json.Marshal(result.Structured)
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal grep output: %v", err)
	}
	if len(out.Matches) != 3 || !out.Capped {
		t.Fatalf("matches = %d capped = %v, want 3 true", len(out.Matches), out.Capped)
	}
	if !strings.Contains(out.Footer, "Results limited to 3 matches") {
		t.Fatalf("footer = %q", out.Footer)
	}

	// A smaller model-supplied maxMatches wins over the config cap.
	result, err = grepTool.Call(context.Background(), json.RawMessage(`{"pattern":"needle","path":".","maxMatches":2}`), tool.Context{})
	if err != nil {
		t.Fatalf("grep Call returned error: %v", err)
	}
	encoded, _ = json.Marshal(result.Structured)
	out = grepOutput{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal grep output: %v", err)
	}
	if len(out.Matches) != 2 || !out.Capped {
		t.Fatalf("model cap not applied: %d matches capped=%v", len(out.Matches), out.Capped)
	}

	// Long matched lines are previewed on rune boundaries.
	if err := os.WriteFile(filepath.Join(root, "wide.txt"), []byte(strings.Repeat("日本語", 200)), 0o644); err != nil {
		t.Fatalf("seed wide.txt: %v", err)
	}
	previewTool, err := Grep(Config{Workspace: ws, GrepMaxLineBytes: 10})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}
	result, err = previewTool.Call(context.Background(), json.RawMessage(`{"pattern":"日","path":"wide.txt"}`), tool.Context{})
	if err != nil {
		t.Fatalf("preview grep Call returned error: %v", err)
	}
	encoded, _ = json.Marshal(result.Structured)
	out = grepOutput{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal grep output: %v", err)
	}
	if len(out.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(out.Matches))
	}
	text := out.Matches[0].Text
	if len(text) != 9 || !utf8.ValidString(text) || text != "日本語" {
		t.Fatalf("preview not rune-safe at cap: %q (%d bytes)", text, len(text))
	}
}

func TestSearchDefaultsMatchDSH(t *testing.T) {
	if DefaultGlobMaxResults != 100 || DefaultGrepMaxMatches != 250 || DefaultGrepMaxLineBytes != 2000 {
		t.Fatalf("search defaults drifted from DSH: glob=%d grep=%d line=%d",
			DefaultGlobMaxResults, DefaultGrepMaxMatches, DefaultGrepMaxLineBytes)
	}
}

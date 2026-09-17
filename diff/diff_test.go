package diff

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnifiedNoChange(t *testing.T) {
	if out := Unified("a\nb\n", "a\nb\n", "a/f.txt", "b/f.txt"); out != "" {
		t.Fatalf("identical texts produced diff %q", out)
	}
	if out := Unified("", "", "a/f.txt", "b/f.txt"); out != "" {
		t.Fatalf("empty texts produced diff %q", out)
	}
}

func TestUnifiedSingleLineChange(t *testing.T) {
	out := Unified("a\nb\nc\n", "a\nB\nc\n", "a/f.txt", "b/f.txt")
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"
	if out != want {
		t.Fatalf("diff =\n%q\nwant\n%q", out, want)
	}
}

func TestUnifiedCreateAndDelete(t *testing.T) {
	created := Unified("", "x\ny\n", "a/f.txt", "b/f.txt")
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -0,0 +1,2 @@\n+x\n+y\n"
	if created != want {
		t.Fatalf("create diff =\n%q\nwant\n%q", created, want)
	}
	deleted := Unified("x\ny\n", "", "a/f.txt", "b/f.txt")
	want = "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +0,0 @@\n-x\n-y\n"
	if deleted != want {
		t.Fatalf("delete diff =\n%q\nwant\n%q", deleted, want)
	}
}

func TestUnifiedInsertAtEnd(t *testing.T) {
	out := Unified("a\n", "a\nb\n", "a/f.txt", "b/f.txt")
	// The renderer always spells out both hunk counts.
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -1,1 +1,2 @@\n a\n+b\n"
	if out != want {
		t.Fatalf("diff =\n%q\nwant\n%q", out, want)
	}
}

func TestUnifiedTwoDistantChangesProduceTwoHunks(t *testing.T) {
	var oldLines, newLines []string
	for i := 1; i <= 20; i++ {
		oldLines = append(oldLines, fmt.Sprintf("line%d", i))
		newLines = append(newLines, fmt.Sprintf("line%d", i))
	}
	newLines[0] = "LINE1"
	newLines[19] = "LINE20"
	out := Unified(strings.Join(oldLines, "\n")+"\n", strings.Join(newLines, "\n")+"\n", "a/f", "b/f")
	if n := countHunks(out); n != 2 {
		t.Fatalf("want 2 hunks, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "-line1\n+LINE1") || !strings.Contains(out, "-line20\n+LINE20") {
		t.Fatalf("hunks missing changes:\n%s", out)
	}
}

func TestUnifiedCoarseFallbackAboveBudget(t *testing.T) {
	var oldLines, newLines []string
	for i := 0; i < MaxDiffLines; i++ {
		oldLines = append(oldLines, fmt.Sprintf("l%d", i))
		newLines = append(newLines, fmt.Sprintf("l%d", i))
	}
	newLines[MaxDiffLines/2] = "CHANGED"
	out := Unified(strings.Join(oldLines, "\n"), strings.Join(newLines, "\n"), "a/f", "b/f")
	if !strings.Contains(out, "-l2000\n+CHANGED") {
		t.Fatalf("coarse diff missing change:\n%.200s", out)
	}
	if n := countHunks(out); n != 1 {
		t.Fatalf("coarse fallback should render one hunk, got %d:\n%.400s", n, out)
	}
}

func countHunks(out string) int {
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			count++
		}
	}
	return count
}

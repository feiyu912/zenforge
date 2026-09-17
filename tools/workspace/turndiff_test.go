package workspace

import (
	"strings"
	"testing"
	"time"
)

func findDiff(diffs []TurnFileDiff, path string) (TurnFileDiff, bool) {
	for _, entry := range diffs {
		if entry.Path == path {
			return entry, true
		}
	}
	return TurnFileDiff{}, false
}

func TestTurnDiffStoreRecordsAndDrains(t *testing.T) {
	store := NewTurnDiffStore()
	store.RecordChange("run-1", "f.txt", "", false, "hello\n")
	store.RecordChange("run-1", "g.txt", "old\n", true, "new\n")
	store.RecordChange("run-2", "other.txt", "", false, "x\n")

	diffs := store.Drain("run-1", 0)
	if len(diffs) != 2 {
		t.Fatalf("diffs = %+v, want 2 entries", diffs)
	}
	created, ok := findDiff(diffs, "f.txt")
	if !ok || !strings.Contains(created.Diff, "+hello") {
		t.Fatalf("created diff = %+v", created)
	}
	if !strings.Contains(created.Diff, "@@ -0,0 +1,1 @@") {
		t.Fatalf("created hunk header = %q", created.Diff)
	}
	modified, ok := findDiff(diffs, "g.txt")
	if !ok || !strings.Contains(modified.Diff, "-old") || !strings.Contains(modified.Diff, "+new") {
		t.Fatalf("modified diff = %+v", modified)
	}

	// The drain cleared run-1 but left run-2 alone.
	if again := store.Drain("run-1", 0); len(again) != 0 {
		t.Fatalf("second drain = %+v, want empty", again)
	}
	if other := store.Drain("run-2", 0); len(other) != 1 || other[0].Path != "other.txt" {
		t.Fatalf("run-2 drain = %+v", other)
	}
}

func TestTurnDiffStoreKeepsFirstOriginalWithinWindow(t *testing.T) {
	store := NewTurnDiffStore()
	store.RecordChange("run", "f.txt", "v1\n", true, "v2\n")
	store.RecordChange("run", "f.txt", "v2\n", true, "v3\n")
	diffs := store.Drain("run", 0)
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want single entry", diffs)
	}
	if !strings.Contains(diffs[0].Diff, "-v1") || !strings.Contains(diffs[0].Diff, "+v3") {
		t.Fatalf("multi-edit turn did not diff against window start: %q", diffs[0].Diff)
	}
}

func TestTurnDiffStoreOmitsUnchangedFiles(t *testing.T) {
	store := NewTurnDiffStore()
	store.RecordChange("run", "f.txt", "same\n", true, "same\n")
	if diffs := store.Drain("run", 0); len(diffs) != 0 {
		t.Fatalf("unchanged file produced %+v", diffs)
	}
}

func TestTurnDiffStoreDegradesOversizedFiles(t *testing.T) {
	store := NewTurnDiffStore()
	huge := strings.Repeat("x", turnDiffMaxFileBytes+1)
	store.RecordChange("run", "big.txt", "", false, huge)
	diffs := store.Drain("run", 0)
	if len(diffs) != 1 || diffs[0].Note != "file too large to diff" {
		t.Fatalf("oversized entry = %+v", diffs)
	}
}

func TestTurnDiffStoreHonorsBudgetFallback(t *testing.T) {
	store := NewTurnDiffStore()
	for i := 0; i < 3; i++ {
		store.RecordChange("run", strings.Repeat("f", i+1)+".txt", "a\n", true, "b\n")
	}
	diffs := store.Drain("run", time.Nanosecond)
	if len(diffs) != 3 {
		t.Fatalf("budget drain = %+v, want all 3 paths", diffs)
	}
	noted := 0
	for _, entry := range diffs {
		if entry.Note == "diff budget exceeded" {
			noted++
		}
	}
	if noted == 0 {
		t.Fatalf("no path-only fallback entries: %+v", diffs)
	}
}

func TestTurnDiffStoreNilSafe(t *testing.T) {
	var store *TurnDiffStore
	store.RecordChange("run", "f.txt", "", false, "x\n")
	if diffs := store.Drain("run", 0); diffs != nil {
		t.Fatalf("nil store drain = %+v", diffs)
	}
}

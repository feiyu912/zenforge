package docs

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The formatting gate.
//
// Every ADR in this repository states its verification as `go test`, `go vet ./...`
// and `gofmt -l`, but nothing enforced the third: CI ran the tests and vet, and
// `gofmt -l .` was a line in a document. A file with no trailing newline sat on
// main that way (ADR 0137).
//
// The check is a test rather than a workflow step for two reasons: `go test
// ./docs/...` is already a required CI step, so the gate cannot be forgotten when
// the workflow is edited; and a test runs identically in a developer's shell,
// where a red result is actionable, instead of only in CI.
//
// `go/format` is the same implementation the `gofmt` command wraps, so this needs
// no external binary and no shell.

// TestGoSourcesAreFormatted fails on any Go file `gofmt` would rewrite.
func TestGoSourcesAreFormatted(t *testing.T) {
	scanned := 0
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "webui":
				// `webui/` is the staged console artifact: JavaScript and its
				// own vendored files, none of them Go.
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		formatted, formatErr := format.Source(source)
		if formatErr != nil {
			// A file that does not parse is a compile error the build reports
			// with a better message than this test could; the gate is about
			// formatting, so it only reports what it can compare.
			return nil
		}
		if string(formatted) != string(source) {
			rel, relErr := filepath.Rel("..", path)
			if relErr != nil {
				rel = path
			}
			t.Errorf("%s is not gofmt-formatted: run `gofmt -w %s`", filepath.ToSlash(rel), rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir returned error: %v", err)
	}
	// A walk that visited nothing would pass vacuously -- and the check is only
	// meaningful over the whole module.
	if scanned < 100 {
		t.Fatalf("scanned %d Go files, which is too few to be the module", scanned)
	}
}

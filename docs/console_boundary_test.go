package docs

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The console adapter tier: everything below these paths may speak the DSH
// protocol, import its packages, and name its methods. Nothing else may (ADR
// 0099).
var consoleAdapterPrefixes = []string{
	"internal/dsh",
	"cli/",
	"cmd/",
	"webui/",
	"docs/",
}

// Vocabulary that belongs to the console protocol and to no framework package:
// a core package that names one of these has grown an inbound dependency on the
// adapter's shape.
var consoleVocabulary = []string{
	"llm-pi-ai",
	"ui-onboarding",
	"remote.mux",
	"directoryPicker",
	"session/selectModel",
	"llm/discoverModels",
	"dshapi",
	"dshboot",
	"dshmount",
	"dshsession",
	"dshstream",
}

func inConsoleAdapter(rel string) bool {
	for _, prefix := range consoleAdapterPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// walkCoreGoFiles visits every Go file that is not part of the console adapter
// tier, handing the caller its repository-relative slug path and its source.
func walkCoreGoFiles(t *testing.T, visit func(rel string, source []byte)) {
	t.Helper()
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel("..", path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if inConsoleAdapter(rel) {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		visit(rel, source)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir returned error: %v", err)
	}
}

// TestCoreDoesNotImportTheConsoleAdapter pins the dependency direction: the
// framework may be consumed by the adapter, never the reverse.
func TestCoreDoesNotImportTheConsoleAdapter(t *testing.T) {
	walkCoreGoFiles(t, func(rel string, source []byte) {
		file, err := parser.ParseFile(token.NewFileSet(), rel, source, parser.ImportsOnly)
		if err != nil {
			t.Errorf("%s: parse imports: %v", rel, err)
			return
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			switch {
			case strings.HasPrefix(path, "github.com/feiyu912/zenforge/internal/dsh"):
				t.Errorf("%s imports the console adapter package %q; the core must not depend on an adapter", rel, path)
			case strings.Contains(path, "/webui/dsh"):
				t.Errorf("%s imports the vendored console %q; the core must not depend on an adapter", rel, path)
			case path == "github.com/feiyu912/zenforge/cli":
				t.Errorf("%s imports the console host entry package %q; the core must not depend on an adapter", rel, path)
			}
		}
	})
}

// TestConsoleVocabularyStaysOutOfTheCore keeps the adapter's words out of the
// framework, so the protocol cannot quietly become the project's shape.
func TestConsoleVocabularyStaysOutOfTheCore(t *testing.T) {
	walkCoreGoFiles(t, func(rel string, source []byte) {
		text := string(source)
		for _, term := range consoleVocabulary {
			if strings.Contains(text, term) {
				t.Errorf("%s names the console protocol term %q; that vocabulary belongs to the adapter tier (ADR 0099)", rel, term)
			}
		}
	})
}

// TestArchitectureNamesTheLayers keeps the repository's front door honest: the
// architecture page must state the three layers and point at the adapter's
// ledger, or a reader is back to guessing what this project is.
func TestArchitectureNamesTheLayers(t *testing.T) {
	source, err := os.ReadFile("architecture.md")
	if err != nil {
		t.Fatalf("read architecture.md: %v", err)
	}
	text := string(source)
	required := []string{
		"deep API",
		"harness core",
		"adapters",
		"internal/dshapi",
		"ADR 0099",
		"dsh-console-coverage.md",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Errorf("architecture.md does not mention %q; the layered boundary must be stated where a reader lands first", want)
		}
	}
}

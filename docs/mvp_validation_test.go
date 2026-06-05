package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	referencedTestName = regexp.MustCompile(`\b(?:Test|Benchmark)[A-Za-z0-9_]+\b`)
	definedTestFunc    = regexp.MustCompile(`func\s+((?:Test|Benchmark)[A-Za-z0-9_]+)\s*\(`)
)

func TestMVPValidationReferencesExistingTests(t *testing.T) {
	data, err := os.ReadFile("mvp-validation.md")
	if err != nil {
		t.Fatalf("ReadFile mvp-validation.md returned error: %v", err)
	}
	referenced := uniqueMatches(referencedTestName, string(data), 0)
	defined, err := collectDefinedTests("..")
	if err != nil {
		t.Fatalf("collectDefinedTests returned error: %v", err)
	}

	var missing []string
	for _, name := range referenced {
		if !defined[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("mvp-validation.md references missing tests: %s", strings.Join(missing, ", "))
	}
}

func collectDefinedTests(root string) (map[string]bool, error) {
	defined := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range uniqueMatches(definedTestFunc, string(data), 1) {
			defined[name] = true
		}
		return nil
	})
	return defined, err
}

func uniqueMatches(re *regexp.Regexp, text string, group int) []string {
	seen := map[string]bool{}
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		if group >= len(match) {
			continue
		}
		seen[match[group]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

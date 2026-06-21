package docs

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var platformBoundaryTerms = []string{
	"agent" + "-platform",
	"Zen" + "Mind",
}

func TestGoSourceKeepsPlatformBoundary(t *testing.T) {
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

		rel, err := filepath.Rel("..", path)
		if err != nil {
			return fmt.Errorf("make %q relative to repository root: %w", path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, violation := range platformBoundaryViolations(filepath.ToSlash(rel), data) {
			t.Errorf("%s: %s", filepath.ToSlash(rel), violation)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir returned error: %v", err)
	}
}

func TestPlatformBoundaryAllowlist(t *testing.T) {
	platformModule := platformBoundaryTerms[0]
	platformBrand := platformBoundaryTerms[1]
	tests := []struct {
		name       string
		path       string
		source     string
		wantDetail string
	}{
		{
			name:       "core harness rejects platform module name",
			path:       "harness/runner.go",
			source:     "package harness\n// copied from " + platformModule + "\n",
			wantDetail: "non-adapter Go source must not mention platform boundary term",
		},
		{
			name:       "other packages reject platform brand",
			path:       "model/client_test.go",
			source:     "package model\nconst source = \"" + platformBrand + "\"\n",
			wantDetail: "non-adapter Go source must not mention platform boundary term",
		},
		{
			name:   "adapter may document protocol source",
			path:   "adapters/zenmind/run.go",
			source: "package zenmind\n// Protocol source: " + platformModule + " / " + platformBrand + ".\n",
		},
		{
			name:   "adapter fixture may preserve protocol names",
			path:   "adapters/zenmind/run_test.go",
			source: "package zenmind\nconst fixture = `" + platformBrand + " " + platformModule + "`\n",
		},
		{
			name:   "adapter may import repository neutral DTOs",
			path:   "adapters/zenmind/run.go",
			source: "package zenmind\nimport \"github.com/feiyu912/zenforge/model\"\n",
		},
		{
			name:       "adapter rejects sibling platform module import",
			path:       "adapters/zenmind/run.go",
			source:     "package zenmind\nimport \"" + platformModule + "/contracts\"\n",
			wantDetail: "adapter must use repository-neutral DTOs instead of platform module import",
		},
		{
			name:       "adapter rejects internal package import",
			path:       "adapters/zenmind/run.go",
			source:     "package zenmind\nimport \"example.com/platform/internal/contracts\"\n",
			wantDetail: "adapter must not import an internal package",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := platformBoundaryViolations(tt.path, []byte(tt.source))
			if tt.wantDetail == "" {
				if len(violations) != 0 {
					t.Fatalf("expected source to be allowed, got violations: %v", violations)
				}
				return
			}
			if len(violations) != 1 || !strings.Contains(violations[0], tt.wantDetail) {
				t.Fatalf("expected one violation containing %q, got %v", tt.wantDetail, violations)
			}
		})
	}
}

func platformBoundaryViolations(path string, source []byte) []string {
	if !isPlatformAdapterPath(path) {
		var violations []string
		for _, term := range platformBoundaryTerms {
			if strings.Contains(string(source), term) {
				violations = append(violations, fmt.Sprintf("non-adapter Go source must not mention platform boundary term %q", term))
			}
		}
		return violations
	}

	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
	if err != nil {
		return []string{fmt.Sprintf("cannot parse adapter imports: %v", err)}
	}
	var violations []string
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			violations = append(violations, fmt.Sprintf("cannot decode adapter import %s: %v", spec.Path.Value, err))
			continue
		}
		if strings.Contains(importPath, platformBoundaryTerms[0]) {
			violations = append(violations, fmt.Sprintf("adapter must use repository-neutral DTOs instead of platform module import %q", importPath))
		}
		if importPathHasSegment(importPath, "internal") {
			violations = append(violations, fmt.Sprintf("adapter must not import an internal package %q", importPath))
		}
	}
	return violations
}

func isPlatformAdapterPath(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	return path == "adapters/zenmind" || strings.HasPrefix(path, "adapters/zenmind/")
}

func importPathHasSegment(importPath, segment string) bool {
	for _, part := range strings.Split(importPath, "/") {
		if part == segment {
			return true
		}
	}
	return false
}

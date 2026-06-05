package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoSourceKeepsPlatformBoundary(t *testing.T) {
	forbidden := []string{
		"agent" + "-platform",
		"Zen" + "Mind",
	}
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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, term := range forbidden {
			if strings.Contains(string(data), term) {
				t.Fatalf("%s contains forbidden platform boundary term %q", path, term)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir returned error: %v", err)
	}
}

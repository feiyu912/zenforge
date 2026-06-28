package zenmind

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fixtureManifest struct {
	Schema       string                 `json:"schema"`
	SourceRepo   string                 `json:"sourceRepo"`
	SourceCommit string                 `json:"sourceCommit"`
	Fixtures     []fixtureManifestEntry `json:"fixtures"`
}

type fixtureManifestEntry struct {
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256"`
	SourceFiles []string `json:"sourceFiles"`
}

func TestPlatformFixtureManifest(t *testing.T) {
	root := filepath.Join("testdata", "platform")
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("Unmarshal manifest: %v", err)
	}
	if manifest.Schema != "zenforge.zenmind_fixture_manifest.v1" ||
		manifest.SourceRepo != "agent-platform" || len(manifest.SourceCommit) != 40 {
		t.Fatalf("invalid fixture manifest identity: %#v", manifest)
	}
	seen := make(map[string]struct{}, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		if fixture.Path == "" || filepath.Base(fixture.Path) != fixture.Path ||
			len(fixture.SourceFiles) == 0 || len(fixture.SHA256) != 64 {
			t.Fatalf("invalid fixture manifest entry: %#v", fixture)
		}
		if _, ok := seen[fixture.Path]; ok {
			t.Fatalf("duplicate fixture manifest path %q", fixture.Path)
		}
		seen[fixture.Path] = struct{}{}
		contents, err := os.ReadFile(filepath.Join(root, fixture.Path))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", fixture.Path, err)
		}
		sum := sha256.Sum256(contents)
		if got := hex.EncodeToString(sum[:]); got != fixture.SHA256 {
			t.Fatalf("fixture %s sha256 = %s, want %s", fixture.Path, got, fixture.SHA256)
		}
	}
	files, err := filepath.Glob(filepath.Join(root, "*.json*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(files)-1 {
		t.Fatalf("manifest covers %d fixtures, directory contains %d fixture files", len(seen), len(files)-1)
	}
}

package dshconsole

import (
	"io/fs"
	"strings"
	"testing"
)

// The host injects the boot rows into these exact bytes, so Index must return
// the same shell Handler serves, including the rebranded title. A drift here
// would mean the injected page and the served page are different documents.
func TestIndexReturnsTheServedShell(t *testing.T) {
	data, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !strings.Contains(string(data), "<title>ZenForge</title>") {
		t.Error("Index returned a shell without the ZenForge title")
	}
	if !strings.Contains(string(data), "./assets/") {
		t.Error("Index returned a shell with no relative asset references")
	}
}

// Plugins is the view the boot-graph composer reads each bundle through; it
// must resolve every staged entry directory and its client.js.
func TestPluginsResolvesEveryStagedBundle(t *testing.T) {
	plugins := Plugins()
	count := 0
	err := fs.WalkDir(plugins, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "client.js" {
			return err
		}
		if _, readErr := fs.ReadFile(plugins, name); readErr != nil {
			t.Errorf("read staged bundle %s: %v", name, readErr)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("walk the plugin tree: %v", err)
	}
	if count == 0 {
		t.Fatal("Plugins exposed no staged client.js bundles")
	}
}

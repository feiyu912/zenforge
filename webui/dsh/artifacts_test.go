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
	if !strings.Contains(string(data), "<title>zenforge</title>") {
		t.Error("Index returned a shell without the zenforge title")
	}
	if !strings.Contains(string(data), "./assets/") {
		t.Error("Index returned a shell with no relative asset references")
	}
}

// The console's public identity is the product's own name in its command form --
// `zenforge`, the name of the binary an operator types -- everywhere a user sees
// it: the page title, the installable-app name, the favicon's accessible name and
// the brand plugins' own strings. A capitalised variant would be a second spelling
// of the same product, and upstream's name must not survive in a brand position at
// all. Both are asserted over the staged bytes, because a rebuild is what would
// reintroduce either.
func TestStagedIdentityIsTheProductsOwnName(t *testing.T) {
	shell, err := Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	page := string(shell)
	if !strings.Contains(page, "<title>zenforge</title>") {
		t.Error("the served shell does not carry the zenforge title")
	}
	// The upstream product name is what the rebrand exists to remove. Module
	// specifiers are lowercase (`@deepseek-ai/...`), so this only matches the
	// display name.
	if strings.Contains(page, "DeepSeek Harness") {
		t.Error("the served shell still carries the upstream product name")
	}
	manifest, err := fs.ReadFile(artifacts, "manifest.webmanifest")
	if err != nil {
		t.Fatalf("read the manifest: %v", err)
	}
	for _, want := range []string{`"name": "zenforge"`, `"short_name": "zenforge"`} {
		if !strings.Contains(string(manifest), want) {
			t.Errorf("manifest is missing %s", want)
		}
	}
	favicon, err := fs.ReadFile(artifacts, "favicon.svg")
	if err != nil {
		t.Fatalf("read the favicon: %v", err)
	}
	if !strings.Contains(string(favicon), "zenforge") {
		t.Error("the favicon does not carry the accessible brand name")
	}
	// No staged *browser-visible* byte may spell the brand any other way.
	plugins := Plugins()
	err = fs.WalkDir(plugins, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, readErr := fs.ReadFile(plugins, name)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), "ZenForge") {
			t.Errorf("%s spells the brand with a capital: the console shows zenforge", name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the plugin tree: %v", err)
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

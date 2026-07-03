package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/skill"
	"github.com/feiyu912/zenforge/tool"
)

func writeSkill(t *testing.T, root, name, description, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: " + name + "\ndescription: " + description +
		"\nlicense: MIT\nmetadata:\n  owner: platform\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogScansSortsLoadsAndDigests(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "z-last", "Last", "instructions z")
	writeSkill(t, root, "a-first", "First", "instructions a")
	catalog, err := New(root, Options{Source: "built-in"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "a-first" || items[1].Name != "z-last" {
		t.Fatalf("unexpected descriptors: %#v", items)
	}
	content, err := catalog.Load(context.Background(), "a-first")
	if err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(filepath.Join(root, "a-first", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(document)
	if content.Body != "instructions a" ||
		strings.Contains(content.Body, "name:") ||
		content.Digest != "sha256:"+hex.EncodeToString(sum[:]) ||
		!strings.HasPrefix(content.Digest, "sha256:") ||
		content.Provenance.Source != "built-in" ||
		content.Provenance.Path != "a-first/SKILL.md" ||
		strings.Contains(content.Provenance.Path, root) ||
		content.Descriptor.Metadata["owner"] != "platform" {
		t.Fatalf("unexpected content: %#v", content)
	}
}

func TestCatalogDefaultsMatchSpecification(t *testing.T) {
	catalog, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.options.MaxContentBytes != 64<<10 ||
		catalog.options.MaxFrontmatterBytes != 8<<10 ||
		catalog.options.MaxDescriptionBytes != 512 ||
		catalog.options.MaxCatalogEntries != 256 {
		t.Fatalf("unexpected defaults: %#v", catalog.options)
	}
}

func TestCatalogOptionsCannotRelaxDefaults(t *testing.T) {
	tests := []Options{
		{MaxContentBytes: skill.MaxContentBytes + 1},
		{MaxFrontmatterBytes: skill.MaxFrontmatterBytes + 1},
		{MaxDescriptionBytes: skill.MaxDescriptionBytes + 1},
		{MaxCatalogEntries: skill.MaxCatalogEntries + 1},
	}
	for _, options := range tests {
		if _, err := New(t.TempDir(), options); !errors.Is(err, skill.ErrInvalid) {
			t.Fatalf("options=%#v error=%v, want ErrInvalid", options, err)
		}
	}
}

func TestCatalogEntryLimitBoundary(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < skill.MaxCatalogEntries; i++ {
		name := fmt.Sprintf("skill-%03d", i)
		writeSkill(t, root, name, "ok", "body")
	}
	catalog, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(context.Background())
	if err != nil || len(items) != skill.MaxCatalogEntries {
		t.Fatalf("entries=%d error=%v", len(items), err)
	}
	writeSkill(t, root, "skill-over-limit", "ok", "body")
	if _, err := catalog.List(context.Background()); !errors.Is(err, skill.ErrTooLarge) {
		t.Fatalf("error=%v, want ErrTooLarge", err)
	}
}

func TestCatalogSupportsDirectSkillDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "direct-skill", "Direct", "body")
	catalog, err := New(filepath.Join(root, "direct-skill"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(context.Background())
	if err != nil || len(items) != 1 || items[0].Name != "direct-skill" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	content, err := catalog.Load(context.Background(), "direct-skill")
	if err != nil {
		t.Fatal(err)
	}
	if content.Provenance.Source != "filesystem" || content.Provenance.Path != "SKILL.md" {
		t.Fatalf("unexpected provenance: %#v", content.Provenance)
	}
	if strings.Contains(content.Provenance.Source, root) || strings.Contains(content.Provenance.Path, root) {
		t.Fatalf("provenance leaked host root %q: %#v", root, content.Provenance)
	}
}

func TestCatalogRejectsMalformedFrontmatter(t *testing.T) {
	tests := []struct {
		name        string
		frontmatter string
	}{
		{"duplicate key", "name: malformed\ndescription: first\ndescription: second"},
		{"duplicate metadata", "name: malformed\ndescription: ok\nmetadata:\n  owner: one\n  owner: two"},
		{"multiline description", "name: malformed\ndescription: |\n  hidden line"},
		{"control character", "name: malformed\ndescription: bad\x01value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "malformed")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			raw := "---\n" + test.frontmatter + "\n---\nbody"
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			catalog, err := New(root, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.List(context.Background()); !errors.Is(err, skill.ErrInvalid) {
				t.Fatalf("error=%v, want ErrInvalid", err)
			}
		})
	}
}

func TestCatalogRejectsOversizeFrontmatter(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "oversize")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: oversize\ndescription: ok\nmetadata:\n  padding: " +
		strings.Repeat("x", skill.MaxFrontmatterBytes) + "\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := New(root, Options{MaxContentBytes: int64(len(raw) + 1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.List(context.Background()); !errors.Is(err, skill.ErrInvalid) {
		t.Fatalf("error=%v, want ErrInvalid", err)
	}
}

func TestCatalogReturnsMetadataCopies(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "copy", "Copy", "body")
	catalog, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first[0].Metadata["owner"] = "mutated"
	second, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Metadata["owner"] != "platform" {
		t.Fatalf("metadata mutation escaped: %#v", second[0].Metadata)
	}
}

func TestBundleSnapshotDoesNotLeakFrontmatterOrFollowDiskChanges(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "snapshot", "Visible description", "# Original body")
	catalog, err := New(root, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := skill.NewBundle(context.Background(), catalog, []string{"snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := bundle.CatalogPrompt()
	if !strings.Contains(prompt, "snapshot: Visible description") ||
		strings.Contains(prompt, "license:") || strings.Contains(prompt, "owner:") ||
		strings.Contains(prompt, "Original body") {
		t.Fatalf("prompt leaked non-descriptor content: %q", prompt)
	}
	first, err := bundle.LoadSkillTool().Call(context.Background(),
		json.RawMessage(`{"name":"snapshot"}`), tool.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Output != "# Original body" ||
		strings.Contains(first.Output, "name:") || strings.Contains(first.Output, "license:") {
		t.Fatalf("tool leaked frontmatter: %q", first.Output)
	}
	encoded, err := json.Marshal(first.Structured)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), root) {
		t.Fatalf("tool result leaked host root %q: %s", root, encoded)
	}
	path := filepath.Join(root, "snapshot", "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: snapshot\ndescription: Changed\n---\nchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := bundle.LoadSkillTool().Call(context.Background(),
		json.RawMessage(`{"name":"snapshot"}`), tool.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Output != first.Output || second.Structured["digest"] != first.Structured["digest"] {
		t.Fatalf("bundle changed with disk: first=%#v second=%#v", first, second)
	}
}

func TestCatalogDefaultSizeAndDescriptionLimits(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "large-default", "ok", strings.Repeat("x", skill.MaxContentBytes))
		catalog, err := New(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.List(context.Background()); !errors.Is(err, skill.ErrTooLarge) {
			t.Fatalf("error=%v, want ErrTooLarge", err)
		}
	})
	t.Run("description", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "long-description", strings.Repeat("d", skill.MaxDescriptionBytes+1), "body")
		catalog, err := New(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.List(context.Background()); !errors.Is(err, skill.ErrInvalid) {
			t.Fatalf("error=%v, want ErrInvalid", err)
		}
	})
}

func TestCatalogRejectsInvalidSkillsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, root string)
		want error
	}{
		{"name mismatch", func(t *testing.T, root string) {
			writeSkill(t, root, "directory", "ok", "body")
			path := filepath.Join(root, "directory", "SKILL.md")
			raw, _ := os.ReadFile(path)
			os.WriteFile(path, []byte(strings.Replace(string(raw), "name: directory", "name: other", 1)), 0o644)
		}, skill.ErrInvalid},
		{"bad name", func(t *testing.T, root string) {
			writeSkill(t, root, "Bad_Name", "ok", "body")
		}, skill.ErrInvalid},
		{"too large", func(t *testing.T, root string) {
			writeSkill(t, root, "large", "ok", strings.Repeat("x", 200))
		}, skill.ErrTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.make(t, root)
			options := Options{}
			if test.name == "too large" {
				options.MaxContentBytes = 100
			}
			catalog, err := New(root, options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.List(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestCatalogRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeSkill(t, outside, "escaped", "no", "secret")
	if err := os.Symlink(filepath.Join(outside, "escaped"), filepath.Join(root, "escaped")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	catalog, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.List(context.Background()); !errors.Is(err, skill.ErrPathEscape) {
		t.Fatalf("error=%v, want ErrPathEscape", err)
	}
}

func TestCatalogUnknownSkill(t *testing.T) {
	catalog, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Load(context.Background(), "missing"); !errors.Is(err, skill.ErrNotFound) {
		t.Fatalf("error=%v, want ErrNotFound", err)
	}
}

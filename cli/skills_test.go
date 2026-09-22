package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillfs "github.com/feiyu912/zenforge/skill/fs"
)

// skillsFixture writes a two-layer skill catalog into a temporary workspace:
// the workspace's own directory and a per-user one, each holding upstream-shaped
// SKILL.md files. Both layers are named explicitly, because the per-user default
// is the machine's real config directory and a test must not read it.
func skillsFixture(t *testing.T) (options, string) {
	t.Helper()
	workspace := t.TempDir()
	user := t.TempDir()
	write := func(root, name, frontmatter string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		raw := "---\n" + frontmatter + "\n---\nbody for " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(user, "release", "name: release\ndescription: Ship a release\nwhenToUse: when the change is ready to ship")
	write(user, "shadowed", "name: shadowed\ndescription: the user's copy")
	write(filepath.Join(workspace, skillfs.DefaultDir), "shadowed", "name: shadowed\ndescription: the workspace's copy")
	write(filepath.Join(workspace, skillfs.DefaultDir), "internal", "name: internal\ndescription: private checklist\nuser-invocable: false")
	write(filepath.Join(workspace, skillfs.DefaultDir), "operator", "name: operator\ndescription: a typed checklist\ndisable-model-invocation: true")
	return options{workspace: workspace, userSkillsDir: user}, workspace
}

// The console's panel lists what the operator may invoke, with the workspace's
// copy of a skill winning over the per-user one -- the same layering the slash
// commands follow.
func TestConsoleSkillsListBothLayers(t *testing.T) {
	opts, _ := skillsFixture(t)
	catalog, err := buildSkillCatalog(opts)
	if err != nil {
		t.Fatalf("buildSkillCatalog: %v", err)
	}
	rows, err := consoleSkills{catalog: catalog}.Skills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]struct {
		description string
		whenToUse   string
		model       bool
	}{}
	for _, row := range rows {
		byName[row.Name] = struct {
			description string
			whenToUse   string
			model       bool
		}{row.Description, row.WhenToUse, row.ModelInvocable}
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want release, shadowed and operator", rows)
	}
	if got := byName["shadowed"].description; got != "the workspace's copy" {
		t.Fatalf("shadowed description = %q, want the workspace layer", got)
	}
	if got := byName["release"].whenToUse; got != "when the change is ready to ship" {
		t.Fatalf("release whenToUse = %q, want the skill's own guidance", got)
	}
	if byName["operator"].model {
		t.Fatalf("operator = %+v, want the model-facing badge off", byName["operator"])
	}
	if _, present := byName["internal"]; present {
		t.Fatalf("rows = %+v, want the operator-hidden skill left out", rows)
	}
}

// A host with no skill directory at all is not an error: it has no skills, and the
// panel says so with an empty list. A workspace that does have one gets the tool
// and the catalog prompt; one that does not must not advertise an empty catalog.
func TestBuildSkillsIsAbsentWithoutACatalog(t *testing.T) {
	bare := options{workspace: t.TempDir(), userSkillsDir: filepath.Join(t.TempDir(), "missing")}
	bundle, err := buildSkills(context.Background(), bare)
	if err != nil {
		t.Fatalf("buildSkills: %v", err)
	}
	if bundle != nil {
		t.Fatal("a host with no skills built a bundle, which would advertise an empty catalog")
	}
	catalog, err := buildSkillCatalog(bare)
	if err != nil {
		t.Fatalf("buildSkillCatalog: %v", err)
	}
	items, err := catalog.List(context.Background())
	if err != nil || len(items) != 0 {
		t.Fatalf("items = %+v err = %v, want an empty catalog", items, err)
	}

	opts, _ := skillsFixture(t)
	bundle, err = buildSkills(context.Background(), opts)
	if err != nil {
		t.Fatalf("buildSkills: %v", err)
	}
	if bundle == nil {
		t.Fatal("a host with skills built no bundle, so the agent would not have them")
	}
	prompt := bundle.CatalogPrompt()
	if !strings.Contains(prompt, "release: Ship a release") {
		t.Fatalf("catalog prompt = %q, want the operator-visible skills", prompt)
	}
	if strings.Contains(prompt, "operator: a typed checklist") {
		t.Fatalf("catalog prompt = %q, want the model-hidden skill left out", prompt)
	}
	if bundle.LoadSkillTool() == nil {
		t.Fatal("the bundle carries no load_skill tool")
	}
}

// The catalog is opened at startup but scanned per request, so an unreadable
// skill directory surfaces where the operator can act on it: the read fails and
// the console is told the listing failed, instead of the panel quietly showing an
// empty catalog that looks like "no skills are installed".
func TestConsoleSkillsReportsAnUnreadableLayer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	catalog, err := buildSkillCatalog(options{skillsDir: locked})
	if err != nil {
		t.Fatalf("opening a locked directory is not itself a read: %v", err)
	}
	if _, err := (consoleSkills{catalog: catalog}).Skills(context.Background()); err == nil {
		t.Fatal("reading an unreadable skill directory reported an empty catalog")
	}
	if _, err := buildSkills(context.Background(), options{skillsDir: locked}); err == nil {
		t.Fatal("a run was configured from an unreadable skill directory")
	}
}

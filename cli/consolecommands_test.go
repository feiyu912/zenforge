package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/commands"
)

// consoleCommandFixture loads a real catalog from a temporary directory, so the
// adapter is tested against the files a command directory actually holds.
func consoleCommandFixture(t *testing.T) (consoleCommands, string) {
	t.Helper()
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// The include target lives in the workspace, the definitions live in the
	// workspace's command directory: every markdown file in that directory is a
	// command, so a stray README there would be one too.
	write("README.md", "the project readme\n")
	dir := filepath.Join(root, commands.DefaultDir)
	write(filepath.Join(commands.DefaultDir, "review.md"), "---\ndescription: Review the working tree\nargument-hint: \"<path>\"\n---\nReview @README.md for $ARGUMENTS\n")
	// A command with no description of its own: the menu still needs a row.
	write(filepath.Join(commands.DefaultDir, "git/commit.md"), "---\nallowed-tools: shell\n---\nSummarise the diff\n")
	catalog, err := commands.Load(dir)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return consoleCommands{catalog: catalog, opts: options{workspace: root}}, root
}

func TestConsoleCommandsListTheCatalog(t *testing.T) {
	adapter, _ := consoleCommandFixture(t)
	descriptors := adapter.Commands()
	byName := map[string]string{}
	for _, descriptor := range descriptors {
		byName[descriptor.Name] = descriptor.Description
	}
	if len(descriptors) != 2 {
		t.Fatalf("descriptors = %+v, want the two command files", descriptors)
	}
	if byName["review"] != "Review the working tree" {
		t.Fatalf("review description = %q, want the file's own description", byName["review"])
	}
	// A command with no description falls back to the catalog's listing line,
	// which names the arguments -- an empty menu row would be worse.
	if !strings.Contains(byName["git:commit"], "/git:commit") {
		t.Fatalf("git:commit description = %q, want the catalog's listing line", byName["git:commit"])
	}
	for _, descriptor := range descriptors {
		if descriptor.Name != "review" {
			continue
		}
		if descriptor.Input == nil || descriptor.Input.Hint != "<path>" {
			t.Fatalf("review input = %+v, want the declared argument hint", descriptor.Input)
		}
		if descriptor.Input.Attachments {
			t.Fatal("review advertises attachments, want none: this host's commands carry no attachments")
		}
	}
}

// Expansion is the host's own, which is what makes the console's commands behave
// exactly like the ones the command line runs: arguments, @file includes and the
// command's shell permission all come from the catalog's rules.
func TestConsoleCommandsExpandWithTheHostsOwnRules(t *testing.T) {
	adapter, _ := consoleCommandFixture(t)
	name, text, ok := adapter.Expand("/review src/main.go")
	if !ok || name != "review" {
		t.Fatalf("Expand = %q %v, want the review command", name, ok)
	}
	if !strings.Contains(text, "src/main.go") {
		t.Fatalf("text = %q, want the arguments substituted", text)
	}
	if !strings.Contains(text, "the project readme") {
		t.Fatalf("text = %q, want the @file include expanded against the workspace", text)
	}
	// A command that takes no arguments still resolves.
	if _, _, ok := adapter.Expand("/git:commit"); !ok {
		t.Fatal("a namespaced command did not resolve")
	}
}

// A line that names no command is not an error here: the composer turns a missing
// value into "unknown or malformed command" and keeps the draft.
func TestConsoleCommandsReportLinesTheyCannotResolve(t *testing.T) {
	adapter, _ := consoleCommandFixture(t)
	for _, line := range []string{
		"/nope",                // not in the catalog
		"/usr/local/bin/thing", // a path, not an invocation
		"/",                    // no name at all
		"plain task",           // not an invocation
		"/review-a-file.md",    // a file name, not a command name
	} {
		if name, _, ok := adapter.Expand(line); ok {
			t.Fatalf("Expand(%q) = %q, want it reported as unresolvable", line, name)
		}
	}
}

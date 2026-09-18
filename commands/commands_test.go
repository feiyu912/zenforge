package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return path
}

func TestLoadReadsFrontMatterAndNamespaces(t *testing.T) {
	root := t.TempDir()
	write(t, root, "review.md", "---\ndescription: Review the working tree\nargument-hint: \"<path>\"\nmodel: gpt-4.1-mini\n---\nReview @README.md for $ARGUMENTS\n")
	write(t, root, "git/commit.md", "---\ndescription: Write a commit\nallowed-tools: shell, workspace_read\n---\nSummarise the diff\n")
	write(t, root, "notes.txt", "not a command")
	catalog, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if catalog.Len() != 2 {
		t.Fatalf("catalog = %#v", catalog.Names())
	}
	review, ok := catalog.Get("/review")
	if !ok {
		t.Fatalf("review not found: %#v", catalog.Names())
	}
	if review.Description != "Review the working tree" || review.ArgumentHint != "<path>" || review.Model != "gpt-4.1-mini" {
		t.Fatalf("review = %#v", review)
	}
	if !strings.Contains(review.Body, "Review @README.md") {
		t.Fatalf("body = %q", review.Body)
	}
	// A subdirectory is a namespace, reachable with ":" or "/".
	commit, ok := catalog.Get("git:commit")
	if !ok || len(commit.AllowedTools) != 2 {
		t.Fatalf("commit = %#v", commit)
	}
	if other, ok := catalog.Get("git/commit"); !ok || other.Name != commit.Name {
		t.Fatalf("slash namespace did not resolve: %#v", other)
	}
	// The listing is stable and readable.
	list := catalog.List()
	if !strings.Contains(list, "/git:commit - Write a commit") || strings.Index(list, "/git:commit") > strings.Index(list, "/review") {
		t.Fatalf("list = %q", list)
	}
	if _, ok := catalog.Get("missing"); ok {
		t.Fatal("an unknown command resolved")
	}
}

func TestLoadRefusesBadDefinitions(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
	}{
		{"unknown key", "bad.md", "---\nallowed_tools: shell\n---\nbody\n"},
		{"unclosed front matter", "bad.md", "---\ndescription: x\nbody\n"},
		{"empty body", "bad.md", "---\ndescription: x\n---\n\n"},
		{"bad boolean", "bad.md", "---\nrun-bash: yes please\n---\nbody\n"},
		{"not key value", "bad.md", "---\njust a line\n---\nbody\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, testCase.file, testCase.content)
			if _, err := Load(root); err == nil {
				t.Fatalf("definition %q was accepted", testCase.content)
			}
		})
	}
	// A missing directory is an empty catalog, not an error.
	catalog, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil || catalog.Len() != 0 {
		t.Fatalf("Load = %#v, %v", catalog, err)
	}
	// A file, not a directory, is an error.
	root := t.TempDir()
	path := write(t, root, "file.md", "body\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a file was accepted as a commands directory")
	}
	// The same name twice is ambiguous, so it is refused.
	duplicate := t.TempDir()
	write(t, duplicate, "same/one.md", "body\n")
	if _, err := Load(duplicate); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}

func TestExpandSubstitutesArguments(t *testing.T) {
	command := Command{Name: "fix", Body: "Task: $ARGUMENTS\nfirst=$1 second=$2 quoted=$3 missing=[$4] dollar=$$5"}
	expanded, err := Expand(command, `alpha "beta gamma" 'delta'`, ExpandOptions{})
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(expanded, "Task: alpha \"beta gamma\" 'delta'") {
		t.Fatalf("expanded = %q", expanded)
	}
	if !strings.Contains(expanded, "first=alpha second=beta gamma quoted=delta missing=[] dollar=$5") {
		t.Fatalf("positional substitution is wrong: %q", expanded)
	}
	// A command with no placeholders passes its body through unchanged.
	plain, err := Expand(Command{Name: "plain", Body: "do the thing"}, "ignored", ExpandOptions{})
	if err != nil || plain != "do the thing" {
		t.Fatalf("plain = %q, %v", plain, err)
	}
	// Oversized arguments are refused rather than truncated into a prompt.
	if _, err := Expand(Command{Name: "big", Body: "$ARGUMENTS"}, strings.Repeat("x", 100), ExpandOptions{MaxArguments: 10}); err == nil {
		t.Fatal("oversized arguments were accepted")
	}
	// A body that expands to nothing is refused.
	if _, err := Expand(Command{Name: "empty", Body: "$ARGUMENTS"}, "  ", ExpandOptions{}); err == nil {
		t.Fatal("an empty expansion was accepted")
	}
}

func TestExpandIncludesAreConfined(t *testing.T) {
	workspace := t.TempDir()
	write(t, workspace, "README.md", "# readme\n")
	write(t, workspace, "docs/guide.md", "guide\n")
	outside := write(t, t.TempDir(), "secret.txt", "secret\n")

	command := Command{Name: "ctx", Body: "start\n@README.md\n@docs/guide.md\nend"}
	expanded, err := Expand(command, "", ExpandOptions{Workspace: workspace})
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(expanded, "# readme") || !strings.Contains(expanded, "guide") || !strings.Contains(expanded, "end") {
		t.Fatalf("expanded = %q", expanded)
	}
	// An escape is refused, not read.
	for _, reference := range []string{"@../secret.txt", "@" + outside, "@missing.md"} {
		if _, err := Expand(Command{Name: "ctx", Body: reference}, "", ExpandOptions{Workspace: workspace}); err == nil {
			t.Fatalf("include %q was accepted", reference)
		}
	}
	// Without a workspace there is nothing to resolve against.
	if _, err := Expand(Command{Name: "ctx", Body: "@README.md"}, "", ExpandOptions{}); err == nil {
		t.Fatal("an include without a workspace was accepted")
	}
	// An oversized include is refused.
	write(t, workspace, "big.md", strings.Repeat("x", 2048))
	if _, err := Expand(Command{Name: "ctx", Body: "@big.md"}, "", ExpandOptions{Workspace: workspace, MaxIncludeBytes: 100}); err == nil {
		t.Fatal("an oversized include was accepted")
	}
	// A directory is not an include.
	if _, err := Expand(Command{Name: "ctx", Body: "@docs"}, "", ExpandOptions{Workspace: workspace}); err == nil {
		t.Fatal("a directory was included")
	}
	// An email address is not an include.
	expanded, err = Expand(Command{Name: "ctx", Body: "mail me at user@example.com"}, "", ExpandOptions{Workspace: workspace})
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(expanded, "user@example.com") {
		t.Fatalf("an email was rewritten: %q", expanded)
	}
}

func TestExpandBashRequiresOptIn(t *testing.T) {
	command := Command{Name: "status", Body: "status:\n!`git status --short`\nend"}
	// Without run-bash the expression is left verbatim: the author sees that
	// nothing ran instead of the prompt quietly losing a section.
	expanded, err := Expand(command, "", ExpandOptions{Bash: func(string) (string, error) {
		t.Fatal("bash ran without the opt-in")
		return "", nil
	}})
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(expanded, "!`git status --short`") {
		t.Fatalf("expanded = %q", expanded)
	}

	command.AllowBash = true
	expanded, err = Expand(command, "", ExpandOptions{Bash: func(expression string) (string, error) {
		if expression != "git status --short" {
			t.Fatalf("expression = %q", expression)
		}
		return " M main.go\n", nil
	}})
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(expanded, "status:\n M main.go\nend") {
		t.Fatalf("expanded = %q", expanded)
	}
	// Opted in but no shell available is an error, not a silent passthrough.
	if _, err := Expand(command, "", ExpandOptions{}); err == nil {
		t.Fatal("inline shell without a shell was accepted")
	}
	// A failing expression fails the expansion.
	if _, err := Expand(command, "", ExpandOptions{Bash: func(string) (string, error) {
		return "", errors.New("boom")
	}}); err == nil {
		t.Fatal("a failing inline shell was accepted")
	}
	// An unterminated expression is an error.
	if _, err := Expand(Command{Name: "s", Body: "!`git status", AllowBash: true}, "", ExpandOptions{Bash: func(string) (string, error) {
		return "", nil
	}}); err == nil {
		t.Fatal("an unterminated expression was accepted")
	}
	// The number of inline expressions is bounded.
	many := Command{Name: "s", AllowBash: true, Body: strings.Repeat("!`true` ", 20)}
	if _, err := Expand(many, "", ExpandOptions{Bash: func(string) (string, error) { return "", nil }}); err == nil {
		t.Fatal("unbounded inline shell was accepted")
	}
}

func TestSplitArgumentsHandlesQuotes(t *testing.T) {
	got := splitArguments(`a "b c" 'd e' f\ g`)
	want := []string{"a", "b c", "d e", "f g"}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}

func TestLoadLayersLetsTheWorkspaceShadowAUserCommand(t *testing.T) {
	workspace := t.TempDir()
	write(t, workspace, "review.md", "workspace review\n")
	write(t, workspace, "git/commit.md", "commit\n")
	user := t.TempDir()
	write(t, user, "review.md", "user review\n")
	write(t, user, "git/status.md", "status\n")
	catalog, err := LoadLayers(workspace, user)
	if err != nil {
		t.Fatalf("LoadLayers returned error: %v", err)
	}
	if catalog.Len() != 3 {
		t.Fatalf("catalog = %#v", catalog.Names())
	}
	review, ok := catalog.Get("review")
	if !ok || review.Layer != LayerWorkspace || !strings.Contains(review.Body, "workspace review") {
		t.Fatalf("review = %#v", review)
	}
	// Namespacing is the same in both layers and survives the merge.
	commit, ok := catalog.Get("git:commit")
	if !ok || commit.Layer != LayerWorkspace {
		t.Fatalf("commit = %#v", commit)
	}
	status, ok := catalog.Get("git/status")
	if !ok || status.Layer != LayerUser {
		t.Fatalf("status = %#v", status)
	}
	// The replaced user definition is retained so the listing can mark it.
	shadowed := catalog.Shadowed()
	if len(shadowed) != 1 || shadowed[0].Name != "review" || shadowed[0].Layer != LayerUser || !shadowed[0].Shadowed {
		t.Fatalf("shadowed = %#v", shadowed)
	}
	listing := catalog.List()
	if !strings.Contains(listing, "/review [workspace]") || !strings.Contains(listing, "/review [user] (workspace overrides user)") {
		t.Fatalf("listing = %q", listing)
	}
}

func TestLoadLayersTreatsMissingDirectoriesAsEmpty(t *testing.T) {
	workspace := t.TempDir()
	write(t, workspace, "only.md", "body\n")
	catalog, err := LoadLayers(workspace, filepath.Join(t.TempDir(), "absent"))
	if err != nil || catalog.Len() != 1 {
		t.Fatalf("LoadLayers = %#v, %v", catalog, err)
	}
	if _, ok := catalog.Get("only"); !ok {
		t.Fatalf("catalog = %#v", catalog.Names())
	}
	if catalog, err = LoadLayers(filepath.Join(t.TempDir(), "absent"), ""); err != nil || catalog.Len() != 0 {
		t.Fatalf("LoadLayers = %#v, %v", catalog, err)
	}
}

func TestLoadLayersNamesTheLayerInErrors(t *testing.T) {
	user := t.TempDir()
	write(t, user, "bad.md", "---\nallowed_tools: shell\n---\nbody\n")
	if _, err := LoadLayers(t.TempDir(), user); err == nil {
		t.Fatal("a malformed user command was accepted")
	} else if !strings.Contains(err.Error(), "user commands directory") || !strings.Contains(err.Error(), user) {
		t.Fatalf("error does not name the user directory: %v", err)
	}
	workspace := t.TempDir()
	write(t, workspace, "bad.md", "---\nallowed_tools: shell\n---\nbody\n")
	if _, err := LoadLayers(workspace, user); err == nil {
		t.Fatal("a malformed workspace command was accepted")
	} else if !strings.Contains(err.Error(), "workspace commands directory") {
		t.Fatalf("error does not name the workspace layer: %v", err)
	}
}

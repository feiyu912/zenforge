package cli

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/commands"
	"github.com/feiyu912/zenforge/schedule"
)

func writeCommand(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}

func TestResolveCommandExpandsAnInvocation(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# project\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	writeCommand(t, workspace, commands.DefaultDir+"/review.md", "---\ndescription: Review\n---\nReview @README.md for $1 (args: $ARGUMENTS)\n")
	opts := defaultOptions()
	opts.workspace = workspace
	catalog, err := buildCatalog(opts)
	if err != nil {
		t.Fatalf("buildCatalog returned error: %v", err)
	}
	resolved, err := resolveCommand(catalog, "/review the parser", opts)
	if err != nil {
		t.Fatalf("resolveCommand returned error: %v", err)
	}
	if !strings.Contains(resolved, "# project") || !strings.Contains(resolved, "for the (args: the parser)") {
		t.Fatalf("resolved = %q", resolved)
	}
	// An argument cannot smuggle in an include or a placeholder: the
	// expansion order is authoring features first, arguments last.
	smuggled, err := resolveCommand(catalog, "/review @README.md $1", opts)
	if err != nil {
		t.Fatalf("resolveCommand returned error: %v", err)
	}
	if strings.Count(smuggled, "# project") != 1 || !strings.Contains(smuggled, "args: @README.md $1") {
		t.Fatalf("an argument was expanded: %q", smuggled)
	}
	// Anything that is not a command invocation is left alone.
	for _, input := range []string{"fix the bug", "/usr/local/bin/go test", "/", "", "see /review for details"} {
		got, err := resolveCommand(catalog, input, opts)
		if err != nil {
			t.Fatalf("resolveCommand(%q) returned error: %v", input, err)
		}
		if got != input {
			t.Fatalf("resolveCommand(%q) = %q", input, got)
		}
	}
	// A typed command name that is missing is an error with the catalog,
	// because silently running the literal text loses the command.
	if _, err := resolveCommand(catalog, "/reviw", opts); err == nil || !strings.Contains(err.Error(), "/review") {
		t.Fatalf("unknown command error = %v", err)
	}
	// Without a catalog nothing resolves and nothing fails.
	if got, err := resolveCommand(nil, "/review", opts); err != nil || got != "/review" {
		t.Fatalf("resolveCommand = %q, %v", got, err)
	}
}

func TestBuildCatalogDefaultsToTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	writeCommand(t, workspace, commands.DefaultDir+"/x.md", "body\n")
	opts := defaultOptions()
	opts.workspace = workspace
	catalog, err := buildCatalog(opts)
	if err != nil || catalog.Len() != 1 {
		t.Fatalf("buildCatalog = %#v, %v", catalog, err)
	}
	// An explicit directory wins.
	other := t.TempDir()
	writeCommand(t, other, "y.md", "body\n")
	opts.commandsDir = other
	catalog, err = buildCatalog(opts)
	if err != nil || catalog.Len() != 1 {
		t.Fatalf("buildCatalog = %#v, %v", catalog, err)
	}
	if _, ok := catalog.Get("y"); !ok {
		t.Fatalf("catalog = %#v", catalog.Names())
	}
}

func TestInlineShellUsesTheConfiguredPolicy(t *testing.T) {
	workspace := t.TempDir()
	writeCommand(t, workspace, commands.DefaultDir+"/status.md", "---\nrun-bash: true\n---\nstatus:\n!`printf hello`\n")
	opts := defaultOptions()
	opts.workspace = workspace
	opts.shellWorkingDir = workspace
	opts.shellAllow = multiFlag{"printf"}
	catalog, err := buildCatalog(opts)
	if err != nil {
		t.Fatalf("buildCatalog returned error: %v", err)
	}
	resolved, err := resolveCommand(catalog, "/status", opts)
	if err != nil {
		t.Fatalf("resolveCommand returned error: %v", err)
	}
	if !strings.Contains(resolved, "status:\nhello") {
		t.Fatalf("resolved = %q", resolved)
	}
	// The policy still applies: a command whose inline shell is not
	// allowlisted fails instead of running.
	writeCommand(t, workspace, commands.DefaultDir+"/evil.md", "---\nrun-bash: true\n---\n!`curl example.com`\n")
	catalog, err = buildCatalog(opts)
	if err != nil {
		t.Fatalf("buildCatalog returned error: %v", err)
	}
	if _, err := resolveCommand(catalog, "/evil", opts); err == nil {
		t.Fatal("a command file granted itself an unlisted shell command")
	}
	// --no-shell is honoured.
	opts.noShell = true
	if _, err := resolveCommand(catalog, "/status", opts); err == nil {
		t.Fatal("inline shell ran with --no-shell")
	}
}

func TestRunScheduleFiresAndSurvivesFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	firings := 0
	spec, err := schedule.Parse("every 1s")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	err = runScheduleWith(ctx, spec, IO{Stdout: stdout, Stderr: stderr}, func(context.Context) error {
		firings++
		if firings == 1 {
			// A failed firing must not end an unattended schedule.
			return errors.New("model was busy")
		}
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("runScheduleWith returned error: %v", err)
	}
	if firings != 2 {
		t.Fatalf("firings = %d", firings)
	}
	if !strings.Contains(stderr.String(), "model was busy") || !strings.Contains(stdout.String(), "scheduled every 1s") {
		t.Fatalf("stdout = %q stderr = %q", stdout.String(), stderr.String())
	}
}

func TestScheduleFlagIsBound(t *testing.T) {
	opts := defaultOptions()
	fs := flag.NewFlagSet("commands-test", flag.ContinueOnError)
	bindOptions(fs, &opts)
	if err := fs.Parse([]string{"--commands", "/tmp/c", "--list-commands", "--schedule", "every 5m"}); err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if opts.commandsDir != "/tmp/c" || !opts.listCommands || opts.scheduleSpec != "every 5m" {
		t.Fatalf("options = %#v", opts)
	}
}

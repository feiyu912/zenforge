package examples_test

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSDKEmbeddedAgentRunsWithoutAPIKey(t *testing.T) {
	cmd := exec.Command("go", "run", "./sdk-embedded-agent")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run ./sdk-embedded-agent returned error: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "durable agent harness") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestHTTPHarnessExampleWiresDurableLocalService(t *testing.T) {
	data, err := os.ReadFile("http-harness-agent/main.go")
	if err != nil {
		t.Fatalf("ReadFile http-harness-agent/main.go returned error: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"provider.FromEnv()",
		"eventlogsqlite.Open",
		"checkpointsqlite.Open",
		"approvalsqlite.OpenInbox",
		"harnesshttp.OpenSQLiteRunRegistry",
		"harnesshttp.NewRuntime",
		"shelltool.ShellBackendSandbox",
		"127.0.0.1:8080",
		"ServeDetachedStart",
		"ServeApproval",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("HTTP harness example missing %q", want)
		}
	}
}

func TestHTTPHarnessExampleRefusesNonLoopbackAddress(t *testing.T) {
	cmd := exec.Command("go", "run", "./http-harness-agent", "-addr", "0.0.0.0:8080")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("HTTP harness accepted a non-loopback address")
	}
	if !strings.Contains(string(output), "must be a loopback address") {
		t.Fatalf("unexpected non-loopback error: %s", output)
	}
}

func TestCodeReviewExampleWiresSafetyControls(t *testing.T) {
	data, err := os.ReadFile("code-review-agent/main.go")
	if err != nil {
		t.Fatalf("ReadFile code-review-agent/main.go returned error: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"approvalcli.New(os.Stdin, os.Stderr)",
		"RequireApproval: true",
		"RequireReadBeforeWrite: true",
		"workspacetools.NewSnapshotStore()",
		"WriteRoots:      []string{\".zenforge/generated\"}",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("code review example missing %q", want)
		}
	}
}

func TestRepoRefactorExampleWiresWorkspacePolicy(t *testing.T) {
	data, err := os.ReadFile("repo-refactor-agent/main.go")
	if err != nil {
		t.Fatalf("ReadFile repo-refactor-agent/main.go returned error: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"RequireReadBeforeWrite: true",
		"workspacetools.NewSnapshotStore()",
		"ReadRoots:       []string{\".\"}",
		"WriteRoots:      []string{\".zenforge/generated\"}",
		"RequireApproval: false",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("repo refactor example missing %q", want)
		}
	}
}

// scenarioExamples are the three examples that run end to end against a
// scripted endpoint, so their promises are checked by behaviour rather than by
// scanning source. This test pins what they promise a reader: they can be
// copied into an application that depends on ZenForge the way any other
// dependency is depended on.
var scenarioExamples = []string{"qa-agent", "long-task-agent", "coding-agent"}

// TestScenarioExamplesUseOnlyPublicSDKPaths makes "public SDK paths" a checked
// claim instead of a sentence in a README. Each scenario may import the public
// SDK, and must not reach into this repository's console adapter or its
// internals: an example that quietly imported cli/ or internal/dsh* would stop
// being copyable evidence for an embedder.
func TestScenarioExamplesUseOnlyPublicSDKPaths(t *testing.T) {
	forbidden := []string{
		"github.com/feiyu912/zenforge/cli",
		"github.com/feiyu912/zenforge/cmd/",
		"github.com/feiyu912/zenforge/internal/",
		"github.com/feiyu912/zenforge/webui/",
	}
	for _, name := range scenarioExamples {
		entries, err := os.ReadDir(name)
		if err != nil {
			t.Fatalf("scenario example %s is missing: %v", name, err)
		}
		found := 0
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			found++
			path := name + "/" + entry.Name()
			for _, imported := range goImports(t, path) {
				for _, prefix := range forbidden {
					base := strings.TrimSuffix(prefix, "/")
					if imported == base || strings.HasPrefix(imported, prefix) {
						t.Errorf("%s imports %q; the scenario examples must use public SDK paths only", path, imported)
					}
				}
				if imported == "github.com/feiyu912/zenforge/examples/internal/modelstub" &&
					!strings.HasSuffix(entry.Name(), "_test.go") {
					t.Errorf("%s imports the scripted endpoint fixture; it is a test double, so only a _test.go file may use it", path)
				}
			}
		}
		if found == 0 {
			t.Errorf("scenario example %s contains no Go source", name)
		}
	}
}

// goImports parses the import list of one Go file.
func goImports(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, strings.Trim(spec.Path.Value, `"`))
	}
	return imports
}

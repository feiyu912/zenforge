package examples_test

import (
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

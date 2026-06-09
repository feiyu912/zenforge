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
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("code review example missing %q", want)
		}
	}
}

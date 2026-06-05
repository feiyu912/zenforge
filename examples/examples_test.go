package examples_test

import (
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

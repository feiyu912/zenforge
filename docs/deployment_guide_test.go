package docs_test

import (
	"os"
	"strings"
	"testing"
)

func TestDeploymentGuideLocksMultiReplicaContract(t *testing.T) {
	raw, err := os.ReadFile("deployment-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")
	for _, required := range []string{
		"RunInfo.OwnerID",
		"RunManager.Cancel",
		"RunCancellationRegistry",
		"RecoverStale",
		"RecoveryOptions",
		"before opening `Agent.Resume`",
		"Last-Event-ID",
		"RunRegistry.Claim",
		"Runtime.Close",
		"tool.Context",
		"runId + toolCallId",
		"Do not place SQLite databases on an arbitrary network",
		"Lease expiry prevents two owners; it does not automatically start recovery",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("deployment guide is missing contract %q", required)
		}
	}
}

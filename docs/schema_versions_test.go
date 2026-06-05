package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
)

func TestDurableSchemaVersionsAreDocumented(t *testing.T) {
	files := []string{
		"checkpoint-resume-guide.md",
		"adr/0007-run-state-schema.md",
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("ReadFile %s returned error: %v", file, err)
		}
		text := string(data)
		if !strings.Contains(text, checkpoint.CheckpointVersion) {
			t.Fatalf("%s does not mention checkpoint schema version %q", file, checkpoint.CheckpointVersion)
		}
		if !strings.Contains(text, harness.RunStateVersion) {
			t.Fatalf("%s does not mention run-state schema version %q", file, harness.RunStateVersion)
		}
	}
}

func TestPublicEventContractDocumentsFlattenedShape(t *testing.T) {
	data, err := os.ReadFile("adr/0002-public-event-contract.md")
	if err != nil {
		t.Fatalf("ReadFile event contract returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{"seq", "type", "timestamp", "runId", "flattened"} {
		if !strings.Contains(text, want) {
			t.Fatalf("event contract does not mention %q", want)
		}
	}
}

package cli

import (
	"os"
	"strings"
	"testing"
)

func TestVersionMatchesVersionFile(t *testing.T) {
	data, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatalf("ReadFile VERSION returned error: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != Version {
		t.Fatalf("VERSION = %q, cli.Version = %q", got, Version)
	}
}

func TestReleaseNotesMentionVersion(t *testing.T) {
	data, err := os.ReadFile("../docs/release-notes-v0.1.md")
	if err != nil {
		t.Fatalf("ReadFile release notes returned error: %v", err)
	}
	want := "V" + strings.TrimSpace(Version)
	if !strings.Contains(string(data), want) {
		t.Fatalf("release notes do not mention %q", want)
	}
}

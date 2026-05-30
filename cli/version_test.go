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

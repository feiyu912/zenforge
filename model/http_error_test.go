package model

import (
	"strings"
	"testing"
)

func TestHTTPStatusErrorSanitizesEndpointAndGuidesAuthentication(t *testing.T) {
	err := NewHTTPStatusError(
		"openai", "chat completion", "https://user:password@example.test/v1/chat/completions?token=query-secret#fragment",
		401, "401 Unauthorized", "bad",
	)

	message := err.Error()
	if !strings.Contains(message, "https://example.test/v1/chat/completions") || !strings.Contains(message, "authentication failed") {
		t.Fatalf("error = %q", message)
	}
	if strings.Contains(message, "user:password") || strings.Contains(message, "query-secret") || strings.Contains(message, "fragment") {
		t.Fatalf("error leaked endpoint credentials: %q", message)
	}
}

func TestRedactSecretLeavesTextUnchangedForEmptySecret(t *testing.T) {
	if got := RedactSecret("provider error", ""); got != "provider error" {
		t.Fatalf("RedactSecret = %q", got)
	}
}

package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestValueNeverAppearsInFormattedOutput(t *testing.T) {
	secret := New("sk-live-do-not-log")
	const raw = "sk-live-do-not-log"

	rendered := []string{
		fmt.Sprintf("%v", secret),
		fmt.Sprintf("%s", secret),
		fmt.Sprintf("%#v", secret),
		fmt.Sprintf("%q", secret),
		fmt.Sprintf("%v", []any{secret}),
		fmt.Sprintf("%v", map[string]any{"key": secret}),
		fmt.Sprintf("%+v", struct{ Key any }{Key: secret}),
		secret.String(),
		secret.GoString(),
	}
	for _, text := range rendered {
		if strings.Contains(text, raw) {
			t.Fatalf("formatted output leaked the secret: %q", text)
		}
	}
	if secret.Reveal() != raw {
		t.Fatalf("Reveal = %q, want the secret", secret.Reveal())
	}
}

func TestLogValueRedactsStructuredLogs(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	logger.Info("calling provider", "api_key", New("sk-logged"))

	if strings.Contains(buffer.String(), "sk-logged") {
		t.Fatalf("slog leaked the secret: %q", buffer.String())
	}
	if !strings.Contains(buffer.String(), Placeholder) {
		t.Fatalf("slog output = %q, want the placeholder", buffer.String())
	}
}

func TestJSONRoundTripsTransparently(t *testing.T) {
	type document struct {
		APIKey String `json:"apiKey,omitempty"`
	}
	data, err := json.Marshal(document{APIKey: New("sk-round-trip")})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if !strings.Contains(string(data), `"apiKey":"sk-round-trip"`) {
		t.Fatalf("Marshal = %s, want the real value so config round-trips", data)
	}
	var decoded document
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded.APIKey.Reveal() != "sk-round-trip" {
		t.Fatalf("round-trip = %q", decoded.APIKey.Reveal())
	}

	// An empty secret is omitted, so config printing gains no noise.
	empty, err := json.Marshal(document{})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if strings.Contains(string(empty), "apiKey") {
		t.Fatalf("empty secret was serialized: %s", empty)
	}
}

func TestUnmarshalAcceptsNullAndRejectsOtherTypes(t *testing.T) {
	type document struct {
		APIKey String `json:"apiKey"`
	}
	var decoded document
	if err := json.Unmarshal([]byte(`{"apiKey":null}`), &decoded); err != nil {
		t.Fatalf("Unmarshal(null) returned error: %v", err)
	}
	if !decoded.APIKey.IsZero() {
		t.Fatalf("null decoded to %q", decoded.APIKey.Reveal())
	}
	if err := json.Unmarshal([]byte(`{"apiKey":42}`), &decoded); err == nil {
		t.Fatal("numeric secret was accepted")
	}
}

func TestDescribeReportsPresenceOnly(t *testing.T) {
	if got := New("").Describe(); got != "unset" {
		t.Fatalf("Describe(empty) = %q", got)
	}
	if got := New("sk-secret").Describe(); got != "set" {
		t.Fatalf("Describe(set) = %q, want presence only", got)
	}
}

package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestJSONSinkWritesEventLine(t *testing.T) {
	var buf bytes.Buffer
	sink := NewJSONSink(&buf)

	if err := sink.Emit(context.Background(), Event{
		Type:      "run.started",
		RunID:     "run_123",
		Timestamp: 42,
		Data:      map[string]any{"input": "hello"},
	}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	var got Event
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("trace JSON did not decode: %v", err)
	}
	if got.Type != "run.started" || got.RunID != "run_123" || got.Data["input"] != "hello" {
		t.Fatalf("unexpected trace event: %#v", got)
	}
}

func TestMemorySinkCopiesEvents(t *testing.T) {
	sink := NewMemorySink()
	event := Event{
		Type:  "tool.call",
		RunID: "run_123",
		Data:  map[string]any{"toolName": "echo"},
	}

	if err := sink.Emit(context.Background(), event); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	event.Data["toolName"] = "mutated"

	got := sink.Events()
	if len(got) != 1 {
		t.Fatalf("event count = %d, want 1", len(got))
	}
	if got[0].Data["toolName"] != "echo" {
		t.Fatalf("memory sink did not copy event data: %#v", got[0])
	}
	got[0].Data["toolName"] = "changed again"
	if sink.Events()[0].Data["toolName"] != "echo" {
		t.Fatalf("Events returned mutable backing data")
	}
}

func TestRedactSinkRedactsSensitiveKeys(t *testing.T) {
	memory := NewMemorySink()
	sink := Redact(memory)

	event := Event{
		Type:  "tool.result",
		RunID: "run_123",
		Data: map[string]any{
			"apiKey":        "sk-secret",
			"Authorization": "Bearer token",
			"nested": map[string]any{
				"access_token": "secret-token",
				"safe":         "visible",
			},
			"items": []any{
				map[string]any{"password": "p@ss"},
				"plain",
			},
		},
	}

	if err := sink.Emit(context.Background(), event); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	got := memory.Events()[0]
	if got.Data["apiKey"] != "[REDACTED]" || got.Data["Authorization"] != "[REDACTED]" {
		t.Fatalf("top-level secrets were not redacted: %#v", got.Data)
	}
	nested := got.Data["nested"].(map[string]any)
	if nested["access_token"] != "[REDACTED]" || nested["safe"] != "visible" {
		t.Fatalf("nested redaction mismatch: %#v", nested)
	}
	items := got.Data["items"].([]any)
	item := items[0].(map[string]any)
	if item["password"] != "[REDACTED]" || items[1] != "plain" {
		t.Fatalf("slice redaction mismatch: %#v", items)
	}
	if event.Data["apiKey"] != "sk-secret" {
		t.Fatalf("redaction mutated source event: %#v", event.Data)
	}
}

func TestRedactWithCustomKeysAndReplacement(t *testing.T) {
	memory := NewMemorySink()
	sink := RedactWith(memory, Redactor{
		Keys:        []string{"session_id"},
		Replacement: "***",
	})

	if err := sink.Emit(context.Background(), Event{
		Type:  "run.started",
		RunID: "run_123",
		Data:  map[string]any{"sessionId": "abc", "token": "kept"},
	}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	got := memory.Events()[0]
	if got.Data["sessionId"] != "***" {
		t.Fatalf("custom key was not redacted: %#v", got.Data)
	}
	if got.Data["token"] != "kept" {
		t.Fatalf("unexpected default key redaction with custom keys: %#v", got.Data)
	}
}

func TestWithFieldsAddsStaticPlatformMetadata(t *testing.T) {
	memory := NewMemorySink()
	fields := map[string]any{
		"tenantId":  "tenant_1",
		"sessionId": "session_abc",
		"service":   "api",
	}
	sink := WithFields(memory, fields)
	event := Event{
		Type:  "run.started",
		RunID: "run_123",
		Data:  map[string]any{"input": "hello", "service": "old"},
	}

	if err := sink.Emit(context.Background(), event); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	fields["tenantId"] = "mutated"
	event.Data["input"] = "mutated"

	got := memory.Events()[0]
	if got.Data["tenantId"] != "tenant_1" || got.Data["sessionId"] != "session_abc" || got.Data["service"] != "api" {
		t.Fatalf("static fields were not injected with precedence: %#v", got.Data)
	}
	if got.Data["input"] != "hello" {
		t.Fatalf("event data was not preserved: %#v", got.Data)
	}
}

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

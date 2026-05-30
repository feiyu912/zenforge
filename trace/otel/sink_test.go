package otel

import (
	"context"
	"testing"

	ztrace "github.com/feiyu912/zenforge/trace"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSinkEmitsSpanWithAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	sink := New(provider.Tracer("test"))

	err := sink.Emit(context.Background(), ztrace.Event{
		Type:      "tool.call",
		RunID:     "run_1",
		Seq:       7,
		Timestamp: 1234,
		Data: map[string]any{
			"toolName":  "workspace_grep",
			"arguments": map[string]any{"pattern": "TODO"},
		},
	})
	if err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "zenforge.tool.call" {
		t.Fatalf("span name = %q", span.Name)
	}
	if got := stringValue(span.Attributes, "zenforge.run_id"); got != "run_1" {
		t.Fatalf("run_id attr = %q", got)
	}
	if got := int64Value(span.Attributes, "zenforge.seq"); got != 7 {
		t.Fatalf("seq attr = %d", got)
	}
	if got := stringValue(span.Attributes, "zenforge.data.toolName"); got != "workspace_grep" {
		t.Fatalf("toolName attr = %q", got)
	}
	if got := stringValue(span.Attributes, "zenforge.data.arguments"); got != `{"pattern":"TODO"}` {
		t.Fatalf("arguments attr = %q", got)
	}
}

func TestSinkHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := New(nil).Emit(ctx, ztrace.Event{Type: "run.started"})
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func stringValue(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func int64Value(attrs []attribute.KeyValue, key string) int64 {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsInt64()
		}
	}
	return 0
}

package sse

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestWriteFormatsEvent(t *testing.T) {
	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_123", map[string]any{
		"textDelta": "hello",
	}).WithSeq(7)
	event.Timestamp = 42

	var buf bytes.Buffer
	if err := Write(&buf, event); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "id: 7\n") {
		t.Fatalf("SSE event missing id: %q", got)
	}
	if !strings.Contains(got, "event: model.delta\n") {
		t.Fatalf("SSE event missing event type: %q", got)
	}
	if !strings.Contains(got, `"runId":"run_123"`) || !strings.Contains(got, `"textDelta":"hello"`) {
		t.Fatalf("SSE data missing event payload: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("SSE event must end with blank line: %q", got)
	}
}

func TestStreamWritesRetryAndEvents(t *testing.T) {
	events := make(chan zenforge.Event, 2)
	events <- zenforge.NewEvent(zenforge.EventRunStarted, "run_123", map[string]any{"input": "hi"}).WithSeq(1)
	events <- zenforge.NewEvent(zenforge.EventRunDone, "run_123", map[string]any{"output": "done"}).WithSeq(2)
	close(events)

	var buf bytes.Buffer
	if err := Stream(context.Background(), &buf, events, Options{RetryMillis: 1500}); err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	got := buf.String()
	if !strings.HasPrefix(got, "retry: 1500\n\n") {
		t.Fatalf("SSE stream missing retry line: %q", got)
	}
	if strings.Count(got, "event: ") != 2 {
		t.Fatalf("SSE stream event count mismatch: %q", got)
	}
}

func TestStreamReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Stream(ctx, &bytes.Buffer{}, make(chan zenforge.Event), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream error = %v, want context.Canceled", err)
	}
}

func TestStreamHTTPHeaders(t *testing.T) {
	events := make(chan zenforge.Event)
	close(events)
	rec := httptest.NewRecorder()

	if err := StreamHTTP(context.Background(), rec, events, Options{}); err != nil {
		t.Fatalf("StreamHTTP returned error: %v", err)
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("Connection"); got != "keep-alive" {
		t.Fatalf("Connection = %q, want keep-alive", got)
	}
}

func TestEventNameStripsNewlines(t *testing.T) {
	if got := eventName(zenforge.EventType("bad\nevent\rname")); got != "badeventname" {
		t.Fatalf("eventName = %q", got)
	}
}

var _ http.Flusher = httptest.NewRecorder()

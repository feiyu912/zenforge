package trace

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
)

type Sink interface {
	Emit(ctx context.Context, event Event) error
}

type SinkFunc func(ctx context.Context, event Event) error

func (f SinkFunc) Emit(ctx context.Context, event Event) error {
	return f(ctx, event)
}

type Event struct {
	Type      string         `json:"type"`
	RunID     string         `json:"runId"`
	Seq       int64          `json:"seq,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Redactor describes key-based trace redaction.
type Redactor struct {
	Keys        []string
	Replacement string
}

// DefaultRedactor returns conservative defaults for common secret-bearing keys.
func DefaultRedactor() Redactor {
	return Redactor{
		Keys: []string{
			"api_key",
			"apikey",
			"authorization",
			"password",
			"secret",
			"token",
		},
		Replacement: "[REDACTED]",
	}
}

// Redact wraps a sink with default secret redaction.
func Redact(next Sink) Sink {
	return RedactWith(next, DefaultRedactor())
}

// RedactWith wraps a sink with custom key-based redaction.
func RedactWith(next Sink, redactor Redactor) Sink {
	if next == nil {
		next = Discard()
	}
	if redactor.Replacement == "" {
		redactor.Replacement = "[REDACTED]"
	}
	if len(redactor.Keys) == 0 {
		redactor.Keys = DefaultRedactor().Keys
	}
	return SinkFunc(func(ctx context.Context, event Event) error {
		return next.Emit(ctx, redactor.Event(event))
	})
}

// WithFields wraps a sink and adds static fields to every trace event.
func WithFields(next Sink, fields map[string]any) Sink {
	if next == nil {
		next = Discard()
	}
	fields = cloneMap(fields)
	return SinkFunc(func(ctx context.Context, event Event) error {
		enriched := cloneEvent(event)
		if len(fields) > 0 {
			if enriched.Data == nil {
				enriched.Data = map[string]any{}
			}
			for key, value := range fields {
				enriched.Data[key] = value
			}
		}
		return next.Emit(ctx, enriched)
	})
}

// Event returns a redacted copy of event.
func (r Redactor) Event(event Event) Event {
	event.Data = r.Map(event.Data)
	return event
}

// Map returns a redacted copy of in.
func (r Redactor) Map(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if r.sensitiveKey(key) {
			out[key] = r.replacement()
			continue
		}
		out[key] = r.value(value)
	}
	return out
}

func Discard() Sink {
	return SinkFunc(func(ctx context.Context, event Event) error {
		return ctx.Err()
	})
}

func Stdout() Sink {
	return NewJSONSink(os.Stdout)
}

type JSONSink struct {
	mu sync.Mutex
	w  io.Writer
}

func NewJSONSink(w io.Writer) *JSONSink {
	return &JSONSink{w: w}
}

func (s *JSONSink) Emit(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.w == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	encoder := json.NewEncoder(s.w)
	return encoder.Encode(event)
}

type MemorySink struct {
	mu     sync.RWMutex
	events []Event
}

func NewMemorySink() *MemorySink {
	return &MemorySink{}
}

func (s *MemorySink) Emit(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, cloneEvent(event))
	return nil
}

func (s *MemorySink) Events() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, 0, len(s.events))
	for _, event := range s.events {
		out = append(out, cloneEvent(event))
	}
	return out
}

func cloneEvent(event Event) Event {
	event.Data = cloneMap(event.Data)
	return event
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (r Redactor) value(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return r.Map(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = r.value(item)
		}
		return out
	default:
		return value
	}
}

func (r Redactor) sensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	for _, candidate := range r.Keys {
		if candidate == "" {
			continue
		}
		if strings.Contains(normalized, normalizeKey(candidate)) {
			return true
		}
	}
	return false
}

func (r Redactor) replacement() string {
	if r.Replacement == "" {
		return "[REDACTED]"
	}
	return r.Replacement
}

func normalizeKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, "-", "")
	key = strings.ReplaceAll(key, ".", "")
	return key
}

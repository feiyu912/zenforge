package trace

import (
	"context"
	"encoding/json"
	"io"
	"os"
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

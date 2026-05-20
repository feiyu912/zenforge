package trace

import "context"

type Sink interface {
	Emit(ctx context.Context, event Event) error
}

type Event struct {
	Type  string
	RunID string
	Data  map[string]any
}


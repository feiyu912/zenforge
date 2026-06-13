package tool

import (
	"context"
	"encoding/json"
	"time"
)

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Call(ctx context.Context, input json.RawMessage, call Context) (Result, error)
}

type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type Call struct {
	ID                   string          `json:"id"`
	RunID                string          `json:"runId"`
	Name                 string          `json:"name"`
	Arguments            json.RawMessage `json:"arguments"`
	Metadata             map[string]any  `json:"metadata,omitempty"`
	RedactedArgumentKeys []string        `json:"-"`
}

type Context struct {
	RunID      string
	Step       int
	ToolCallID string
	Deadline   time.Time
	Metadata   map[string]any
	Meta       map[string]any
}

type Result struct {
	Output     string `json:"output,omitempty"`
	Structured map[string]any
	Error      string         `json:"error,omitempty"`
	ExitCode   int            `json:"exitCode"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type Registry interface {
	Register(tool Tool) error
	Lookup(name string) (Tool, bool)
	Definitions() []Definition
}

type Invoker interface {
	Invoke(ctx context.Context, call Call) (Result, error)
}

type InvokerFunc func(ctx context.Context, call Call) (Result, error)

func (f InvokerFunc) Invoke(ctx context.Context, call Call) (Result, error) {
	return f(ctx, call)
}

type Middleware func(Invoker) Invoker

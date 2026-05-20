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

type Context struct {
	RunID     string
	ToolCallID string
	Deadline  time.Time
	Meta      map[string]any
}

type Result struct {
	Output     string
	Structured map[string]any
	Error      string
	ExitCode   int
	Meta       map[string]any
}


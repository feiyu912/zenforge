package model

import (
	"context"
	"encoding/json"
	"time"
)

type Model interface {
	Generate(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

type Request struct {
	Messages   []Message
	Tools      []ToolSpec
	ToolChoice ToolChoice
	Meta       map[string]any
	// OutputSchema, when set, asks the provider to constrain the final
	// response to a JSON Schema (codex exec --output-schema). Adapters
	// that cannot enforce a schema fail with
	// ErrUnsupportedOutputSchema instead of silently ignoring it.
	OutputSchema map[string]any
	// OutputSchemaName labels the schema for providers that require a
	// name. Empty selects "final_output".
	OutputSchemaName string
	// OutputSchemaStrict requests strict provider validation. It only
	// applies when OutputSchema is set.
	OutputSchemaStrict bool
}

// DefaultOutputSchemaName labels a schema when the caller does not.
const DefaultOutputSchemaName = "final_output"

type Response struct {
	Message Message
	Usage   Usage
	Meta    map[string]any
}

type Event struct {
	Type      EventType
	Delta     string
	Message   *Message
	ToolCalls []ToolCallSpec
	Usage     Usage
	Error     error
	Meta      map[string]any
}

type EventType string

const (
	EventDelta EventType = "delta"
	EventUsage EventType = "usage"
	EventDone  EventType = "done"
	EventError EventType = "error"
)

type Message struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []ToolCallSpec
}

type ToolCallSpec struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// RateLimits carries the provider-reported rate-limit snapshot for
	// the call that produced this usage, normalized by the adapter from
	// HTTP response headers. Nil when the provider reported none.
	RateLimits *RateLimit
}

// RateLimit is a provider rate-limit snapshot. Zero fields mean the
// provider did not report that dimension; adapters normalize their own
// header formats into this shared shape.
type RateLimit struct {
	RequestsLimit     int
	RequestsRemaining int
	RequestsReset     time.Duration
	TokensLimit       int
	TokensRemaining   int
	TokensReset       time.Duration
}

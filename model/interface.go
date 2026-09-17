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
	// EventReasoning carries a reasoning delta. It is separate from
	// EventDelta so a caller can render or log reasoning without treating
	// it as answer text.
	EventReasoning EventType = "reasoning"
)

type Message struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []ToolCallSpec
	// Images carry image content alongside the text. A message with images
	// is sent as a multipart message; an adapter that cannot express images
	// fails loudly rather than dropping them, because a silently missing
	// image is an answer about something the model never saw.
	Images []Image
	// Reasoning is the model's own reasoning text for this turn, kept so
	// the transcript records what the model thought. It is replayed only by
	// adapters that require it (Anthropic thinking blocks, which must be
	// returned with their signature).
	Reasoning string
	// ReasoningSignature is the provider's signature over Reasoning. An
	// adapter that replays reasoning must replay the signature verbatim;
	// without it the provider rejects the block, so the pair travels
	// together.
	ReasoningSignature string
}

// Image is one image attached to a message.
type Image struct {
	// MediaType is the IANA media type, e.g. "image/png".
	MediaType string `json:"mediaType"`
	// Data is the encoded image bytes. It marshals as base64, which is how
	// it survives a durable checkpoint.
	Data []byte `json:"data"`
	// Path is where the image came from, for events and errors. It is not
	// sent to the provider.
	Path string `json:"path,omitempty"`
	// Detail is an optional provider hint ("low"/"high"/"auto").
	Detail string `json:"detail,omitempty"`
}

// MaxImageBytes bounds one image. An image is re-sent on every later request
// in the conversation, so a huge one would silently consume the context
// window on each turn.
const MaxImageBytes = 8 << 20

// SupportedImageMediaType reports whether a media type may be sent to a
// provider. Only formats every supported provider accepts are allowed, so a
// run cannot come to depend on one vendor's format.
func SupportedImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
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

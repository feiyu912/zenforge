package model

import (
	"context"
	"encoding/json"
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
}

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
}

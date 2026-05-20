package model

import "context"

type Model interface {
	Generate(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

type Request struct {
	Messages []Message
	Tools    []ToolSpec
	Meta     map[string]any
}

type Response struct {
	Message Message
	Usage   Usage
	Meta    map[string]any
}

type Event struct {
	Type  string
	Delta string
	Meta  map[string]any
}

type Message struct {
	Role    string
	Content string
}

type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}


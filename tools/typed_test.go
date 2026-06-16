package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/feiyu912/zenforge/tool"
)

func TestTypedToolCallsStructHandler(t *testing.T) {
	type input struct {
		Query string `json:"query" jsonschema:"required"`
		Limit int    `json:"limit,omitempty"`
	}
	type output struct {
		Results []string `json:"results"`
	}

	search, err := New("search", "Search docs", func(ctx context.Context, in input) (output, error) {
		return output{Results: []string{in.Query}}, nil
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	result, err := search.Call(context.Background(), json.RawMessage(`{"query":"zen"}`), tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result.Output != `{"results":["zen"]}` {
		t.Fatalf("unexpected output: %s", result.Output)
	}
	if result.Structured["results"] == nil {
		t.Fatalf("expected structured output: %#v", result.Structured)
	}
}

func TestTypedToolSupportsStringAndErrorOnlyHandlers(t *testing.T) {
	stringTool := Must("string", "String", func(ctx context.Context, in struct{}) (string, error) {
		return "ok", nil
	})
	result, err := stringTool.Call(context.Background(), json.RawMessage(`{}`), tool.Context{})
	if err != nil || result.Output != "ok" {
		t.Fatalf("unexpected string result=%#v err=%v", result, err)
	}

	errorOnly := Must("noop", "Noop", func(ctx context.Context, in struct{}) error {
		return nil
	})
	result, err = errorOnly.Call(context.Background(), json.RawMessage(`{}`), tool.Context{})
	if err != nil || result.Output != "" || result.Error != "" {
		t.Fatalf("unexpected error-only result=%#v err=%v", result, err)
	}
}

func TestTypedToolSupportsToolContextHandler(t *testing.T) {
	contextual := Must("contextual", "Contextual", func(ctx context.Context, in struct {
		Value string `json:"value"`
	}, call tool.Context) (string, error) {
		return call.RunID + ":" + in.Value, nil
	})
	result, err := contextual.Call(context.Background(), json.RawMessage(`{"value":"ok"}`), tool.Context{RunID: "run_1"})
	if err != nil || result.Output != "run_1:ok" {
		t.Fatalf("contextual result=%#v err=%v", result, err)
	}
}

func TestTypedToolRejectsUnknownFields(t *testing.T) {
	typed := Must("strict", "Strict", func(ctx context.Context, in struct {
		Query string `json:"query"`
	}) (string, error) {
		return in.Query, nil
	})
	result, err := typed.Call(context.Background(), json.RawMessage(`{"query":"ok","extra":true}`), tool.Context{})
	if !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got result=%#v err=%v", result, err)
	}
	if result.ExitCode == 0 || result.Error == "" {
		t.Fatalf("expected invalid argument result, got %#v", result)
	}
}

func TestTypedToolRejectsUnsupportedSignature(t *testing.T) {
	_, err := New("bad", "Bad", func(in string) string {
		return in
	})
	if !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("expected ErrInvalidTool, got %v", err)
	}
}

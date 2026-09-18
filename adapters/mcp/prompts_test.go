package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// promptServer builds a server with one prompt that takes a required
// argument, one that takes none, and one that panics.
func promptServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []ServerTool{{
			Name: "noop",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				return CallResult{}, nil
			},
		}},
		Prompts: []ServerPrompt{
			{
				Name:        "review",
				Description: "Review the working tree",
				Arguments: []PromptArgument{
					{Name: "path", Description: "the file to review", Required: true},
				},
				Handler: func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
					if arguments["path"] == "" {
						return PromptResult{}, errors.New("the handler got no path")
					}
					return PromptResult{Messages: []PromptMessage{{
						Role:    PromptRoleUser,
						Content: Content{Type: "text", Text: "Review " + arguments["path"]},
					}}}, nil
				},
			},
			{
				Name: "plain",
				Handler: func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
					return PromptResult{Messages: []PromptMessage{{
						Role:    PromptRoleUser,
						Content: Content{Type: "text", Text: "plain prompt"},
					}}}, nil
				},
			},
			{
				Name: "panic",
				Handler: func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
					panic("boom")
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

func TestServerListsPromptsWithArguments(t *testing.T) {
	server := promptServer(t)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`))
	prompts, ok := result["prompts"].([]any)
	if !ok || len(prompts) != 3 {
		t.Fatalf("prompts = %v", result["prompts"])
	}
	first := prompts[0].(map[string]any)
	if first["name"] != "review" || first["description"] != "Review the working tree" {
		t.Fatalf("the first prompt is %v", first)
	}
	arguments, ok := first["arguments"].([]any)
	if !ok || len(arguments) != 1 {
		t.Fatalf("arguments = %v", first["arguments"])
	}
	argument := arguments[0].(map[string]any)
	if argument["name"] != "path" || argument["required"] != true || argument["description"] != "the file to review" {
		t.Fatalf("arguments[0] = %v", argument)
	}
	// A prompt with no arguments must not carry an empty list, so a client can
	// tell "takes none" from "the server forgot to say".
	second := prompts[1].(map[string]any)
	if _, ok := second["arguments"]; ok {
		t.Fatalf("an argument-less prompt carried arguments: %v", second)
	}
}

func TestServerGetsAPromptWithArguments(t *testing.T) {
	server := promptServer(t)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"review","arguments":{"path":"main.go"}}}`))
	if result["description"] != nil {
		t.Fatalf("the prompt invented a description: %v", result["description"])
	}
	messages, ok := result["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v", result["messages"])
	}
	message := messages[0].(map[string]any)
	if message["role"] != PromptRoleUser {
		t.Fatalf("role = %v", message["role"])
	}
	content, ok := message["content"].(map[string]any)
	if !ok || content["type"] != "text" || content["text"] != "Review main.go" {
		t.Fatalf("content = %v", message["content"])
	}
	// A prompt with no declared arguments renders without any.
	result = resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"plain"}}`))
	content = result["messages"].([]any)[0].(map[string]any)["content"].(map[string]any)
	if content["text"] != "plain prompt" {
		t.Fatalf("content = %v", content)
	}
}

func TestServerReportsPromptErrorsAsInvalidParams(t *testing.T) {
	server := promptServer(t)
	cases := []struct {
		name    string
		message string
	}{
		{"unknown prompt", `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"missing"}}`},
		{"missing name", `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{}}`},
		{"missing required argument", `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"review"}}`},
		{"empty required argument", `{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"review","arguments":{"path":"  "}}}`},
		{"bad params", `{"jsonrpc":"2.0","id":5,"method":"prompts/get","params":"not-an-object"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			errValue := errorOf(t, call(t, server, testCase.message))
			if errValue["code"] != float64(codeInvalidParams) {
				t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeInvalidParams, errValue)
			}
			if message, _ := errValue["message"].(string); message == "" {
				t.Fatalf("the error has no message: %v", errValue)
			}
		})
	}
	// An unknown method stays method-not-found: prompts/get with a real name
	// is the only thing this server answers for prompts.
	errValue := errorOf(t, call(t, server, `{"jsonrpc":"2.0","id":6,"method":"prompts/nope"}`))
	if errValue["code"] != float64(codeMethodNotFound) {
		t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeMethodNotFound, errValue)
	}
}

func TestServerSurvivesAPromptHandlerPanic(t *testing.T) {
	server := promptServer(t)
	errValue := errorOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"panic"}}`))
	if errValue["code"] != float64(codeInternalError) {
		t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeInternalError, errValue)
	}
	if message, _ := errValue["message"].(string); !strings.Contains(message, "panicked") {
		t.Fatalf("the panic was not reported: %v", errValue)
	}
	// The stream is alive: the next request is answered normally.
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"plain"}}`))
	if _, ok := result["messages"].([]any); !ok {
		t.Fatalf("the server did not answer after a panic: %v", result)
	}
}

func TestNewServerRejectsBadPromptConfigurations(t *testing.T) {
	handler := func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
		return PromptResult{}, nil
	}
	tool := ServerTool{
		Name: "noop",
		Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
			return CallResult{}, nil
		},
	}
	cases := []struct {
		name   string
		config ServerConfig
	}{
		{"unnamed prompt", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Prompts: []ServerPrompt{{Handler: handler}}}},
		{"handler missing", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Prompts: []ServerPrompt{{Name: "review"}}}},
		{"duplicate prompt", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Prompts: []ServerPrompt{{Name: "review", Handler: handler}, {Name: "review", Handler: handler}}}},
		{"unnamed argument", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Prompts: []ServerPrompt{{Name: "review", Handler: handler, Arguments: []PromptArgument{{Name: " "}}}}}},
		{"duplicate argument", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Prompts: []ServerPrompt{{Name: "review", Handler: handler, Arguments: []PromptArgument{{Name: "path"}, {Name: "path"}}}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewServer(testCase.config); err == nil {
				t.Fatal("an invalid configuration was accepted")
			}
		})
	}
}

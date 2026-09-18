package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// resourceServer builds a server with one exact resource, one template
// resource, and one that panics, so the read path can be exercised without
// any of the CLI's run plumbing.
func resourceServer(t *testing.T) *Server {
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
		Resources: []ServerResource{
			{
				URI:         "zenforge://runs",
				Name:        "Recorded runs",
				Description: "An index",
				MimeType:    "application/json",
				Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
					if uri != "zenforge://runs" {
						return nil, fmt.Errorf("the handler was called with %q", uri)
					}
					return []ResourceContent{{URI: uri, MimeType: "application/json", Text: `[{"runId":"run_1"}]`}}, nil
				},
			},
			{
				URI:      "zenforge://runs/{runId}",
				Name:     "Recorded run",
				MimeType: "application/json",
				Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
					runID := strings.TrimPrefix(uri, "zenforge://runs/")
					if runID != "run_1" {
						return nil, fmt.Errorf("no recorded run %q: %w", runID, ErrResourceNotFound)
					}
					return []ResourceContent{{URI: uri, MimeType: "application/json", Text: `{"runId":"run_1"}`}}, nil
				},
			},
			{
				URI:  "zenforge://panic",
				Name: "Panicking resource",
				Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
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

func TestServerListsResources(t *testing.T) {
	server := resourceServer(t)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`))
	resources, ok := result["resources"].([]any)
	if !ok || len(resources) != 3 {
		t.Fatalf("resources = %v", result["resources"])
	}
	first := resources[0].(map[string]any)
	if first["uri"] != "zenforge://runs" || first["name"] != "Recorded runs" ||
		first["description"] != "An index" || first["mimeType"] != "application/json" {
		t.Fatalf("the first resource is %v", first)
	}
	// A resource that declared no description must not invent one.
	third := resources[2].(map[string]any)
	if _, ok := third["description"]; ok {
		t.Fatalf("an undescribed resource claimed a description: %v", third)
	}
}

func TestServerReadsResourcesAndTemplates(t *testing.T) {
	server := resourceServer(t)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"zenforge://runs"}}`))
	contents, ok := result["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %v", result["contents"])
	}
	item := contents[0].(map[string]any)
	if item["uri"] != "zenforge://runs" || item["text"] != `[{"runId":"run_1"}]` || item["mimeType"] != "application/json" {
		t.Fatalf("contents[0] = %v", item)
	}
	// The template is registered once but serves a concrete instance: the
	// handler receives the URI the client asked for, not the pattern, which is
	// what lets one registration answer for every run.
	result = resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"zenforge://runs/run_1"}}`))
	item = result["contents"].([]any)[0].(map[string]any)
	if item["uri"] != "zenforge://runs/run_1" || item["text"] != `{"runId":"run_1"}` {
		t.Fatalf("templated contents[0] = %v", item)
	}
}

func TestServerReportsUnknownResourcesAsResourceNotFound(t *testing.T) {
	server := resourceServer(t)
	for _, uri := range []string{
		"zenforge://missing",      // nothing registered for it
		"zenforge://runs/run_1/x", // a template matches one segment, not a path
		"zenforge://runs/",        // the placeholder must be non-empty
	} {
		t.Run(uri, func(t *testing.T) {
			errValue := errorOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"`+uri+`"}}`))
			if errValue["code"] != float64(codeResourceNotFound) {
				t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeResourceNotFound, errValue)
			}
		})
	}
	// A matched handler that cannot find the instance reports the same code,
	// so a client sees one answer for "no such resource" however it got there.
	errValue := errorOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"zenforge://runs/run_missing"}}`))
	if errValue["code"] != float64(codeResourceNotFound) {
		t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeResourceNotFound, errValue)
	}
	// A missing or malformed request is the client's bug, not a missing
	// resource, so it stays invalid params.
	for _, message := range []string{
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":"not-an-object"}`,
	} {
		errValue := errorOf(t, call(t, server, message))
		if errValue["code"] != float64(codeInvalidParams) {
			t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeInvalidParams, errValue)
		}
	}
}

// capabilitiesOf decodes the capability block of an initialize response.
func capabilitiesOf(t *testing.T, server *Server) map[string]any {
	t.Helper()
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %v", result["capabilities"])
	}
	return capabilities
}

func TestServerAdvertisesCapabilitiesOnlyWhenServed(t *testing.T) {
	tool := ServerTool{
		Name: "noop",
		Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
			return CallResult{}, nil
		},
	}
	resource := ServerResource{
		URI: "zenforge://runs",
		Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
			return nil, nil
		},
	}
	prompt := ServerPrompt{
		Name: "review",
		Handler: func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
			return PromptResult{}, nil
		},
	}
	newServer := func(t *testing.T, config ServerConfig) *Server {
		t.Helper()
		config.Name = "zenforge"
		config.Version = "test"
		config.Tools = []ServerTool{tool}
		server, err := NewServer(config)
		if err != nil {
			t.Fatalf("NewServer returned error: %v", err)
		}
		return server
	}
	// A server with only tools advertises only tools: a capability with
	// nothing behind it would send a client to a method that cannot help.
	capabilities := capabilitiesOf(t, newServer(t, ServerConfig{}))
	if _, ok := capabilities["resources"]; ok {
		t.Fatalf("a server with no resources advertised them: %v", capabilities)
	}
	if _, ok := capabilities["prompts"]; ok {
		t.Fatalf("a server with no prompts advertised them: %v", capabilities)
	}
	// One resource turns on resources, and nothing else.
	capabilities = capabilitiesOf(t, newServer(t, ServerConfig{Resources: []ServerResource{resource}}))
	resources, ok := capabilities["resources"].(map[string]any)
	if !ok || resources["subscribe"] != false || resources["listChanged"] != false {
		t.Fatalf("resources capability = %v", capabilities["resources"])
	}
	if _, ok := capabilities["prompts"]; ok {
		t.Fatalf("a server with no prompts advertised them: %v", capabilities)
	}
	// One prompt turns on prompts, and nothing else.
	capabilities = capabilitiesOf(t, newServer(t, ServerConfig{Prompts: []ServerPrompt{prompt}}))
	prompts, ok := capabilities["prompts"].(map[string]any)
	if !ok || prompts["listChanged"] != false {
		t.Fatalf("prompts capability = %v", capabilities["prompts"])
	}
	if _, ok := capabilities["resources"]; ok {
		t.Fatalf("a server with no resources advertised them: %v", capabilities)
	}
}

func TestServerSurvivesAResourceHandlerPanic(t *testing.T) {
	server := resourceServer(t)
	errValue := errorOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"zenforge://panic"}}`))
	if errValue["code"] != float64(codeInternalError) {
		t.Fatalf("code = %v, want %v (%v)", errValue["code"], codeInternalError, errValue)
	}
	if message, _ := errValue["message"].(string); !strings.Contains(message, "panicked") {
		t.Fatalf("the panic was not reported: %v", errValue)
	}
	// The stream is alive: the next request is answered normally, which is
	// what a peer needs instead of a closed pipe.
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"zenforge://runs"}}`))
	if _, ok := result["contents"].([]any); !ok {
		t.Fatalf("the server did not answer after a panic: %v", result)
	}
}

func TestNewServerRejectsBadResourceConfigurations(t *testing.T) {
	handler := func(ctx context.Context, uri string) ([]ResourceContent, error) { return nil, nil }
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
		{"unnamed resource", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Resources: []ServerResource{{Handler: handler}}}},
		{"handler missing", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Resources: []ServerResource{{URI: "zenforge://runs"}}}},
		{"duplicate resource", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{tool}, Resources: []ServerResource{{URI: "zenforge://runs", Handler: handler}, {URI: "zenforge://runs", Handler: handler}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewServer(testCase.config); err == nil {
				t.Fatal("an invalid configuration was accepted")
			}
		})
	}
}

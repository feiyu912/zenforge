package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Name:         "zenforge",
		Version:      "test",
		Instructions: "Be careful.",
		Tools: []ServerTool{
			{
				Name:        "read_file",
				Description: "Read a file",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
				ReadOnly:    true,
				Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
					var input struct {
						Path string `json:"path"`
					}
					if err := json.Unmarshal(arguments, &input); err != nil {
						return CallResult{}, err
					}
					return CallResult{Content: []Content{{Type: "text", Text: "contents of " + input.Path}}}, nil
				},
			},
			{
				Name: "fail",
				Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
					return CallResult{}, errors.New("the tool refused")
				},
			},
			{
				Name: "panic",
				Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
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

// call sends one request and returns the decoded response. It fails the test
// if the server answered a notification or answered one of these with an
// error where a result was expected.
func call(t *testing.T, server *Server, message string) map[string]any {
	t.Helper()
	response, respond := server.Handle(context.Background(), []byte(message))
	if !respond {
		t.Fatalf("the server did not answer %s", message)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("the response is not JSON (%v): %s", err, response)
	}
	return decoded
}

func resultOf(t *testing.T, decoded map[string]any) map[string]any {
	t.Helper()
	if errValue, ok := decoded["error"]; ok {
		t.Fatalf("the server returned an error: %v", errValue)
	}
	result, ok := decoded["result"].(map[string]any)
	if !ok {
		t.Fatalf("the response has no result object: %v", decoded)
	}
	return result
}

func errorOf(t *testing.T, decoded map[string]any) map[string]any {
	t.Helper()
	errValue, ok := decoded["error"].(map[string]any)
	if !ok {
		t.Fatalf("the response has no error object: %v", decoded)
	}
	return errValue
}

func TestServerInitializeNegotiatesTheProtocolVersion(t *testing.T) {
	server := testServer(t)
	// A version the server knows is echoed verbatim.
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"peer","version":"1"}}}`))
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v, want the client's", result["protocolVersion"])
	}
	// An unknown version falls back to the newest this server speaks rather
	// than failing the handshake.
	result = resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`))
	if result["protocolVersion"] != ServerProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", result["protocolVersion"], ServerProtocolVersion)
	}
	// No params at all is still a valid handshake.
	result = resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":3,"method":"initialize"}`))
	if result["protocolVersion"] != ServerProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", result["protocolVersion"], ServerProtocolVersion)
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "zenforge" || info["version"] != "test" {
		t.Fatalf("serverInfo = %v", result["serverInfo"])
	}
	if result["instructions"] != "Be careful." {
		t.Fatalf("instructions = %v", result["instructions"])
	}
	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %v", result["capabilities"])
	}
	tools, ok := capabilities["tools"].(map[string]any)
	if !ok || tools["listChanged"] != false {
		t.Fatalf("tools capability = %v", capabilities["tools"])
	}
}

func TestServerListsToolsWithAnnotations(t *testing.T) {
	server := testServer(t)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 3 {
		t.Fatalf("tools = %v", result["tools"])
	}
	first := tools[0].(map[string]any)
	if first["name"] != "read_file" || first["description"] != "Read a file" {
		t.Fatalf("the first tool is %v", first)
	}
	annotations, ok := first["annotations"].(map[string]any)
	if !ok || annotations["readOnlyHint"] != true {
		t.Fatalf("a read-only tool must say so: %v", first["annotations"])
	}
	if _, ok := first["inputSchema"].(map[string]any); !ok {
		t.Fatalf("the tool has no input schema: %v", first)
	}
	// A tool that was declared without a schema still gets one, because the
	// spec makes inputSchema required.
	second := tools[1].(map[string]any)
	schema, ok := second["inputSchema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("the default schema is %v", second["inputSchema"])
	}
	if _, ok := second["annotations"]; ok {
		t.Fatalf("a tool that is not read-only must not claim it: %v", second)
	}
}

func TestServerCallsTools(t *testing.T) {
	server := testServer(t)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"main.go"}}}`))
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %v", result["content"])
	}
	text := content[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "contents of main.go" {
		t.Fatalf("content[0] = %v", text)
	}
	if isError, ok := result["isError"]; ok && isError != false {
		t.Fatalf("a successful call reported isError: %v", result)
	}
	// A call with no arguments is still a call.
	result = resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"fail"}}`))
	if result["isError"] != true {
		t.Fatalf("a failing tool did not set isError: %v", result)
	}
	content = result["content"].([]any)
	if text := content[0].(map[string]any); text["text"] != "the tool refused" {
		t.Fatalf("the failure message was lost: %v", text)
	}
}

func TestServerReportsProtocolErrorsDistinctly(t *testing.T) {
	server := testServer(t)
	cases := []struct {
		name    string
		message string
		code    float64
	}{
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"tools/nope"}`, codeMethodNotFound},
		{"unknown tool", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"missing"}}`, codeInvalidParams},
		{"missing name", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`, codeInvalidParams},
		{"bad params", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":"not-an-object"}`, codeInvalidParams},
		{"wrong jsonrpc", `{"jsonrpc":"1.0","id":5,"method":"ping"}`, codeInvalidRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			errValue := errorOf(t, call(t, server, testCase.message))
			if errValue["code"] != testCase.code {
				t.Fatalf("code = %v, want %v", errValue["code"], testCase.code)
			}
			if message, _ := errValue["message"].(string); message == "" {
				t.Fatalf("the error has no message: %v", errValue)
			}
		})
	}
	// A malformed line is a parse error with no id to echo.
	errValue := errorOf(t, call(t, server, `{"jsonrpc":"2.0",`))
	if errValue["code"] != float64(codeParseError) {
		t.Fatalf("code = %v, want %v", errValue["code"], codeParseError)
	}
}

func TestServerAnswersNotificationsWithNothing(t *testing.T) {
	server := testServer(t)
	for _, message := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","method":"tools/nope"}`,
	} {
		if response, respond := server.Handle(context.Background(), []byte(message)); respond {
			t.Fatalf("a notification was answered with %s", response)
		}
	}
}

func TestServerEchoesStringIDsAndSurvivesAPanic(t *testing.T) {
	server := testServer(t)
	response, respond := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":"abc","method":"ping"}`))
	if !respond {
		t.Fatal("ping was not answered")
	}
	if !strings.Contains(string(response), `"id":"abc"`) {
		t.Fatalf("the string id was not echoed verbatim: %s", response)
	}
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"panic"}}`))
	if result["isError"] != true {
		t.Fatalf("a panicking tool did not fail the call: %v", result)
	}
	content := result["content"].([]any)
	if text := content[0].(map[string]any); !strings.Contains(fmt.Sprint(text["text"]), "panicked") {
		t.Fatalf("the panic was not reported: %v", text)
	}
}

func TestNewServerRejectsBadConfigurations(t *testing.T) {
	handler := func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
		return CallResult{}, nil
	}
	cases := []struct {
		name   string
		config ServerConfig
	}{
		{"no name", ServerConfig{Version: "1", Tools: []ServerTool{{Name: "a", Handler: handler}}}},
		{"no version", ServerConfig{Name: "n", Tools: []ServerTool{{Name: "a", Handler: handler}}}},
		{"no tools", ServerConfig{Name: "n", Version: "1"}},
		{"unnamed tool", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{{Handler: handler}}}},
		{"handler missing", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{{Name: "a"}}}},
		{"duplicate tool", ServerConfig{Name: "n", Version: "1", Tools: []ServerTool{{Name: "a", Handler: handler}, {Name: "a", Handler: handler}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewServer(testCase.config); err == nil {
				t.Fatal("an invalid configuration was accepted")
			}
		})
	}
}

// TestServerServeRoundTrip drives the server over a real pair of pipes, which
// is the path a stdio peer takes: framing, ordering, and end of stream are
// all exercised rather than bypassed through Handle.
func TestServerServeRoundTrip(t *testing.T) {
	server := testServer(t)
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), serverRead, serverWrite)
	}()
	writer := bufio.NewWriter(clientWrite)
	reader := bufio.NewReader(clientRead)
	messages := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a.go"}}}`,
	}
	for _, message := range messages {
		if _, err := writer.WriteString(message + "\n"); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	// Three requests, three responses: the notification is not answered, and
	// every response carries the id of a request that was sent, exactly once.
	// The order is deliberately not asserted any more: the reader hands each
	// request to its own goroutine so a handler can wait for a server-initiated
	// answer, and two requests that do not wait may now finish in either order.
	seen := map[float64]bool{}
	for index := 0; index < 3; index++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read %d failed: %v", index, err)
		}
		decoded := decodeFrame(t, []byte(line))
		id, ok := decoded["id"].(float64)
		if !ok {
			t.Fatalf("response %d carries no numeric id: %s", index, line)
		}
		if seen[id] {
			t.Fatalf("response %d repeated id %v: %s", index, id, line)
		}
		seen[id] = true
	}
	for _, id := range []float64{1, 2, 3} {
		if !seen[id] {
			t.Fatalf("no response for request %v (got %v)", id, seen)
		}
	}
	// Closing the client end ends the loop cleanly rather than as an error.
	if err := clientWrite.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestServerFromReachesTheServerFromAHandlerContext pins the plumbing a tool
// handler uses to make a server-initiated request. The handler sees the server
// answering it through its own context, and a context that never came from a
// server -- or a nil one -- yields nil rather than a panic, which is what lets
// a caller take a path that needs no server.
func TestServerFromReachesTheServerFromAHandlerContext(t *testing.T) {
	if ServerFrom(context.Background()) != nil {
		t.Fatal("a plain context reported a server")
	}
	if ServerFrom(nil) != nil {
		t.Fatal("a nil context reported a server")
	}
	var seen *Server
	server, err := NewServer(ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []ServerTool{{
			Name: "probe",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				seen = ServerFrom(ctx)
				return CallResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	if _, respond := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe"}}`)); !respond {
		t.Fatal("the probe tool was not answered")
	}
	if seen != server {
		t.Fatalf("the handler saw server %p, want %p", seen, server)
	}
}

// TestReadFrameAcceptsHeaderFraming pins the reader's tolerance: a peer that
// frames with Content-Length is not the spec's stdio form, but refusing it
// would turn a harmless difference into a connection failure.
func TestReadFrameAcceptsHeaderFraming(t *testing.T) {
	var buffer strings.Builder
	if err := writeContentLengthFrame(&buffer, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}); err != nil {
		t.Fatalf("writeContentLengthFrame returned error: %v", err)
	}
	var decoded map[string]any
	if err := readFrame(bufio.NewReader(strings.NewReader(buffer.String())), &decoded); err != nil {
		t.Fatalf("readFrame returned error: %v", err)
	}
	if decoded["method"] != "ping" {
		t.Fatalf("decoded %v", decoded)
	}
	// Blank lines between messages are skipped, not fatal.
	if err := readFrame(bufio.NewReader(strings.NewReader("\n\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"pong\"}\n")), &decoded); err != nil {
		t.Fatalf("readFrame returned error: %v", err)
	}
	if decoded["method"] != "pong" {
		t.Fatalf("decoded %v", decoded)
	}
}

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ServerProtocolVersion is the MCP revision this server speaks. It is offered
// when the client names a version this server does not know, because the
// alternative -- refusing to start because a newer client exists -- makes
// every protocol bump a breaking change for no reason.
const ServerProtocolVersion = "2025-06-18"

// serverProtocolVersions are the revisions this server implements, newest
// first. A client that asks for one of these gets it back verbatim, which is
// what the spec's version negotiation requires.
var serverProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes. They are the ones the MCP schema names, not generic
// HTTP-style numbers: a client decides whether to retry a tool error or fix a
// request based on which one it gets.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// ServerHandler runs one tool call. A returned error is a *tool* failure and
// is reported in the result with isError set, not as a JSON-RPC error: an
// MCP client distinguishes "the tool failed" (retry with different
// arguments, or show the model the message) from "the request was malformed"
// (a bug in the client), and collapsing the two loses that.
type ServerHandler func(ctx context.Context, arguments json.RawMessage) (CallResult, error)

// ServerTool is one tool the server exposes.
type ServerTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// ReadOnly marks the tool with the readOnlyHint annotation, which is how
	// a client decides whether the call needs approval.
	ReadOnly bool
	Handler  ServerHandler
}

// ServerConfig configures a Server.
type ServerConfig struct {
	// Name and Version identify the server in initialize.
	Name    string
	Version string
	// Instructions is an optional hint the client may add to the model's
	// context, mirroring the field's meaning on the client side.
	Instructions string
	Tools        []ServerTool
}

// Server implements the server half of the MCP protocol. It is transport
// agnostic: Serve drives it over any pair of streams, and Handle processes a
// single message for a caller that owns the transport (an in-process bridge,
// or a test).
//
// It handles requests sequentially. MCP over stdio is a single ordered
// stream, and the tools this server is meant to expose (a run, a job) are
// long and stateful, so concurrency here would buy nothing and would make
// interleaved output harder to attribute.
type Server struct {
	name         string
	version      string
	instructions string
	tools        []ServerTool
	byName       map[string]ServerTool

	mu       sync.Mutex
	shutdown bool
}

// NewServer validates the configuration and builds the server. A nil input
// schema is replaced with the schema for an object with no properties, which
// is what a tool that takes no arguments needs and is what the spec expects.
func NewServer(config ServerConfig) (*Server, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		return nil, errors.New("mcp server name is required")
	}
	version := strings.TrimSpace(config.Version)
	if version == "" {
		return nil, errors.New("mcp server version is required")
	}
	if len(config.Tools) == 0 {
		return nil, errors.New("mcp server needs at least one tool")
	}
	server := &Server{
		name:         name,
		version:      version,
		instructions: config.Instructions,
		tools:        make([]ServerTool, 0, len(config.Tools)),
		byName:       make(map[string]ServerTool, len(config.Tools)),
	}
	for _, serverTool := range config.Tools {
		toolName := strings.TrimSpace(serverTool.Name)
		if toolName == "" {
			return nil, errors.New("mcp server tool name is required")
		}
		if _, exists := server.byName[toolName]; exists {
			return nil, fmt.Errorf("mcp server tool %q is declared twice", toolName)
		}
		if serverTool.Handler == nil {
			return nil, fmt.Errorf("mcp server tool %q has no handler", toolName)
		}
		serverTool.Name = toolName
		if serverTool.InputSchema == nil {
			serverTool.InputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		server.tools = append(server.tools, serverTool)
		server.byName[toolName] = serverTool
	}
	return server, nil
}

// Tools returns the declared tools, for a caller that wants to list them
// without going through the protocol.
func (s *Server) Tools() []ServerTool {
	out := make([]ServerTool, len(s.tools))
	copy(out, s.tools)
	return out
}

// Handle processes one raw JSON message and returns the response to write,
// if any. A notification (a request without an id) produces no response: the
// protocol has no way to answer one, and inventing an id-less response would
// desynchronize the peer.
func (s *Server) Handle(ctx context.Context, message []byte) ([]byte, bool) {
	var envelope serverRequest
	if err := decodeMessage(message, &envelope); err != nil {
		return mustMarshal(serverResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()},
		}), true
	}
	if envelope.JSONRPC != "" && envelope.JSONRPC != "2.0" {
		return mustMarshal(serverResponse{
			JSONRPC: "2.0",
			ID:      envelope.ID,
			Error:   &rpcError{Code: codeInvalidRequest, Message: fmt.Sprintf("unsupported jsonrpc version %q", envelope.JSONRPC)},
		}), true
	}
	if envelope.ID == nil {
		// A notification: act on it, answer nothing.
		s.handleNotification(envelope.Method)
		return nil, false
	}
	response := serverResponse{JSONRPC: "2.0", ID: envelope.ID}
	result, rpcErr := s.dispatch(ctx, envelope.Method, envelope.Params)
	if rpcErr != nil {
		response.Error = rpcErr
	} else {
		response.Result = result
	}
	return mustMarshal(response), true
}

// Serve reads messages until the input ends, writing each response. It
// returns nil on a clean end of stream: a client that closes the pipe is done
// with the server, not an error worth reporting.
func (s *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	buffered := bufio.NewReader(reader)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		var raw json.RawMessage
		if err := readFrame(buffered, &raw); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if len(raw) == 0 {
			continue
		}
		response, respond := s.Handle(ctx, raw)
		if !respond {
			continue
		}
		if err := writeFrame(writer, json.RawMessage(response)); err != nil {
			return err
		}
	}
}

func (s *Server) handleNotification(method string) {
	switch method {
	case "notifications/initialized":
		// The client acknowledges the handshake; nothing to do.
	case "notifications/cancelled":
		// Cancellation of a single request: the run tools are driven by the
		// caller's context, so there is nothing to stop here.
	}
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *rpcError) {
	switch method {
	case "initialize":
		return s.initialize(params)
	case "ping":
		return json.RawMessage(`{}`), nil
	case "tools/list":
		return s.listTools()
	case "tools/call":
		return s.callTool(ctx, params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("unknown method %q", method)}
	}
}

func (s *Server) initialize(params json.RawMessage) (json.RawMessage, *rpcError) {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		if err := decodeMessage(params, &request); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	version := ServerProtocolVersion
	for _, supported := range serverProtocolVersions {
		if strings.TrimSpace(request.ProtocolVersion) == supported {
			version = supported
			break
		}
	}
	result := map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			// listChanged is false because the tool set is fixed at startup:
			// claiming it would promise a notification this server never sends.
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{"name": s.name, "version": s.version},
	}
	if s.instructions != "" {
		result["instructions"] = s.instructions
	}
	return mustMarshal(result), nil
}

func (s *Server) listTools() (json.RawMessage, *rpcError) {
	tools := make([]map[string]any, 0, len(s.tools))
	for _, serverTool := range s.tools {
		entry := map[string]any{
			"name":        serverTool.Name,
			"inputSchema": serverTool.InputSchema,
		}
		if serverTool.Description != "" {
			entry["description"] = serverTool.Description
		}
		if serverTool.ReadOnly {
			entry["annotations"] = map[string]any{"readOnlyHint": true}
		}
		tools = append(tools, entry)
	}
	return mustMarshal(map[string]any{"tools": tools}), nil
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (json.RawMessage, *rpcError) {
	var request struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeMessage(params, &request); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a tool name"}
	}
	serverTool, ok := s.byName[name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
	}
	if len(request.Arguments) == 0 {
		request.Arguments = json.RawMessage(`{}`)
	}
	result, err := s.invoke(ctx, serverTool, request.Arguments)
	if err != nil {
		// A tool failure is a result, not a protocol error: the client must
		// be able to tell it apart from a malformed request.
		return mustMarshal(CallResult{
			Content: []Content{{Type: "text", Text: err.Error()}},
			IsError: true,
		}), nil
	}
	return mustMarshal(result), nil
}

// invoke runs a handler, converting a panic into an error. A panicking tool
// must not take the server's whole stream down with it: the peer would see a
// closed pipe instead of a failed call, and the reason would be lost.
func (s *Server) invoke(ctx context.Context, serverTool ServerTool, arguments json.RawMessage) (result CallResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("mcp tool %q panicked: %v", serverTool.Name, recovered)
		}
	}()
	return serverTool.Handler(ctx, arguments)
}

// serverRequest is the wire form of a request. ID is kept raw so a string id
// is echoed back as a string: the spec allows either, and a client that sent a
// string and got a number back would not match the response to its request.
type serverRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type serverResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mustMarshal marshals a value the server built itself. A failure here is a
// programming error in a literal map, so it is surfaced as an internal error
// response rather than dropped.
func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"error":{"code":%d,"message":"failed to encode the response"}}`, codeInternalError))
	}
	return data
}

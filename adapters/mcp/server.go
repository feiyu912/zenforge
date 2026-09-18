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
	// codeResourceNotFound is the spec's resource-not-found code. It is kept
	// apart from codeInvalidParams because "this server has no such resource"
	// is a fact about the server, while invalid params is a fact about the
	// request, and a client acts differently on the two.
	codeResourceNotFound = -32002
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
	// Resources and Prompts are the read-only surfaces, declared the same way
	// tools are. Either may be empty; initialize advertises a capability only
	// when something is behind it, so a client never sees a promise this
	// server cannot keep.
	Resources []ServerResource
	Prompts   []ServerPrompt
	// DynamicLists says the tool, resource and prompt sets may change while
	// this server is serving, so initialize should advertise listChanged:
	// true for each surface. It is an explicit opt-in because the default is
	// a promise the server can keep: a fixed set advertised as listChanged:
	// false tells the client it never has to re-list, and a server that then
	// sent a list-changed notification would be sending one the client was
	// told not to expect. With this set, the server must call the matching
	// Notify method after it changes a list, because a client that was told
	// the list can change may cache it and wait to be told.
	DynamicLists bool
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
	name          string
	version       string
	instructions  string
	tools         []ServerTool
	byName        map[string]ServerTool
	resources     []ServerResource
	resourceByURI map[string]ServerResource
	prompts       []ServerPrompt
	promptByName  map[string]ServerPrompt
	dynamicLists  bool

	// stream is the writer Serve is currently answering on, set for the
	// duration of one Serve call. writeMu serializes frames from every source
	// that may write concurrently -- the serving goroutine and a Notify call
	// made by whatever changed a list -- so two frames can never interleave
	// on the wire. streamMu only guards the pointer, and is never held while
	// a write happens.
	stream   io.Writer
	streamMu sync.Mutex
	writeMu  sync.Mutex

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
		name:          name,
		version:       version,
		instructions:  config.Instructions,
		tools:         make([]ServerTool, 0, len(config.Tools)),
		byName:        make(map[string]ServerTool, len(config.Tools)),
		resources:     make([]ServerResource, 0, len(config.Resources)),
		resourceByURI: make(map[string]ServerResource, len(config.Resources)),
		prompts:       make([]ServerPrompt, 0, len(config.Prompts)),
		promptByName:  make(map[string]ServerPrompt, len(config.Prompts)),
		dynamicLists:  config.DynamicLists,
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
	for _, serverResource := range config.Resources {
		uri := strings.TrimSpace(serverResource.URI)
		if uri == "" {
			return nil, errors.New("mcp server resource uri is required")
		}
		if _, exists := server.resourceByURI[uri]; exists {
			return nil, fmt.Errorf("mcp server resource %q is declared twice", uri)
		}
		if serverResource.Handler == nil {
			return nil, fmt.Errorf("mcp server resource %q has no handler", uri)
		}
		serverResource.URI = uri
		server.resources = append(server.resources, serverResource)
		server.resourceByURI[uri] = serverResource
	}
	for _, serverPrompt := range config.Prompts {
		promptName := strings.TrimSpace(serverPrompt.Name)
		if promptName == "" {
			return nil, errors.New("mcp server prompt name is required")
		}
		if _, exists := server.promptByName[promptName]; exists {
			return nil, fmt.Errorf("mcp server prompt %q is declared twice", promptName)
		}
		if serverPrompt.Handler == nil {
			return nil, fmt.Errorf("mcp server prompt %q has no handler", promptName)
		}
		serverPrompt.Name = promptName
		seen := make(map[string]bool, len(serverPrompt.Arguments))
		for index, argument := range serverPrompt.Arguments {
			argumentName := strings.TrimSpace(argument.Name)
			if argumentName == "" {
				return nil, fmt.Errorf("mcp server prompt %q has an unnamed argument", promptName)
			}
			if seen[argumentName] {
				return nil, fmt.Errorf("mcp server prompt %q declares argument %q twice", promptName, argumentName)
			}
			seen[argumentName] = true
			argument.Name = argumentName
			serverPrompt.Arguments[index] = argument
		}
		server.prompts = append(server.prompts, serverPrompt)
		server.promptByName[promptName] = serverPrompt
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

// Resources returns the declared resources, for a caller that wants them
// without going through the protocol.
func (s *Server) Resources() []ServerResource {
	out := make([]ServerResource, len(s.resources))
	copy(out, s.resources)
	return out
}

// Prompts returns the declared prompts, for a caller that wants them without
// going through the protocol.
func (s *Server) Prompts() []ServerPrompt {
	out := make([]ServerPrompt, len(s.prompts))
	copy(out, s.prompts)
	return out
}

// Handle processes one raw JSON message and returns the response to write,
// if any. A notification (a request without an id) produces no response: the
// protocol has no way to answer one, and inventing an id-less response would
// desynchronize the peer.
//
// A caller that owns the transport can install a sink with
// [WithNotificationSink] to receive the progress notifications a handler emits
// while it runs; those arrive before Handle returns and therefore before the
// response is written.
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
//
// Serve owns the outbound stream for its duration. That is what lets a handler
// answer with progress notifications on the same pipe, and what makes the
// Notify* list-changed methods usable: they write to the stream Serve attached,
// so they fail rather than silently vanish when no client is connected.
func (s *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	s.attach(writer)
	defer s.detach()
	// Progress and list-changed frames share the response stream, so the
	// handlers Serve drives reach it through the same sink. Installing it on
	// the context (rather than on the Server) keeps Handle's in-process
	// callers in control of their own transport.
	ctx = WithNotificationSink(ctx, s.send)
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
		if err := s.send(response); err != nil {
			return err
		}
	}
}

// attach records the writer Serve answers on. It is separate from send so the
// pointer can be read without holding writeMu, which is held across writes.
func (s *Server) attach(writer io.Writer) {
	s.streamMu.Lock()
	s.stream = writer
	s.streamMu.Unlock()
}

func (s *Server) detach() {
	s.streamMu.Lock()
	s.stream = nil
	s.streamMu.Unlock()
}

// send writes exactly one frame to the attached stream. Every outbound frame
// goes through it -- responses, progress, list changes -- because a Notify
// call can arrive from a different goroutine than the one Serve is answering
// on, and writeMu is what keeps two frames from sharing a line.
func (s *Server) send(frame []byte) error {
	s.streamMu.Lock()
	writer := s.stream
	s.streamMu.Unlock()
	if writer == nil {
		return errors.New("mcp server is not serving a stream")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeFrame(writer, json.RawMessage(frame))
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
		return s.callTool(withProgress(ctx, params), params)
	case "resources/list":
		return s.listResources()
	case "resources/read":
		return s.readResource(withProgress(ctx, params), params)
	case "prompts/list":
		return s.listPrompts()
	case "prompts/get":
		return s.getPrompt(withProgress(ctx, params), params)
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
	// A capability is advertised only when something is behind it. Claiming
	// resources or prompts that do not exist would make a conforming client
	// call a method that answers with an empty list at best, and the whole
	// point of the capability block is that the client can trust it.
	// subscribe is false because there is no per-resource subscription, and
	// listChanged mirrors ServerConfig.DynamicLists: false by default,
	// because a fixed set that claimed otherwise would promise notifications
	// this server would never send, and true only when the server was told it
	// may change its lists and has the Notify methods to say so.
	capabilities := map[string]any{
		"tools": map[string]any{"listChanged": s.dynamicLists},
	}
	if len(s.resources) > 0 {
		capabilities["resources"] = map[string]any{"subscribe": false, "listChanged": s.dynamicLists}
	}
	if len(s.prompts) > 0 {
		capabilities["prompts"] = map[string]any{"listChanged": s.dynamicLists}
	}
	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    capabilities,
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
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

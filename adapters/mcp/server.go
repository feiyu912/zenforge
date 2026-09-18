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
// Serve reads frames on one goroutine and answers each request on its own, so
// a handler that calls Request can wait for the client's response without
// stopping the reader. That split is the whole point: a server-initiated
// request is answered on the same single stream it was sent on, and a reader
// that was busy inside a handler could never see the answer it was waiting
// for. Requests that run at the same time are attributed by the id each
// response carries, and writes are serialized, so a frame is still one
// well-formed line.
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
	// that may write concurrently -- the goroutine answering a request, a
	// Notify call made by whatever changed a list, and a Request sent by a
	// handler -- so two frames can never interleave on the wire. writeMu is
	// taken before streamMu, and streamMu only guards the pointer; neither is
	// held by a caller that is not writing.
	stream   io.Writer
	streamMu sync.Mutex
	writeMu  sync.Mutex

	// serving is true while Serve owns an outbound stream. It is guarded by
	// requestMu together with pendingRequests, so a Request either registers
	// while the stream is up or is refused at once: it can never register into
	// a registry that Serve has already stopped draining.
	serving bool
	// nextRequestID numbers this server's own requests. Server ids live in a
	// namespace of their own ("srv-<n>", see serverRequestIDPrefix), so an id
	// this server mints can never be confused with an id a client minted for
	// its own requests: client ids are echoed back raw and are never touched.
	nextRequestID uint64
	// pendingRequests maps a server request id, as raw JSON so it is compared
	// byte for byte with what the client echoes back, to the waiter its Request
	// call is reading.
	pendingRequests map[string]chan pendingResponse
	// requestMu guards serving, nextRequestID and pendingRequests. It is never
	// held while a frame is written.
	requestMu sync.Mutex

	// writeErr records the first transport write that failed, so Serve can
	// still report a broken stream now that responses are written by the
	// goroutine answering a request rather than by Serve itself.
	writeErrMu sync.Mutex
	writeErr   error

	// clientMu guards the capabilities the client advertised in initialize.
	// They are the client's, not this server's: elicitation is checked against
	// them, and nothing here is ever added to the server's own capability
	// block.
	clientMu          sync.Mutex
	clientElicitation bool
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

		pendingRequests: map[string]chan pendingResponse{},
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

// serverContextKey is the context key that carries the server handling a
// request to the handler answering it. It is unexported, so this package is the
// only writer: the reader and the handler always belong to the same server, and
// a caller cannot substitute one server's identity for another's.
type serverContextKey struct{}

// withServer returns a context that carries the server handling the request. A
// nil server leaves ctx unchanged, so the paths that have no server behind them
// -- a handler invoked directly, as a test may do -- behave as if no server
// existed rather than panicking on a nil dereference.
func withServer(ctx context.Context, s *Server) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, serverContextKey{}, s)
}

// ServerFrom returns the server whose handler is running under ctx, or nil
// when ctx did not come from [Server.Handle] or [Server.Serve].
//
// It exists because a handler that wants to make a server-initiated request --
// an elicitation is the one that matters here -- needs the server itself, and
// [ServerHandler] receives only a context. The nil result is a documented
// outcome rather than a failure to paper over: a handler with no server cannot
// ask its client anything, so it has to take the path that does not need one.
func ServerFrom(ctx context.Context) *Server {
	if ctx == nil {
		return nil
	}
	server, _ := ctx.Value(serverContextKey{}).(*Server)
	return server
}

// Handle processes one raw JSON message and returns the response to write,
// if any. A notification (a request without an id) produces no response: the
// protocol has no way to answer one, and inventing an id-less response would
// desynchronize the peer.
//
// A response to a server-initiated request is not a request: it is delivered
// to the Request waiting for that id, and nothing is answered even when nobody
// is waiting. Treating it as a request would answer a late response with a
// method-not-found error and desynchronize a peer that is merely slow.
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
	return s.handleEnvelope(ctx, envelope)
}

// handleEnvelope answers one decoded frame. It is split out of Handle so
// Serve's reader can decode a frame once and then either route a response to
// its waiter or hand the request to its own goroutine, without decoding the
// same bytes twice.
func (s *Server) handleEnvelope(ctx context.Context, envelope serverRequest) ([]byte, bool) {
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
	if isResponseEnvelope(envelope) {
		s.routeResponse(envelope)
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
// The reader never runs a handler itself. It reads a frame, routes a response
// to the Request waiting for its id, and hands every other frame to a
// goroutine of its own. That is what lets a handler call Request and wait: the
// client's answer is read while the handler is still blocked, instead of
// queueing behind it. Responses are written under writeMu, so concurrent
// handlers still put one well-formed line each on the wire.
//
// Serve owns the outbound stream for its duration. That is what lets a handler
// answer with progress notifications on the same pipe, and what makes the
// Notify* list-changed methods usable: they write to the stream Serve attached,
// so they fail rather than silently vanish when no client is connected.
func (s *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	s.attach(writer)
	// Progress and list-changed frames share the response stream, so the
	// handlers Serve drives reach it through the same sink. Installing it on
	// the context (rather than on the Server) keeps Handle's in-process
	// callers in control of their own transport.
	ctx = WithNotificationSink(ctx, s.send)

	var handlers sync.WaitGroup
	serveErr := s.readLoop(ctx, reader, &handlers)
	// The order here is the guarantee: fail the waiters, wait for every
	// handler, then release the stream.
	//
	//   - Failing the pending requests first is what unblocks a handler
	//     blocked in Request, so handlers.Wait cannot wait on an answer the
	//     client will never send. Without it, shutdown would have to lean on
	//     the caller's deadline, and Serve could stay up long after the client
	//     left.
	//   - Waiting for the handlers before detaching is what keeps a response
	//     from being lost. A client may send its last request and close its
	//     input -- a half-close -- while the handler is still running; if the
	//     stream were released the instant the reader saw EOF, that handler
	//     would find no stream and its response would be silently dropped.
	//   - Detaching after the wait is what keeps "when Serve returns, nothing
	//     is writing to the stream" true and leaves no goroutine behind: a
	//     write is only ever attempted while the stream is attached, and every
	//     writer is accounted for by the wait.
	s.failPending(ErrStreamClosed)
	handlers.Wait()
	s.detach()
	if serveErr == nil {
		serveErr = s.takeWriteError()
	}
	return serveErr
}

// readLoop reads frames until the input ends, routing answers to their waiters
// and handing requests to their own goroutines. It is the only reader of the
// transport, which is what makes a blocked handler harmless: the next frame is
// read whoever is waiting for what.
func (s *Server) readLoop(ctx context.Context, reader io.Reader, handlers *sync.WaitGroup) error {
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
		var envelope serverRequest
		if err := decodeMessage(raw, &envelope); err != nil {
			// A malformed line is not a response; it goes to a handler
			// goroutine so its parse-error answer does not stall the reader.
			handlers.Add(1)
			go func(raw json.RawMessage) {
				defer handlers.Done()
				s.respondRaw(ctx, raw)
			}(raw)
			continue
		}
		if isResponseEnvelope(envelope) {
			// A response nobody is waiting for -- an unknown id, or an answer
			// that arrived after its deadline -- is dropped here. Answering it
			// would be a protocol error aimed at a client that did nothing
			// wrong, and dispatching it as a request would answer a response
			// with a method-not-found error.
			s.routeResponse(envelope)
			continue
		}
		handlers.Add(1)
		go func(envelope serverRequest) {
			defer handlers.Done()
			s.respondEnvelope(ctx, envelope)
		}(envelope)
	}
}

// respondEnvelope answers one decoded request on its own goroutine. The
// recover is a backstop for a panic that escaped dispatch's per-surface
// recovery: turning it into an internal-error response keeps the failing call
// failing and the stream alive, which is the guarantee the in-process Handle
// path already had.
func (s *Server) respondEnvelope(ctx context.Context, envelope serverRequest) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.writeResponse(mustMarshal(serverResponse{
				JSONRPC: "2.0",
				ID:      envelope.ID,
				Error:   &rpcError{Code: codeInternalError, Message: fmt.Sprintf("mcp server panicked while answering %q: %v", envelope.Method, recovered)},
			}))
		}
	}()
	response, respond := s.handleEnvelope(ctx, envelope)
	if !respond {
		return
	}
	s.writeResponse(response)
}

// respondRaw answers a frame that did not decode, so the peer gets the parse
// error rather than silence. It mirrors respondEnvelope's recover because a
// malformed frame is exactly the input most likely to trip an encoder.
func (s *Server) respondRaw(ctx context.Context, raw json.RawMessage) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.writeResponse(mustMarshal(serverResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: codeInternalError, Message: fmt.Sprintf("mcp server panicked on a malformed message: %v", recovered)},
			}))
		}
	}()
	response, respond := s.Handle(ctx, raw)
	if !respond {
		return
	}
	s.writeResponse(response)
}

// writeResponse puts one response on the stream. A send failure during
// shutdown is expected -- the stream is gone and so is the client -- and is
// dropped; any other failure is recorded so Serve still reports a broken
// transport.
func (s *Server) writeResponse(frame []byte) {
	if err := s.send(frame); err != nil && !errors.Is(err, ErrNotServing) {
		s.recordWriteError(err)
	}
}

// attach records the writer Serve answers on. The stream pointer is set before
// serving is turned on, so a Request that sees a live server also sees a
// writer; a Request that races attach is either refused or finds the writer
// already there.
func (s *Server) attach(writer io.Writer) {
	s.writeMu.Lock()
	s.streamMu.Lock()
	s.stream = writer
	s.streamMu.Unlock()
	s.writeMu.Unlock()

	s.requestMu.Lock()
	s.serving = true
	if s.pendingRequests == nil {
		s.pendingRequests = map[string]chan pendingResponse{}
	}
	s.requestMu.Unlock()

	s.writeErrMu.Lock()
	s.writeErr = nil
	s.writeErrMu.Unlock()
}

// detach releases the outbound stream. It takes writeMu so it cannot run in
// the middle of a frame: a goroutine that already decided to write either
// finishes first or finds the stream gone, never writes half a line to a
// stream nobody owns.
func (s *Server) detach() {
	s.writeMu.Lock()
	s.streamMu.Lock()
	s.stream = nil
	s.streamMu.Unlock()
	s.writeMu.Unlock()
}

// send writes exactly one frame to the attached stream. Every outbound frame
// goes through it -- responses, progress, list changes, server requests --
// because any of them can arrive from a different goroutine than the others,
// and writeMu is what keeps two frames from sharing a line. writeMu is taken
// before the stream pointer is read, so a detach can never race a write into
// using a stream that was already released.
func (s *Server) send(frame []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.streamMu.Lock()
	writer := s.stream
	s.streamMu.Unlock()
	if writer == nil {
		return ErrNotServing
	}
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
		// The server is attached here, at the one point that knows both the
		// request and the server answering it, so a tool handler can reach
		// its client through [ServerFrom]. It is tools/call only: a
		// server-initiated request belongs to a tool doing work on the
		// client's behalf, and the read-only surfaces have no reason to hold
		// the stream open waiting on a person.
		return s.callTool(withServer(withProgress(ctx, params), s), params)
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
		// Capabilities is the *client's* block. It is read here (and only
		// here) because initialize is where a client states what it can be
		// asked for: elicitation lives in it, and a Request that needs the
		// capability has to know whether the client claimed it.
		Capabilities map[string]any `json:"capabilities"`
	}
	if len(params) > 0 {
		if err := decodeMessage(params, &request); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	// A re-initialize replaces what was remembered, including with absence:
	// the latest handshake is the client's word on what it supports, and a
	// later one that drops a capability must not leave the older claim behind.
	s.clientMu.Lock()
	s.clientElicitation = clientAdvertises(request.Capabilities, "elicitation")
	s.clientMu.Unlock()
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

// serverRequest is the wire form of an inbound frame. ID is kept raw so a
// string id is echoed back as a string: the spec allows either, and a client
// that sent a string and got a number back would not match the response to its
// request. Result and Error are present only on a response to a
// server-initiated request, and routeResponse is the only reader of them.
type serverRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
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

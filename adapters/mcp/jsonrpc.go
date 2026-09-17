package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

var ErrClientClosed = errors.New("mcp jsonrpc client is closed")

// JSONRPCClient speaks JSON-RPC 2.0 over a pair of streams.
//
// One goroutine reads the stream and routes each response to the call that is
// waiting for that id, so a call can be abandoned when its context ends
// without stopping the reader: a stdio stream has no request cancellation, and
// a client that reads inline cannot give up on a server that never answers
// without either leaking the goroutine or closing the whole connection. The
// late response of an abandoned call is dropped by id, and the connection
// stays usable for the next call.
type JSONRPCClient struct {
	reader *bufio.Reader
	writer io.Writer

	// mu protects the bookkeeping below. writeMu serializes frame writes and
	// is never held while mu is taken.
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan inboundResponse
	closed  bool
	readErr error

	writeMu  sync.Mutex
	readDone chan struct{}
}

// inboundResponse is one response delivered to a waiting call.
type inboundResponse struct {
	message inboundMessage
	err     error
}

// inboundMessage is any frame the client reads: a response to one of its
// calls, or a server-initiated notification or request.
type inboundMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ClientInfo      Implementation `json:"clientInfo"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
}

type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func NewJSONRPCClient(r io.Reader, w io.Writer) *JSONRPCClient {
	client := &JSONRPCClient{
		writer:   w,
		pending:  map[int64]chan inboundResponse{},
		readDone: make(chan struct{}),
	}
	if r != nil {
		client.reader = bufio.NewReader(r)
		go client.read()
	} else {
		close(client.readDone)
	}
	return client
}

func (c *JSONRPCClient) Initialize(ctx context.Context, params InitializeParams) error {
	if params.ProtocolVersion == "" {
		params.ProtocolVersion = "2024-11-05"
	}
	if params.ClientInfo.Name == "" {
		params.ClientInfo = Implementation{Name: "zenforge", Version: "0.1.0"}
	}
	var result map[string]any
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

func (c *JSONRPCClient) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	var result struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (c *JSONRPCClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (CallResult, error) {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var args map[string]any
	if err := json.Unmarshal(arguments, &args); err != nil {
		return CallResult{}, fmt.Errorf("parse MCP tool arguments: %w", err)
	}
	var result CallResult
	err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}, &result)
	return result, err
}

func (c *JSONRPCClient) call(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.reader == nil || c.writer == nil {
		return fmt.Errorf("mcp jsonrpc client is not open")
	}

	// Register the waiting channel before the request goes out: a response can
	// arrive on the reader goroutine as soon as the write lands, and a call
	// that registered afterwards would never see it.
	waiter := make(chan inboundResponse, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = waiter
	c.mu.Unlock()

	c.writeMu.Lock()
	err := writeFrame(c.writer, request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		if c.isClosed() {
			return ErrClientClosed
		}
		return err
	}

	select {
	case inbound := <-waiter:
		if inbound.err != nil {
			return inbound.err
		}
		if inbound.message.Error != nil {
			return inbound.message.Error
		}
		if result == nil || len(inbound.message.Result) == 0 {
			return nil
		}
		return json.Unmarshal(inbound.message.Result, result)
	case <-ctx.Done():
		// The caller gave up. The id is dropped so a late response is
		// discarded by the reader instead of stalling the next call.
		c.forget(id)
		return ctx.Err()
	}
}

func (c *JSONRPCClient) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.writer == nil {
		return fmt.Errorf("mcp jsonrpc client is not open")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}
	readErr := c.readErr
	c.mu.Unlock()
	if readErr != nil {
		return readErr
	}
	c.writeMu.Lock()
	err := writeFrame(c.writer, request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	c.writeMu.Unlock()
	if err != nil && c.isClosed() {
		return ErrClientClosed
	}
	return err
}

// read routes every inbound frame until the stream ends. It is the only reader
// of the transport, which is what lets a call be abandoned without leaving a
// goroutine blocked on the pipe.
func (c *JSONRPCClient) read() {
	defer close(c.readDone)
	for {
		var message inboundMessage
		if err := readFrame(c.reader, &message); err != nil {
			c.fail(err)
			return
		}
		c.dispatch(message)
	}
}

func (c *JSONRPCClient) dispatch(message inboundMessage) {
	if message.Method != "" {
		// A server-initiated frame. The client advertises no capabilities
		// (sampling, elicitation, roots), so a conforming server has no
		// reason to send one, and an id must never be mistaken for the
		// response to a call: a server request carrying id 1 while call 1 is
		// in flight would otherwise resolve that call with the wrong frame.
		//
		// It is ignored rather than answered. The reader is the only reader
		// of the stream, so writing a refusal from here would stop it
		// reading, and a peer that is still writing its own request would
		// then deadlock the connection.
		return
	}
	if message.ID == nil {
		return
	}
	c.mu.Lock()
	waiter, ok := c.pending[*message.ID]
	delete(c.pending, *message.ID)
	c.mu.Unlock()
	if !ok {
		// A response to a call that already gave up. Dropping it is the whole
		// point of dispatching by id.
		return
	}
	waiter <- inboundResponse{message: message}
}

// fail ends every waiting call once the stream is gone.
func (c *JSONRPCClient) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.readErr == nil {
		c.readErr = err
	}
	for id, waiter := range c.pending {
		delete(c.pending, id)
		waiter <- inboundResponse{err: c.readErr}
	}
}

func (c *JSONRPCClient) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *JSONRPCClient) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	for id, waiter := range c.pending {
		delete(c.pending, id)
		waiter <- inboundResponse{err: ErrClientClosed}
	}
	c.mu.Unlock()
}

func (c *JSONRPCClient) isClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp jsonrpc error %d: %s", e.Code, e.Message)
}

// writeFrame writes one message the way the MCP stdio transport defines it:
// a single line of JSON terminated by a newline. The message must not contain
// a newline of its own, which json.Marshal guarantees.
//
// Content-Length framing is not what the spec says for stdio, and a peer that
// follows the spec (every real MCP server) reads a line, so writing headers
// would make this client speak a private dialect. readFrame still accepts
// both forms, because a peer that uses headers is not worth failing on.
func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// writeContentLengthFrame writes the header form. It exists for the tests
// that pin the reader's tolerance for a header-framed peer.
func writeContentLengthFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// readFrame reads one message, accepting both the spec's newline-delimited
// form and the Content-Length header form.
//
// A line that starts a JSON object is the message. Anything else is the start
// of a header block, which is read up to its blank line and followed by
// exactly Content-Length bytes. Blank lines between messages are skipped,
// which keeps a log that interleaves stray newlines readable rather than
// fatal.
func readFrame(r *bufio.Reader, value any) error {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			return decodeMessage([]byte(line), value)
		}
		contentLength, err := readHeaders(r, line)
		if err != nil {
			return err
		}
		if contentLength <= 0 {
			return fmt.Errorf("missing MCP content length")
		}
		data := make([]byte, contentLength)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		return decodeMessage(data, value)
	}
}

// readHeaders consumes a Content-Length header block whose first line has
// already been read.
func readHeaders(r *bufio.Reader, first string) (int, error) {
	var contentLength int
	for line := first; ; {
		if line == "" {
			return contentLength, nil
		}
		name, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			return 0, fmt.Errorf("invalid MCP header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(rawValue))
			if err != nil {
				return 0, fmt.Errorf("invalid MCP content length %q: %w", rawValue, err)
			}
			contentLength = n
		}
		next, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(next, "\r\n")
	}
}

// decodeMessage decodes one JSON message, keeping numbers exact so an id or a
// structured payload is not rounded on the way through.
func decodeMessage(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(value)
}

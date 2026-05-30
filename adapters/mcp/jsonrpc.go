package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

type JSONRPCClient struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
	nextID int64
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
	return &JSONRPCClient{
		reader: bufio.NewReader(r),
		writer: w,
	}
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
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	if err := writeFrame(c.writer, request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var response response
		if err := readFrame(c.reader, &response); err != nil {
			return err
		}
		if response.ID == nil || *response.ID != id {
			continue
		}
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
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
	defer c.mu.Unlock()
	return writeFrame(c.writer, request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
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

func writeFrame(w io.Writer, value any) error {
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

func readFrame(r *bufio.Reader, value any) error {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("invalid MCP header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(rawValue))
			if err != nil {
				return fmt.Errorf("invalid MCP content length %q: %w", rawValue, err)
			}
			contentLength = n
		}
	}
	if contentLength <= 0 {
		return fmt.Errorf("missing MCP content length")
	}
	data := make([]byte, contentLength)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(value)
}

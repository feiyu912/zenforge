package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestJSONRPCClientListsAndCallsTools(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	defer clientRead.Close()
	defer serverWrite.Close()

	serverDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverRead)
		for i := 0; i < 2; i++ {
			var req request
			if err := readFrame(reader, &req); err != nil {
				serverDone <- err
				return
			}
			switch req.Method {
			case "tools/list":
				err := writeFrame(serverWrite, response{
					JSONRPC: "2.0",
					ID:      &req.ID,
					Result:  rawJSON(`{"tools":[{"name":"echo","description":"Echo","inputSchema":{"type":"object"}}]}`),
				})
				if err != nil {
					serverDone <- err
					return
				}
			case "tools/call":
				err := writeFrame(serverWrite, response{
					JSONRPC: "2.0",
					ID:      &req.ID,
					Result:  rawJSON(`{"content":[{"type":"text","text":"hello"}]}`),
				})
				if err != nil {
					serverDone <- err
					return
				}
			default:
				serverDone <- nil
				return
			}
		}
		serverDone <- nil
	}()

	client := NewJSONRPCClient(clientRead, clientWrite)
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	result, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool returned error: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

func TestJSONRPCClientReturnsRemoteError(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	defer clientRead.Close()
	defer serverWrite.Close()

	go func() {
		reader := bufio.NewReader(serverRead)
		var req request
		if err := readFrame(reader, &req); err != nil {
			return
		}
		_ = writeFrame(serverWrite, response{
			JSONRPC: "2.0",
			ID:      &req.ID,
			Error:   &rpcError{Code: -32601, Message: "missing"},
		})
	}()

	client := NewJSONRPCClient(clientRead, clientWrite)
	_, err := client.ListTools(context.Background())
	if err == nil || err.Error() != "mcp jsonrpc error -32601: missing" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultInitializeParamsUseReleaseVersion(t *testing.T) {
	params := InitializeParams{}
	if params.ClientInfo.Version != "" {
		t.Fatalf("zero value should not set version")
	}
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	defer clientRead.Close()
	defer serverWrite.Close()

	done := make(chan Implementation, 1)
	go func() {
		reader := bufio.NewReader(serverRead)
		var req request
		if err := readFrame(reader, &req); err != nil {
			done <- Implementation{}
			return
		}
		raw, _ := json.Marshal(req.Params)
		var got InitializeParams
		_ = json.Unmarshal(raw, &got)
		_ = writeFrame(serverWrite, response{
			JSONRPC: "2.0",
			ID:      &req.ID,
			Result:  rawJSON(`{"protocolVersion":"2024-11-05","serverInfo":{"name":"test","version":"1"}}`),
		})
		var notify request
		_ = readFrame(reader, &notify)
		done <- got.ClientInfo
	}()

	client := NewJSONRPCClient(clientRead, clientWrite)
	if err := client.Initialize(context.Background(), InitializeParams{}); err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	info := <-done
	if info.Name != "zenforge" || info.Version != "0.1.0" {
		t.Fatalf("client info = %#v", info)
	}
}

func rawJSON(s string) json.RawMessage {
	return json.RawMessage(s)
}

func TestJSONRPCClientAbandonsACallWithoutLosingTheConnection(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	defer clientRead.Close()
	defer serverWrite.Close()

	answered := make(chan struct{})
	go func() {
		defer close(answered)
		reader := bufio.NewReader(serverRead)
		// The first call is never answered: the client has to give up on its
		// own deadline.
		var ignored request
		if err := readFrame(reader, &ignored); err != nil {
			return
		}
		var second request
		if err := readFrame(reader, &second); err != nil {
			return
		}
		_ = writeFrame(serverWrite, response{
			JSONRPC: "2.0",
			ID:      &second.ID,
			Result:  rawJSON(`{"tools":[{"name":"after","description":"After the timeout."}]}`),
		})
	}()

	client := NewJSONRPCClient(clientRead, clientWrite)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.ListTools(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("abandoned call error = %v, want DeadlineExceeded", err)
	}
	// The connection survives the abandoned call: the late response is
	// dropped by id, and the next request is answered normally.
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("call after an abandoned one returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "after" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	<-answered
}

func TestJSONRPCClientIgnoresServerInitiatedFrames(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	defer clientRead.Close()
	defer serverWrite.Close()

	go func() {
		reader := bufio.NewReader(serverRead)
		var req request
		if err := readFrame(reader, &req); err != nil {
			return
		}
		// A notification (no id), and a server request whose id collides with
		// the id of the call in flight. Both must be ignored: treating the
		// second as a response would resolve the call with the wrong frame.
		_ = writeFrame(serverWrite, map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params":  map[string]any{"progress": 1},
		})
		_ = writeFrame(serverWrite, map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"method":  "sampling/createMessage",
			"params":  map[string]any{},
		})
		_ = writeFrame(serverWrite, response{
			JSONRPC: "2.0",
			ID:      &req.ID,
			Result:  rawJSON(`{"tools":[{"name":"real","description":"The real answer."}]}`),
		})
	}()

	client := NewJSONRPCClient(clientRead, clientWrite)
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "real" {
		t.Fatalf("a server-initiated frame was read as the response: %#v", tools)
	}
}

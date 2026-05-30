package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
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

func rawJSON(s string) json.RawMessage {
	return json.RawMessage(s)
}

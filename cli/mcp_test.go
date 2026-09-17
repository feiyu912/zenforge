package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/adapters/mcp"
)

// TestMCPServerToolsAreReadOnly pins the deliberate shape of the exposed set:
// a tool call arrives from another process with no approval prompt in front of
// it, so every tool served this way must be one that cannot change anything.
func TestMCPServerToolsAreReadOnly(t *testing.T) {
	tools, err := mcpServerTools(context.Background(), "jsonl", t.TempDir())
	if err != nil {
		t.Fatalf("mcpServerTools returned error: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("the server exposes no tools")
	}
	for _, serverTool := range tools {
		if !serverTool.ReadOnly {
			t.Fatalf("tool %q is not read-only", serverTool.Name)
		}
		if serverTool.InputSchema == nil || serverTool.InputSchema["type"] != "object" {
			t.Fatalf("tool %q has schema %v", serverTool.Name, serverTool.InputSchema)
		}
	}
}

func TestMCPVersionToolReportsTheVersion(t *testing.T) {
	tools, err := mcpServerTools(context.Background(), "jsonl", t.TempDir())
	if err != nil {
		t.Fatalf("mcpServerTools returned error: %v", err)
	}
	var version mcp.ServerTool
	for _, serverTool := range tools {
		if serverTool.Name == "zenforge_version" {
			version = serverTool
		}
	}
	result, err := version.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("the handler returned error: %v", err)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "0.") {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.StructuredContent["version"] != strings.TrimSpace(Version) {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
}

// TestMCPServerCommandSpeaksTheProtocol drives the subcommand end to end over
// real pipes, which is the path a peer takes: the CLI dispatch, the tool set,
// and the server's framing all have to agree.
func TestMCPServerCommandSpeaksTheProtocol(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mcpServerCommand(context.Background(), []string{"--checkpoint-dir", t.TempDir()}, IO{
			Stdin:  serverRead,
			Stdout: serverWrite,
			Stderr: io.Discard,
		})
	}()
	writer := bufio.NewWriter(clientWrite)
	reader := bufio.NewReader(clientRead)
	for _, message := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"zenforge_runs","arguments":{"limit":5}}}`,
	} {
		if _, err := writer.WriteString(message + "\n"); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	// initialize: the server identifies itself.
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &initialized); err != nil {
		t.Fatalf("the initialize response is not JSON (%v): %s", err, line)
	}
	if initialized.Result.ServerInfo.Name != "zenforge" || initialized.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize = %#v", initialized.Result)
	}
	// tools/list: both tools are advertised as read-only.
	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var listed struct {
		Result struct {
			Tools []mcp.ToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &listed); err != nil {
		t.Fatalf("the tools/list response is not JSON (%v): %s", err, line)
	}
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "zenforge_runs" {
		t.Fatalf("tools = %#v", listed.Result.Tools)
	}
	if !listed.Result.Tools[0].ReadOnly() {
		t.Fatalf("zenforge_runs is not advertised read-only: %#v", listed.Result.Tools[0])
	}
	// tools/call: an empty store answers with an empty list, not an error.
	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var called struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &called); err != nil {
		t.Fatalf("the tools/call response is not JSON (%v): %s", err, line)
	}
	if called.Result.IsError {
		t.Fatalf("the call failed: %s", line)
	}
	if count, ok := called.Result.StructuredContent["count"].(float64); !ok || count != 0 {
		t.Fatalf("structured content = %#v", called.Result.StructuredContent)
	}
	if err := clientWrite.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("mcpServerCommand returned error: %v", err)
	}
}

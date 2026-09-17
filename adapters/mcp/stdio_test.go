package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const stdioHelperEnv = "ZENFORGE_MCP_STDIO_HELPER"

func TestStdioClientLifecycle(t *testing.T) {
	var stderr bytes.Buffer
	client, err := newTestStdioClient(context.Background(), "serve", &stderr)
	if err != nil {
		t.Fatalf("NewStdioClient returned error: %v", err)
	}

	if err := client.Initialize(context.Background(), InitializeParams{}); err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
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
	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !strings.Contains(stderr.String(), "helper stderr") {
		t.Fatalf("stderr was not captured: %q", stderr.String())
	}

	_, err = client.ListTools(context.Background())
	if !errors.Is(err, ErrClientClosed) {
		t.Fatalf("call after Close error = %v, want ErrClientClosed", err)
	}
}

func TestStdioClientStartFailure(t *testing.T) {
	client, err := NewStdioClient(context.Background(), StdioConfig{
		Command: t.TempDir() + "/missing",
	})
	if err == nil || client != nil {
		t.Fatalf("NewStdioClient = (%v, %v), want startup error", client, err)
	}
}

func TestStdioClientNormalExit(t *testing.T) {
	client, err := newTestStdioClient(context.Background(), "exit", nil)
	if err != nil {
		t.Fatalf("NewStdioClient returned error: %v", err)
	}
	select {
	case <-client.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error for normal exit: %v", err)
	}
}

func TestStdioClientContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, err := newTestStdioClient(ctx, "hang", nil)
	if err != nil {
		t.Fatalf("NewStdioClient returned error: %v", err)
	}
	cancel()
	if err := client.Close(); err != nil {
		t.Fatalf("Close after cancellation returned error: %v", err)
	}
	select {
	case <-client.waitDone:
	default:
		t.Fatal("Close returned before process was reaped")
	}
}

func TestStdioClientConcurrentClose(t *testing.T) {
	client, err := newTestStdioClient(context.Background(), "hang", nil)
	if err != nil {
		t.Fatalf("NewStdioClient returned error: %v", err)
	}

	const closers = 16
	errs := make(chan error, closers)
	var wg sync.WaitGroup
	for range closers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- client.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	}
	select {
	case <-client.waitDone:
	default:
		t.Fatal("Close returned before process was reaped")
	}
}

func TestStdioClientCloseUnblocksRPC(t *testing.T) {
	client, err := newTestStdioClient(context.Background(), "block-rpc", nil)
	if err != nil {
		t.Fatalf("NewStdioClient returned error: %v", err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(context.Background())
		callDone <- err
	}()

	time.Sleep(100 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	select {
	case err := <-callDone:
		if !errors.Is(err, ErrClientClosed) {
			t.Fatalf("blocked call error = %v, want ErrClientClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked RPC was not released by Close")
	}
}

func TestStdioClientDeadlineOnAWedgedServerKeepsTheConnection(t *testing.T) {
	client, err := newTestStdioClient(context.Background(), "ignore-call", nil)
	if err != nil {
		t.Fatalf("NewStdioClient returned error: %v", err)
	}
	defer client.Close()
	if err := client.Initialize(context.Background(), InitializeParams{}); err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := client.CallTool(ctx, "echo", json.RawMessage(`{"text":"hello"}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wedged call error = %v, want DeadlineExceeded", err)
	}
	// The server never answered that call; a later request still gets its own
	// response, because dispatch is by id and the abandoned one is dropped.
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools after a wedged call returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestBuildChildEnvScrubsCredentialNames(t *testing.T) {
	ambient := []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"OPENAI_API_KEY=sk-secret",
		"GITHUB_TOKEN=gh-token",
		"AWS_SECRET_ACCESS_KEY=aws",
		"DB_PASSWORD=hunter2",
		"LANG=en_US.UTF-8",
	}
	scrubbed := BuildChildEnv(ambient, []string{"PATH=/custom", "GITHUB_TOKEN=explicit"})
	values := map[string]string{}
	for _, entry := range scrubbed {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	if values["OPENAI_API_KEY"] != "" || values["AWS_SECRET_ACCESS_KEY"] != "" || values["DB_PASSWORD"] != "" {
		t.Fatalf("credential-shaped names survived the scrub: %#v", values)
	}
	if values["PATH"] != "/custom" {
		t.Fatalf("explicit entry did not override the ambient one: %q", values["PATH"])
	}
	if values["GITHUB_TOKEN"] != "explicit" {
		t.Fatalf("an explicitly configured credential was scrubbed: %q", values["GITHUB_TOKEN"])
	}
	if values["HOME"] != "/home/user" || values["LANG"] != "en_US.UTF-8" {
		t.Fatalf("ordinary variables were dropped: %#v", values)
	}
}

func newTestStdioClient(ctx context.Context, mode string, stderr io.Writer) (*StdioClient, error) {
	return NewStdioClient(ctx, StdioConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPStdioHelperProcess", "--", mode},
		Env:     []string{stdioHelperEnv + "=1"},
		Stderr:  stderr,
	})
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv(stdioHelperEnv) != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "exit":
		os.Exit(0)
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	case "block-rpc":
		var req request
		_ = readFrame(bufio.NewReader(os.Stdin), &req)
		for {
			time.Sleep(time.Hour)
		}
	case "serve":
		runMCPStdioHelper(false)
		os.Exit(0)
	case "ignore-call":
		runMCPStdioHelper(true)
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
}

func runMCPStdioHelper(dropCalls bool) {
	if !dropCalls {
		fmt.Fprintln(os.Stderr, "helper stderr")
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		var req request
		if err := readFrame(reader, &req); err != nil {
			return
		}
		if req.ID == 0 {
			continue
		}
		if dropCalls && req.Method == "tools/call" {
			// A wedged tool: the request is read and never answered, while
			// the server keeps serving everything else.
			continue
		}
		var result json.RawMessage
		switch req.Method {
		case "initialize":
			result = rawJSON(`{"protocolVersion":"2024-11-05","serverInfo":{"name":"helper","version":"1"}}`)
		case "tools/list":
			result = rawJSON(`{"tools":[{"name":"echo","description":"Echo","inputSchema":{"type":"object"}}]}`)
		case "tools/call":
			result = rawJSON(`{"content":[{"type":"text","text":"hello"}]}`)
		default:
			_ = writeFrame(os.Stdout, response{
				JSONRPC: "2.0",
				ID:      &req.ID,
				Error:   &rpcError{Code: -32601, Message: "missing"},
			})
			continue
		}
		if err := writeFrame(os.Stdout, response{JSONRPC: "2.0", ID: &req.ID, Result: result}); err != nil {
			return
		}
	}
}

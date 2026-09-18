package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
)

// exchange sends one request to a served MCP session and decodes the one
// response it produces, so a protocol test reads as a conversation.
func exchange(t *testing.T, writer *bufio.Writer, reader *bufio.Reader, message string) map[string]any {
	t.Helper()
	if _, err := writer.WriteString(message + "\n"); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("the response is not JSON (%v): %s", err, line)
	}
	return decoded
}

// startMCPServerCommand drives the real subcommand over pipes, which is the
// path a peer takes: the CLI dispatch, the resource and prompt wiring, and the
// server's framing all have to agree.
func startMCPServerCommand(t *testing.T, args []string) (*bufio.Writer, *bufio.Reader) {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mcpServerCommand(context.Background(), args, IO{
			Stdin:  serverRead,
			Stdout: serverWrite,
			Stderr: io.Discard,
		})
		// Closing the server's end lets a reader stop instead of blocking if
		// the subcommand returned early.
		_ = serverWrite.Close()
	}()
	t.Cleanup(func() {
		_ = clientWrite.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("the MCP server did not stop")
		}
	})
	return bufio.NewWriter(clientWrite), bufio.NewReader(clientRead)
}

func capabilitiesFrom(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	if errValue, ok := response["error"]; ok {
		t.Fatalf("initialize returned an error: %v", errValue)
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize has no result: %v", response)
	}
	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("initialize has no capabilities: %v", result)
	}
	return capabilities
}

// recordServedRun runs one served run to completion against a stub model and
// returns its id plus the options pointing at the store it was recorded in.
func recordServedRun(t *testing.T) (string, options) {
	t.Helper()
	model := newOpenAISSEStub(t, textChunk("recorded answer"))
	opts := servedRunOptions(t, model.url)
	registry := newServedRunRegistry(context.Background())
	runTool, err := newMCPRunTool(context.Background(), &opts, servedRunStreams(), time.Minute, registry)
	if err != nil {
		t.Fatalf("newMCPRunTool returned error: %v", err)
	}
	t.Cleanup(func() { drainClosers(&opts, servedRunStreams()) })
	finished, err := runTool.Handler(context.Background(), json.RawMessage(`{"prompt":"say hello"}`))
	if err != nil {
		t.Fatalf("the run handler returned error: %v", err)
	}
	runID, _ := finished.StructuredContent["runId"].(string)
	if runID == "" {
		t.Fatalf("the run was not recorded: %#v", finished.StructuredContent)
	}
	return runID, opts
}

func TestMCPServerResourcesListTheRunsIndex(t *testing.T) {
	resources := mcpServerResources("jsonl", t.TempDir())
	byURI := map[string]mcp.ServerResource{}
	for _, resource := range resources {
		byURI[resource.URI] = resource
	}
	index, ok := byURI["zenforge://runs"]
	if !ok {
		t.Fatalf("resources = %#v", resources)
	}
	if index.Name == "" || index.MimeType != "application/json" || index.Handler == nil {
		t.Fatalf("the runs index = %#v", index)
	}
	if _, ok := byURI["zenforge://runs/{runId}"]; !ok {
		t.Fatalf("the run instance resource is missing: %#v", resources)
	}
}

func TestMCPServerResourcesServeRecordedRuns(t *testing.T) {
	runID, opts := recordServedRun(t)
	writer, reader := startMCPServerCommand(t, []string{"--checkpoint-dir", opts.checkpointDir})

	// initialize advertises resources because the run resources exist.
	capabilities := capabilitiesFrom(t, exchange(t, writer, reader, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	resourcesCapability, ok := capabilities["resources"].(map[string]any)
	if !ok || resourcesCapability["listChanged"] != false {
		t.Fatalf("resources capability = %v", capabilities["resources"])
	}

	// resources/list contains the runs index and the instance template.
	listed := exchange(t, writer, reader, `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`)
	result, _ := listed["result"].(map[string]any)
	resources, _ := result["resources"].([]any)
	uris := map[string]bool{}
	for _, entry := range resources {
		uris[entry.(map[string]any)["uri"].(string)] = true
	}
	if !uris["zenforge://runs"] || !uris["zenforge://runs/{runId}"] {
		t.Fatalf("resources = %v", resources)
	}

	// resources/read of the index carries the recorded run.
	read := exchange(t, writer, reader, `{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"zenforge://runs"}}`)
	result, _ = read["result"].(map[string]any)
	contents, _ := result["contents"].([]any)
	indexText, _ := contents[0].(map[string]any)["text"].(string)
	if !strings.Contains(indexText, runID) {
		t.Fatalf("the index does not carry run %s: %s", runID, indexText)
	}

	// resources/read of a concrete run returns exactly that run.
	read = exchange(t, writer, reader, `{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"zenforge://runs/`+runID+`"}}`)
	result, _ = read["result"].(map[string]any)
	contents, _ = result["contents"].([]any)
	runText, _ := contents[0].(map[string]any)["text"].(string)
	var summary runSummary
	if err := json.Unmarshal([]byte(runText), &summary); err != nil || summary.RunID != runID {
		t.Fatalf("run summary = %#v, %v (%s)", summary, err, runText)
	}

	// An id that is not recorded is the spec's resource-not-found error
	// (-32002), not a tool-style result and not invalid params.
	missing := exchange(t, writer, reader, `{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"zenforge://runs/run_does_not_exist"}}`)
	errValue, _ := missing["error"].(map[string]any)
	if errValue == nil || errValue["code"] != float64(-32002) {
		t.Fatalf("unknown run = %v", missing)
	}
}

func TestMCPServerPromptsRoundTripACommand(t *testing.T) {
	dir := t.TempDir()
	writeCommand(t, dir, "review.md", "---\ndescription: Review the working tree\nargument-hint: \"<path>\"\n---\nReview $1 (all: $ARGUMENTS)\n")
	writer, reader := startMCPServerCommand(t, []string{"--checkpoint-dir", t.TempDir(), "--commands", dir})

	// initialize advertises prompts because the catalog has a command.
	capabilities := capabilitiesFrom(t, exchange(t, writer, reader, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	promptsCapability, ok := capabilities["prompts"].(map[string]any)
	if !ok || promptsCapability["listChanged"] != false {
		t.Fatalf("prompts capability = %v", capabilities["prompts"])
	}

	// prompts/list names the command and its argument.
	listed := exchange(t, writer, reader, `{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`)
	result, _ := listed["result"].(map[string]any)
	prompts, _ := result["prompts"].([]any)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v", prompts)
	}
	prompt := prompts[0].(map[string]any)
	if prompt["name"] != "review" {
		t.Fatalf("prompt = %v", prompt)
	}
	if description, _ := prompt["description"].(string); !strings.Contains(description, "Review the working tree") {
		t.Fatalf("the command's description was lost: %v", prompt["description"])
	}
	arguments, _ := prompt["arguments"].([]any)
	if len(arguments) != 1 || arguments[0].(map[string]any)["name"] != "path" || arguments[0].(map[string]any)["required"] != true {
		t.Fatalf("arguments = %v", prompt["arguments"])
	}

	// prompts/get renders the command's task text from its argument.
	got := exchange(t, writer, reader, `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"review","arguments":{"path":"main.go"}}}`)
	result, _ = got["result"].(map[string]any)
	messages, _ := result["messages"].([]any)
	content, _ := messages[0].(map[string]any)["content"].(map[string]any)
	if content["text"] != "Review main.go (all: main.go)" {
		t.Fatalf("content = %v", messages[0])
	}

	// A missing required argument is invalid params (-32602), the same code
	// the protocol layer returns, so a client is told to fix its request.
	missing := exchange(t, writer, reader, `{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"review"}}`)
	errValue, _ := missing["error"].(map[string]any)
	if errValue == nil || errValue["code"] != float64(-32602) {
		t.Fatalf("missing argument = %v", missing)
	}
}

func TestMCPServerPromptsAreAbsentWithoutACommandCatalog(t *testing.T) {
	opts := defaultOptions()
	// A fresh workspace has no .zenforge/commands, so there is nothing to
	// advertise: an empty prompt set must not become a capability.
	opts.workspace = t.TempDir()
	prompts, err := mcpServerPrompts(opts)
	if err != nil {
		t.Fatalf("mcpServerPrompts returned error: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("prompts = %#v", prompts)
	}
}

// TestMCPServerPromptArgumentsComeFromTheCommand pins how a command's
// arguments are declared: an argument-hint's <required>/[optional]
// placeholders become named prompt arguments, and a template that substitutes
// $ARGUMENTS or $1..$9 with no hint becomes one raw argument.
func TestMCPServerPromptArgumentsComeFromTheCommand(t *testing.T) {
	dir := t.TempDir()
	writeCommand(t, dir, "hinted.md", "---\nargument-hint: \"<path> [reason]\"\n---\nReview $1 because $2\n")
	writeCommand(t, dir, "body.md", "Fix $ARGUMENTS now\n")
	writeCommand(t, dir, "plain.md", "Just say hello\n")
	writeCommand(t, dir, "escaped.md", "Print $$ARGUMENTS and $$5 literally\n")
	opts := defaultOptions()
	opts.commandsDir = dir
	prompts, err := mcpServerPrompts(opts)
	if err != nil {
		t.Fatalf("mcpServerPrompts returned error: %v", err)
	}
	byName := map[string]mcp.ServerPrompt{}
	for _, prompt := range prompts {
		byName[prompt.Name] = prompt
	}
	hinted := byName["hinted"].Arguments
	if len(hinted) != 2 || hinted[0].Name != "path" || !hinted[0].Required ||
		hinted[1].Name != "reason" || hinted[1].Required {
		t.Fatalf("hinted arguments = %#v", hinted)
	}
	body := byName["body"].Arguments
	if len(body) != 1 || body[0].Name != "arguments" || body[0].Required {
		t.Fatalf("body arguments = %#v", body)
	}
	// A body that only prints escaped dollars takes no arguments at all.
	if arguments := byName["escaped"].Arguments; len(arguments) != 0 {
		t.Fatalf("escaped arguments = %#v", arguments)
	}
	if arguments := byName["plain"].Arguments; len(arguments) != 0 {
		t.Fatalf("plain arguments = %#v", arguments)
	}
}

// TestMCPServerPromptRenderingNeverRunsInlineShell pins the read-only promise:
// prompts/get arrives from another process with no approval in front of it, so
// a run-bash command must render its expression verbatim rather than execute
// it.
func TestMCPServerPromptRenderingNeverRunsInlineShell(t *testing.T) {
	dir := t.TempDir()
	writeCommand(t, dir, "status.md", "---\nrun-bash: true\n---\nstatus:\n!`printf ran`\n")
	opts := defaultOptions()
	opts.commandsDir = dir
	prompts, err := mcpServerPrompts(opts)
	if err != nil {
		t.Fatalf("mcpServerPrompts returned error: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %#v", prompts)
	}
	result, err := prompts[0].Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("the prompt handler returned error: %v", err)
	}
	text := result.Messages[0].Content.Text
	if !strings.Contains(text, "!`printf ran`") {
		t.Fatalf("inline shell was executed or dropped: %q", text)
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/redact"
	"github.com/feiyu912/zenforge/tool"
)

const (
	cliMCPHelperEnv      = "ZENFORGE_CLI_MCP_HELPER"
	cliMCPHelperInitEnv  = "ZENFORGE_CLI_MCP_INIT_FILE"
	cliMCPHelperExitEnv  = "ZENFORGE_CLI_MCP_EXIT_FILE"
	cliMCPHelperLogEnv   = "ZENFORGE_CLI_MCP_CALL_LOG"
	cliMCPHelperCommand  = "-test.run=TestCLIMCPHelperProcess"
	cliMCPHelperArgument = "serve"
)

func TestMCPServerSpecsValidateTheSection(t *testing.T) {
	cases := []struct {
		name    string
		config  mcpServersConfig
		wantErr string
	}{
		{
			name:   "nothing configured is not an error",
			config: nil,
		},
		{
			name: "a complete entry is accepted",
			config: mcpServersConfig{"helper": {
				Command:  "helper",
				Args:     []string{"--stdio"},
				Env:      map[string]redact.String{"HELPER_TOKEN": redact.New("value")},
				Deferred: true,
			}},
		},
		{
			name:    "a missing command is a configuration error",
			config:  mcpServersConfig{"helper": {}},
			wantErr: "mcpServers.helper.command is required",
		},
		{
			name:    "a blank command is a configuration error",
			config:  mcpServersConfig{"helper": {Command: "   "}},
			wantErr: "mcpServers.helper.command is required",
		},
		{
			name:    "an empty server name is rejected",
			config:  mcpServersConfig{"": {Command: "helper"}},
			wantErr: "empty server name",
		},
		{
			name:    "surrounding whitespace in a server name is rejected",
			config:  mcpServersConfig{" helper": {Command: "helper"}},
			wantErr: "surrounding whitespace",
		},
		{
			name:    "the namespace separator in a server name is rejected",
			config:  mcpServersConfig{"a__b": {Command: "helper"}},
			wantErr: `must not contain "__"`,
		},
		{
			name:    "an unusable environment name is rejected",
			config:  mcpServersConfig{"helper": {Command: "helper", Env: map[string]redact.String{"A=B": redact.New("x")}}},
			wantErr: "invalid variable name",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			specs, err := mcpServerSpecs(testCase.config)
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("mcpServerSpecs returned error: %v", err)
				}
				if testCase.config == nil && specs != nil {
					t.Fatalf("specs = %#v, want nil", specs)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want to contain %q", err, testCase.wantErr)
			}
		})
	}
}

func TestMCPServerSpecsAreOrderedAndKeepSecrets(t *testing.T) {
	config := mcpServersConfig{
		"zeta":  {Command: "z"},
		"alpha": {Command: "a", Env: map[string]redact.String{"B_TOKEN": redact.New("second"), "A_TOKEN": redact.New("first")}},
	}
	specs, err := mcpServerSpecs(config)
	if err != nil {
		t.Fatalf("mcpServerSpecs returned error: %v", err)
	}
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "zeta" {
		t.Fatalf("specs are not sorted by name: %#v", specs)
	}
	if got := strings.Join(specs[0].Env, ","); got != "A_TOKEN=first,B_TOKEN=second" {
		t.Fatalf("env entries = %q", got)
	}
	// The value has to survive to the child process; everything that formats
	// the config must not show it.
	if formatted := fmt.Sprintf("%v", config["alpha"].Env); strings.Contains(formatted, "first") {
		t.Fatalf("formatting leaked the secret: %q", formatted)
	}
}

func TestBuildAgentStartsConfiguredMCPServers(t *testing.T) {
	helper := newCLIMCPHelper(t)
	opts := defaultOptions()
	opts.apiKey = "test"
	opts.checkpointDir = t.TempDir()
	opts.mcpServers = []mcpServerSpec{helper.spec("helper")}

	agent, err := buildAgent(context.Background(), &opts, IO{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("buildAgent returned error: %v", err)
	}
	if agent == nil {
		t.Fatal("buildAgent returned no agent")
	}
	helper.waitForMarker(t, helper.initFile)

	// Draining is the command's deferred cleanup; the server is expected to
	// be gone once it returns.
	drainClosers(&opts, IO{Stderr: io.Discard})
	helper.waitForMarker(t, helper.exitFile)
}

func TestBuildAgentFailsAndReapsStartedServersWhenOneCannotStart(t *testing.T) {
	helper := newCLIMCPHelper(t)
	opts := defaultOptions()
	opts.apiKey = "test"
	opts.checkpointDir = t.TempDir()
	opts.mcpServers = []mcpServerSpec{
		helper.spec("helper"),
		{Name: "broken", Command: filepath.Join(t.TempDir(), "missing-server")},
	}

	if _, err := buildAgent(context.Background(), &opts, IO{Stderr: io.Discard}); err == nil {
		t.Fatal("buildAgent accepted a server that cannot start")
	} else if !strings.Contains(err.Error(), "start mcp server broken") {
		t.Fatalf("error = %v", err)
	}
	// The first server was already running, so it has to be reaped even
	// though the build failed.
	drainClosers(&opts, IO{Stderr: io.Discard})
	helper.waitForMarker(t, helper.exitFile)
}

func TestBuildMCPToolsNamespacesAndMarksDeferredTools(t *testing.T) {
	helper := newCLIMCPHelper(t)
	opts := defaultOptions()
	opts.mcpServers = []mcpServerSpec{helper.spec("helper")}

	adapted, err := buildMCPTools(context.Background(), &opts, IO{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("buildMCPTools returned error: %v", err)
	}
	defer drainClosers(&opts, IO{Stderr: io.Discard})
	names := make([]string, 0, len(adapted))
	for _, adaptedTool := range adapted {
		names = append(names, adaptedTool.Name())
		if tool.IsDeferred(adaptedTool) {
			t.Fatalf("an eager server produced deferred tool %s", adaptedTool.Name())
		}
	}
	if got := strings.Join(names, ","); got != "mcp__helper__echo,mcp__helper__mutate" {
		t.Fatalf("tool names = %q", got)
	}
	if hasDeferredTools(adapted) {
		t.Fatal("an eager server should not need tool_search")
	}

	deferredSpecs := []mcpServerSpec{helper.spec("helper")}
	deferredSpecs[0].Deferred = true
	deferredOpts := defaultOptions()
	deferredOpts.mcpServers = deferredSpecs
	deferred, err := buildMCPTools(context.Background(), &deferredOpts, IO{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("buildMCPTools returned error: %v", err)
	}
	defer drainClosers(&deferredOpts, IO{Stderr: io.Discard})
	if !hasDeferredTools(deferred) {
		t.Fatal("a deferred server should make tool_search necessary")
	}
	for _, adaptedTool := range deferred {
		if !tool.IsDeferred(adaptedTool) {
			t.Fatalf("deferred server left %s eager", adaptedTool.Name())
		}
	}
}

func TestBuildMCPToolsRefusesCollidingToolNamesAcrossServers(t *testing.T) {
	helper := newCLIMCPHelper(t)
	opts := defaultOptions()
	// Two server names that sanitize onto the same namespace prefix: the
	// model-visible names would collide, which would silently hide one tool.
	first := helper.spec("a.b")
	second := helper.spec("a b")
	opts.mcpServers = []mcpServerSpec{first, second}
	_, err := buildMCPTools(context.Background(), &opts, IO{Stderr: io.Discard})
	defer drainClosers(&opts, IO{Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "both expose the tool name") {
		t.Fatalf("error = %v, want a collision error", err)
	}
}

func TestRunCallsAReadOnlyMCPToolWithoutAsking(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	helper := newCLIMCPHelper(t)
	config := writeMCPConfig(t, map[string]any{"helper": helper.serverConfig()})
	secondRequest := newOpenAISSEStub(t,
		toolCallChunk("call_echo", "mcp__helper__echo", `{}`),
		textChunk("the remote tool answered"),
	)

	var stdout, stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{
		"run",
		"--config", config,
		"--ignore-user-config",
		"--base-url", secondRequest.url,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--no-shell",
		"--approve", "never",
		"call the remote tool",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "tool mcp__helper__echo") {
		t.Fatalf("the MCP tool was not called: %q", stdout.String())
	}
	if !strings.Contains(secondRequest.body(), "remote hello") {
		t.Fatalf("the remote result never reached the model: %s", secondRequest.body())
	}
	if calls := helper.calls(t); len(calls) != 1 || calls[0] != "echo" {
		t.Fatalf("remote calls = %v", calls)
	}
	// The run ended, so the command's deferred drain has closed the server.
	helper.waitForMarker(t, helper.exitFile)
}

func TestRunRefusesAnUndeclaredMCPToolWhenApprovalIsDenied(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	helper := newCLIMCPHelper(t)
	config := writeMCPConfig(t, map[string]any{"helper": helper.serverConfig()})
	model := newOpenAISSEStub(t,
		toolCallChunk("call_mutate", "mcp__helper__mutate", `{}`),
		textChunk("the tool was refused"),
	)

	var stdout, stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{
		"run",
		"--config", config,
		"--ignore-user-config",
		"--base-url", model.url,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--no-shell",
		"--approve", "never",
		"mutate through the remote tool",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if exitCode != exitApprovalRejected {
		t.Fatalf("exit code = %d, want %d; stderr=%q stdout=%q", exitCode, exitApprovalRejected, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "approval required") {
		t.Fatalf("no approval request was rendered: %q", stdout.String())
	}
	// Denied means denied: the remote process never saw the call.
	if calls := helper.calls(t); len(calls) != 0 {
		t.Fatalf("a denied tool still reached the server: %v", calls)
	}
}

func TestRunCallsAnUndeclaredMCPToolWhenApprovalIsGranted(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	helper := newCLIMCPHelper(t)
	config := writeMCPConfig(t, map[string]any{"helper": helper.serverConfig()})
	model := newOpenAISSEStub(t,
		toolCallChunk("call_mutate", "mcp__helper__mutate", `{}`),
		textChunk("the tool ran"),
	)

	var stdout, stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{
		"run",
		"--config", config,
		"--ignore-user-config",
		"--base-url", model.url,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--no-shell",
		"--approve", "always",
		"mutate through the remote tool",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if calls := helper.calls(t); len(calls) != 1 || calls[0] != "mutate" {
		t.Fatalf("remote calls = %v", calls)
	}
	if !strings.Contains(model.body(), "mutated") {
		t.Fatalf("the approved result never reached the model: %s", model.body())
	}
}

func TestRunRejectsAnInvalidMCPServerSectionAsUsage(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	config := writeMCPConfig(t, map[string]any{"broken": map[string]any{}})
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{
		"run",
		"--config", config,
		"--ignore-user-config",
		"--checkpoint-dir", t.TempDir(),
		"--no-shell",
		"do something",
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if exitCode != exitInvalidUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%q", exitCode, exitInvalidUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "mcpServers.broken.command is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// cliMCPHelper is a real MCP server process: the test binary re-executes
// itself in helper mode, which is the only way to exercise the stdio client
// against a transport that behaves like a subprocess.
type cliMCPHelper struct {
	initFile string
	exitFile string
	logFile  string
}

func newCLIMCPHelper(t *testing.T) *cliMCPHelper {
	t.Helper()
	dir := t.TempDir()
	return &cliMCPHelper{
		initFile: filepath.Join(dir, "initialized"),
		exitFile: filepath.Join(dir, "exited"),
		logFile:  filepath.Join(dir, "calls.log"),
	}
}

func (h *cliMCPHelper) spec(name string) mcpServerSpec {
	return mcpServerSpec{
		Name:    name,
		Command: os.Args[0],
		Args:    []string{cliMCPHelperCommand, "--", cliMCPHelperArgument},
		Env: []string{
			cliMCPHelperEnv + "=1",
			cliMCPHelperInitEnv + "=" + h.initFile,
			cliMCPHelperExitEnv + "=" + h.exitFile,
			cliMCPHelperLogEnv + "=" + h.logFile,
		},
	}
}

func (h *cliMCPHelper) serverConfig() map[string]any {
	spec := h.spec("helper")
	env := map[string]any{}
	for _, entry := range spec.Env {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	return map[string]any{
		"command": spec.Command,
		"args":    spec.Args,
		"env":     env,
	}
}

func (h *cliMCPHelper) waitForMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the helper never wrote %s", path)
}

func (h *cliMCPHelper) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(h.logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func writeMCPConfig(t *testing.T, servers map[string]any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "zenforge.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return path
}

// openAISSEStub is a one-tool-call-then-answer OpenAI-compatible stream.
type openAISSEStub struct {
	url       string
	requests  int
	lastBody  string
	responses []string
}

func newOpenAISSEStub(t *testing.T, responses ...string) *openAISSEStub {
	t.Helper()
	if len(responses) == 0 {
		t.Fatalf("the stub needs at least one response")
	}
	stub := &openAISSEStub{responses: responses}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll returned error: %v", err)
		}
		stub.lastBody = string(body)
		index := stub.requests - 1
		if index >= len(stub.responses) {
			index = len(stub.responses) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stub.responses[index])
	}))
	t.Cleanup(server.Close)
	stub.url = server.URL
	return stub
}

func (s *openAISSEStub) body() string { return s.lastBody }

func toolCallChunk(id, name, arguments string) string {
	payload := map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    id,
					"type":  "function",
					"function": map[string]any{
						"name":      name,
						"arguments": arguments,
					},
				}},
			},
		}},
	}
	data, _ := json.Marshal(payload)
	return "data: " + string(data) + "\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
}

func textChunk(text string) string {
	payload := map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{"role": "assistant", "content": text}}},
	}
	data, _ := json.Marshal(payload)
	return "data: " + string(data) + "\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
}

// TestCLIMCPHelperProcess is the helper side of the re-exec. It is a no-op in
// the parent test run and an MCP stdio server when the binary is re-executed
// with the marker set.
func TestCLIMCPHelperProcess(t *testing.T) {
	if os.Getenv(cliMCPHelperEnv) != "1" {
		return
	}
	runCLIMCPHelper()
	os.Exit(0)
}

func runCLIMCPHelper() {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			writeHelperMarker(os.Getenv(cliMCPHelperExitEnv))
			return
		}
		if request.ID == nil {
			// initialize also sends notifications/initialized.
			continue
		}
		if request.Method == "tools/call" {
			var params struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(request.Params, &params)
			appendHelperCall(os.Getenv(cliMCPHelperLogEnv), params.Name)
		}
		result, ok := cliMCPHelperResult(request.Method, request.Params)
		if !ok {
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      *request.ID,
				"error":   map[string]any{"code": -32601, "message": "missing"},
			})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}
}

func cliMCPHelperResult(method string, params json.RawMessage) (any, bool) {
	switch method {
	case "initialize":
		writeHelperMarker(os.Getenv(cliMCPHelperInitEnv))
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "cli-helper", "version": "1"},
		}, true
	case "tools/list":
		return map[string]any{"tools": []map[string]any{
			{
				"name":        "echo",
				"description": "Echo a fixed string.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				"annotations": map[string]any{"readOnlyHint": true},
			},
			{
				"name":        "mutate",
				"description": "Pretend to mutate something.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}}, true
	case "tools/call":
		var call struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(params, &call)
		switch call.Name {
		case "echo":
			return map[string]any{"content": []map[string]any{{"type": "text", "text": "remote hello"}}}, true
		case "mutate":
			return map[string]any{"content": []map[string]any{{"type": "text", "text": "mutated"}}}, true
		}
		return nil, false
	default:
		return nil, false
	}
}

func writeHelperMarker(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	_ = os.WriteFile(path, []byte("1"), 0o600)
}

func appendHelperCall(path, name string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(name + "\n")
}

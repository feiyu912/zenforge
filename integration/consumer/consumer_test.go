package consumer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/fake"
	"github.com/feiyu912/zenforge/tools"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
)

type lookupInput struct {
	Key string `json:"key" jsonschema:"required"`
}

type lookupOutput struct {
	Value string `json:"value"`
}

func TestOpenAIEnvProviderRunsTypedAndApprovedSandboxTools(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer consumer-key" {
			t.Errorf("Authorization = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		turn := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			fmt.Fprint(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"lookup-1","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"answer\"}"}},{"index":1,"id":"shell-1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"uname -s\",\"description\":\"verify sandbox OS\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		fmt.Fprint(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"role":"assistant","content":"tools "}}]}`,
			`data: {"choices":[{"delta":{"content":"completed"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	t.Setenv("ZENFORGE_PROVIDER", "openai")
	t.Setenv("ZENFORGE_MODEL", "consumer-model")
	t.Setenv("ZENFORGE_API_KEY", "consumer-key")
	t.Setenv("ZENFORGE_BASE_URL", server.URL+"/v1")
	modelClient, err := provider.FromEnv()
	if err != nil {
		t.Fatalf("provider.FromEnv: %v", err)
	}

	typedCalls := 0
	lookup := tools.Must("lookup", "Look up a local value.", func(ctx context.Context, in lookupInput) (lookupOutput, error) {
		typedCalls++
		return lookupOutput{Value: "42"}, nil
	})
	sandboxBackend := &fake.Sandbox{Result: sandbox.ExecuteResult{
		ExitCode: 0, Stdout: "Linux\n", WorkingDirectory: "/workspace",
	}}
	shell := shelltool.Must(shelltool.Config{
		Backend: shelltool.ShellBackendSandbox,
		Sandbox: sandboxBackend,
		Policy: policy.ShellPolicy{
			WorkingDir: "/workspace", RequireApproval: true, MaxTimeout: time.Second,
		},
	})
	approvalCalls := 0
	broker := approval.BrokerFunc(func(ctx context.Context, req approval.Request) (approval.Decision, error) {
		approvalCalls++
		return approval.Decision{
			RequestID: req.ID, Action: approval.DecisionApprove,
			Scope: approval.ScopeOnce, DecidedAt: time.Now().UTC(),
		}, nil
	})

	result, err := zenforge.New(zenforge.Config{
		Model: modelClient, Tools: []zenforge.Tool{lookup, shell},
		Approval: broker, MaxSteps: 4,
	}).Run(context.Background(), zenforge.Task{RunID: "consumer-run", Input: "run both tools"})
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if result.Output != "tools completed" {
		t.Fatalf("output = %q", result.Output)
	}
	if typedCalls != 1 || approvalCalls != 1 {
		t.Fatalf("typed calls = %d, approval calls = %d", typedCalls, approvalCalls)
	}
	if len(sandboxBackend.ExecuteCalls) != 1 || sandboxBackend.ExecuteCalls[0].Request.Command != "uname -s" {
		t.Fatalf("sandbox calls = %#v", sandboxBackend.ExecuteCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	messages, _ := requests[1]["messages"].([]any)
	encoded, _ := json.Marshal(messages)
	for _, want := range []string{`"tool_call_id":"lookup-1"`, `\"value\":\"42\"`, `"tool_call_id":"shell-1"`, `Linux`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("second request missing %q: %s", want, encoded)
		}
	}
}

func TestAnthropicEnvProviderFactory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "anthropic-key" {
			t.Errorf("X-API-Key = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	t.Setenv("CONSUMER_API_KEY", "anthropic-key")
	t.Setenv("CONSUMER_MODEL", "claude-consumer")
	t.Setenv("CONSUMER_BASE_URL", server.URL)
	client, err := provider.FromEnv(provider.Config{
		Protocol:  provider.Anthropic,
		EnvPrefix: "CONSUMER",
	})
	if err != nil {
		t.Fatalf("provider.FromEnv: %v", err)
	}
	result, err := zenforge.New(zenforge.Config{Model: client}).Run(
		context.Background(), zenforge.Task{Input: "ping"},
	)
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if result.Output != "anthropic ok" {
		t.Fatalf("output = %q", result.Output)
	}
}

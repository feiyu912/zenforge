package zenmind

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/skill"
	"github.com/feiyu912/zenforge/tools"
	"github.com/feiyu912/zenforge/workspace"
)

type fixture[T any] struct {
	Fixture map[string]string `json:"_fixture"`
	Data    T                 `json:"data"`
}

type stubModel struct{ key string }

func (*stubModel) Generate(context.Context, model.Request) (*model.Response, error)  { return nil, nil }
func (*stubModel) Stream(context.Context, model.Request) (<-chan model.Event, error) { return nil, nil }

func runtimeWithModel() Runtime {
	return Runtime{Model: &stubModel{key: "test"}}
}

func readFixture[T any](t *testing.T, name string) fixture[T] {
	t.Helper()
	data, err := os.ReadFile("testdata/platform/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var got fixture[T]
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if got.Fixture["commit"] != "1893edb51b8dc691ae974cea2719a835e0e21de4" || got.Fixture["source"] == "" {
		t.Fatalf("fixture provenance is missing or stale: %#v", got.Fixture)
	}
	return got
}

func TestBuildRunMapsCatalogSessionToConfigAndTask(t *testing.T) {
	agent := readFixture[CatalogAgent](t, "catalog_agent.json").Data
	session := readFixture[Session](t, "query_session.json").Data
	echo := tools.Must("echo", "Echo.", func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})
	hidden := tools.Must("hidden", "Hidden.", func(context.Context, struct{}) (string, error) {
		return "hidden", nil
	})
	wantModel := &stubModel{key: "platform-primary"}
	wantSkills := &skill.Bundle{}
	wantWorkspace := &stubWorkspace{id: "platform"}
	var resolvedKey string

	run, err := BuildRun(context.Background(), agent, session, Runtime{
		Model: &stubModel{key: "legacy-must-not-win"},
		ModelResolver: ModelResolverFunc(func(_ context.Context, key string) (model.Model, error) {
			resolvedKey = key
			if key == wantModel.key {
				return wantModel, nil
			}
			return nil, nil
		}),
		SkillResolver: SkillResolverFunc(func(context.Context, []string) (*skill.Bundle, error) {
			return wantSkills, nil
		}),
		ToolResolver: ToolResolverFunc(func(_ context.Context, request ToolResolution) ([]zenforge.Tool, error) {
			return selectTools([]zenforge.Tool{echo, hidden}, request.Names)
		}),
		WorkspaceResolver: WorkspaceResolverFunc(func(context.Context, WorkspaceResolution) (workspace.Workspace, error) {
			return wantWorkspace, nil
		}),
		Tools: []zenforge.Tool{echo, hidden},
	})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}

	if resolvedKey != "platform-primary" || run.Config.Model != wantModel {
		t.Fatalf("model was not resolved from modelKey: key=%q model=%#v", resolvedKey, run.Config.Model)
	}
	if run.Config.Skills != wantSkills || run.Config.Workspace != wantWorkspace {
		t.Fatalf("skills/workspace were not resolved: %#v", run.Config)
	}
	if run.Config.Mode != zenforge.ModePlanExecute || run.Config.Planning != zenforge.PlanningPlanExecute {
		t.Fatalf("unexpected modes: mode=%s planning=%s", run.Config.Mode, run.Config.Planning)
	}
	if run.Config.MaxSteps != 42 || run.Config.Instructions != "Review carefully." {
		t.Fatalf("unexpected config: %#v", run.Config)
	}
	if len(run.Config.Tools) != 1 || run.Config.Tools[0].Name() != "echo" {
		t.Fatalf("platform tools should override toolNames alias: %#v", run.Config.Tools)
	}
	if run.Task.RunID != "run-platform-1" || run.Task.Input != "Inspect the checkpoint DTO changes." {
		t.Fatalf("platform message/runId should win aliases: %#v", run.Task)
	}
	if !reflect.DeepEqual(run.Task.InitialMessages, []model.Message{{Role: "user", Content: "Check the DTO boundary."}, {Role: "assistant", Content: "I will inspect it."}}) {
		t.Fatalf("history messages were not converted: %#v", run.Task.InitialMessages)
	}
	if run.Task.Meta["session"] != "metadata-value" {
		t.Fatalf("session metadata was not retained: %#v", run.Task.Meta)
	}

	zenmindMeta := run.Task.Meta["zenmind"].(map[string]any)
	agentMeta := zenmindMeta["agent"].(map[string]any)
	sessionMeta := zenmindMeta["session"].(map[string]any)
	if agentMeta["key"] != "checkpoint-reviewer" || agentMeta["mode"] != "PLAN_EXECUTE" {
		t.Fatalf("catalog identity/mode not mapped: %#v", agentMeta)
	}
	if !reflect.DeepEqual(agentMeta["skills"], []string{"go-review", "checkpoint"}) || agentMeta["runtime"].(map[string]any)["environmentId"] != "go-1.26" {
		t.Fatalf("catalog runtime fields not mapped: %#v", agentMeta)
	}
	if agentMeta["budget"].(map[string]any)["maxSteps"] != float64(42) || agentMeta["stageSettings"].(map[string]any)["maxWorkRoundsPerTask"] != float64(4) {
		t.Fatalf("budget/stage settings not mapped: %#v", agentMeta)
	}
	if agentMeta["toolOverrides"].(map[string]any)["echo"] == nil || agentMeta["workspace"].(Workspace).Root != "/workspace/zenforge" {
		t.Fatalf("tool/workspace settings not mapped: %#v", agentMeta)
	}
	if sessionMeta["requestId"] != "req-platform-1" || sessionMeta["chatId"] != "chat-platform-1" || sessionMeta["agentKey"] != "checkpoint-reviewer" {
		t.Fatalf("session identity not mapped: %#v", sessionMeta)
	}
	if sessionMeta["accessLevel"] != "auto_approve" || sessionMeta["workspaceRoot"] != "/workspace/zenforge" || len(sessionMeta["historyMessages"].([]map[string]any)) != 2 {
		t.Fatalf("session execution context not mapped: %#v", sessionMeta)
	}
}

func TestBuildRunPrefersResolvedPromptAndStrictlyConvertsHistory(t *testing.T) {
	run, err := BuildRun(context.Background(), CatalogAgent{Instructions: "catalog fallback"}, Session{
		Message:        "current query",
		ResolvedPrompt: "fully resolved platform prompt",
		HistoryMessages: []map[string]any{
			{"role": "assistant", "content": "calling", "name": "assistant-name", "runId": "ignored", "reasoning_content": "ignored", "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"id":1}`},
			}}},
			{"role": "tool", "content": "result", "name": "lookup", "toolCallId": "call_1", "ts": 123, "_msgId": "ignored"},
			{"role": "tool", "content": "second result", "tool_call_id": "call_2"},
		},
	}, runtimeWithModel())
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if run.Config.Instructions != "fully resolved platform prompt" {
		t.Fatalf("instructions = %q", run.Config.Instructions)
	}
	want := []model.Message{
		{Role: "assistant", Content: "calling", Name: "assistant-name", ToolCalls: []model.ToolCallSpec{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"id":1}`)}}},
		{Role: "tool", Content: "result", Name: "lookup", ToolCallID: "call_1"},
		{Role: "tool", Content: "second result", ToolCallID: "call_2"},
	}
	if !reflect.DeepEqual(run.Task.InitialMessages, want) {
		t.Fatalf("history = %#v, want %#v", run.Task.InitialMessages, want)
	}
	if countTaskInput(run.Task.InitialMessages, run.Task.Input) != 0 {
		t.Fatalf("adapter duplicated current query in history: %#v", run.Task.InitialMessages)
	}
}

func TestBuildRunRejectsMalformedHistoryWithIndex(t *testing.T) {
	tests := []struct {
		name    string
		message map[string]any
		want    string
	}{
		{name: "role", message: map[string]any{"role": "developer", "content": "no"}, want: `history message 1: invalid role "developer"`},
		{name: "content", message: map[string]any{"role": "user", "content": []any{"no"}}, want: "history message 1: content must be a string"},
		{name: "arguments", message: map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call", "function": map[string]any{"name": "lookup", "arguments": "{"}}}}, want: "history message 1: tool_calls[0] function.arguments must be valid JSON"},
		{name: "call identity", message: map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "lookup", "arguments": `{}`}}}}, want: "history message 1: tool_calls[0] requires id and function.name"},
		{name: "tool identity", message: map[string]any{"role": "tool", "content": "result"}, want: "history message 1: tool message requires tool call identity"},
		{name: "user tool calls", message: map[string]any{"role": "user", "content": "bad", "tool_calls": []any{map[string]any{}}}, want: "history message 1: tool_calls is only valid for assistant messages"},
		{name: "tool tool calls", message: map[string]any{"role": "tool", "content": "bad", "tool_call_id": "call", "tool_calls": []any{map[string]any{}}}, want: "history message 1: tool_calls is only valid for assistant messages"},
		{name: "assistant empty tool calls", message: map[string]any{"role": "assistant", "tool_calls": []any{}}, want: "history message 1: tool_calls must contain at least one call"},
		{name: "assistant tool identity", message: map[string]any{"role": "assistant", "content": "bad", "tool_call_id": "call"}, want: "history message 1: tool call identity is only valid for tool messages"},
		{name: "user camel identity", message: map[string]any{"role": "user", "content": "bad", "toolCallId": "call"}, want: "history message 1: tool call identity is only valid for tool messages"},
		{name: "identity type", message: map[string]any{"role": "tool", "content": "bad", "tool_call_id": 1}, want: "history message 1: tool_call_id must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildRun(context.Background(), CatalogAgent{}, Session{Message: "current", HistoryMessages: []map[string]any{{"role": "user", "content": "valid"}, tt.message}}, runtimeWithModel())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildRunPreservesToolCallIdentityWhitespaceAfterValidation(t *testing.T) {
	run, err := BuildRun(context.Background(), CatalogAgent{}, Session{
		Message: "current",
		HistoryMessages: []map[string]any{{
			"role": "tool", "content": "result", "tool_call_id": " call_1 ",
		}},
	}, runtimeWithModel())
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if got := run.Task.InitialMessages[0].ToolCallID; got != " call_1 " {
		t.Fatalf("tool call identity = %q, want original value", got)
	}
}

func countTaskInput(messages []model.Message, input string) int {
	count := 0
	for _, message := range messages {
		if message.Role == "user" && message.Content == input {
			count++
		}
	}
	return count
}

func TestBuildRunRetainsLegacyAliases(t *testing.T) {
	legacyModel := &stubModel{key: "legacy"}
	echo := tools.Must("echo", "Echo.", func(context.Context, struct{}) (string, error) {
		return "echo", nil
	})
	run, err := BuildRun(context.Background(), CatalogAgent{
		Name:      "reviewer",
		Model:     ModelRef{Provider: "openai", Name: "gpt-test"},
		ToolNames: []string{"echo"},
		MaxSteps:  7,
		Planning:  "enabled",
		SubAgents: "enabled",
	}, Session{
		RunID:          "run_1",
		Input:          "What next?",
		ConversationID: "chat_1",
	}, Runtime{Model: legacyModel, Tools: []zenforge.Tool{echo}})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if run.Config.Model != legacyModel || run.Config.MaxSteps != 7 || run.Config.Planning != zenforge.PlanningEnabled || run.Config.SubAgents != zenforge.SubAgentsEnabled {
		t.Fatalf("legacy config aliases regressed: %#v", run.Config)
	}
	meta := run.Task.Meta["zenmind"].(map[string]any)
	if meta["agent"].(map[string]any)["key"] != "reviewer" || meta["session"].(map[string]any)["chatId"] != "chat_1" {
		t.Fatalf("legacy identity aliases regressed: %#v", meta)
	}
}

func TestBuildRunValidatesPlatformIdentityWithoutTighteningLegacyAliases(t *testing.T) {
	tests := []struct {
		name    string
		agent   CatalogAgent
		session Session
		want    string
	}{
		{
			name:    "missing chat",
			agent:   CatalogAgent{Key: "agent-1"},
			session: Session{AgentKey: "agent-1", RunID: "run-1", Message: "hello"},
			want:    "platform identity requires agentKey, chatId, and runId",
		},
		{
			name:    "missing run",
			agent:   CatalogAgent{Key: "agent-1"},
			session: Session{AgentKey: "agent-1", ChatID: "chat-1", Message: "hello"},
			want:    "platform identity requires agentKey, chatId, and runId",
		},
		{
			name:    "request catalog conflict",
			agent:   CatalogAgent{Key: "catalog-agent"},
			session: Session{AgentKey: "request-agent", ChatID: "chat-1", RunID: "run-1", Message: "hello"},
			want:    `session agentKey "request-agent" does not match catalog agent key "catalog-agent"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildRun(context.Background(), tt.agent, tt.session, runtimeWithModel())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}

	_, err := BuildRun(context.Background(), CatalogAgent{Name: "legacy-agent"}, Session{
		ConversationID: "legacy-chat",
		RunID:          "legacy-run",
		Input:          "hello",
	}, runtimeWithModel())
	if err != nil {
		t.Fatalf("legacy alias-only BuildRun returned error: %v", err)
	}
}

func TestBuildRunAcceptsPlatformYAMLIntegerBudget(t *testing.T) {
	run, err := BuildRun(context.Background(), CatalogAgent{
		Budget: map[string]any{"maxSteps": int64(23)},
	}, Session{Message: "hello"}, runtimeWithModel())
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if run.Config.MaxSteps != 23 {
		t.Fatalf("max steps = %d, want 23", run.Config.MaxSteps)
	}
}

func TestBuildRunRejectsUnknownModelAndMode(t *testing.T) {
	t.Run("missing model", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{}, Session{Message: "hello"}, Runtime{})
		if err == nil || !strings.Contains(err.Error(), "zenmind model is required") {
			t.Fatalf("expected required model error, got %v", err)
		}
	})
	t.Run("typed nil model", func(t *testing.T) {
		var missing *stubModel
		_, err := BuildRun(context.Background(), CatalogAgent{}, Session{Message: "hello"}, Runtime{Model: missing})
		if err == nil || !strings.Contains(err.Error(), "zenmind model is required") {
			t.Fatalf("expected required model error, got %v", err)
		}
	})
	t.Run("missing resolver", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{ModelKey: "missing"}, Session{Message: "hello"}, Runtime{})
		if err == nil || !strings.Contains(err.Error(), "requires a ModelResolver") {
			t.Fatalf("expected resolver error, got %v", err)
		}
	})
	t.Run("unknown model", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{ModelKey: "missing"}, Session{Message: "hello"}, Runtime{
			ModelResolver: ModelResolverFunc(func(context.Context, string) (model.Model, error) { return nil, nil }),
		})
		if err == nil || !strings.Contains(err.Error(), `unknown zenmind model "missing"`) {
			t.Fatalf("expected unknown model error, got %v", err)
		}
	})
	t.Run("typed nil resolved model", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{ModelKey: "missing"}, Session{Message: "hello"}, Runtime{
			ModelResolver: ModelResolverFunc(func(context.Context, string) (model.Model, error) {
				var missing *stubModel
				return missing, nil
			}),
		})
		if err == nil || !strings.Contains(err.Error(), `unknown zenmind model "missing"`) {
			t.Fatalf("expected typed nil model error, got %v", err)
		}
	})
	t.Run("resolver error", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{ModelKey: "broken"}, Session{Message: "hello"}, Runtime{
			ModelResolver: ModelResolverFunc(func(context.Context, string) (model.Model, error) { return nil, errors.New("offline") }),
		})
		if err == nil || !strings.Contains(err.Error(), `resolve zenmind model "broken": offline`) {
			t.Fatalf("expected wrapped resolver error, got %v", err)
		}
	})
	t.Run("unknown mode", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{Mode: "PROXY"}, Session{Message: "hello"}, Runtime{})
		if err == nil || !strings.Contains(err.Error(), `unknown zenmind agent mode "PROXY"`) {
			t.Fatalf("expected unknown mode error, got %v", err)
		}
	})
}

func TestBuildRunValidatesCatalogTools(t *testing.T) {
	echo := tools.Must("echo", "Echo.", func(context.Context, struct{}) (string, error) {
		return "echo", nil
	})
	hidden := tools.Must("hidden", "Hidden.", func(context.Context, struct{}) (string, error) {
		return "hidden", nil
	})

	t.Run("missing", func(t *testing.T) {
		_, err := BuildRun(context.Background(), CatalogAgent{Tools: []string{"missing"}}, Session{Message: "hello"}, runtimeWithModel())
		if err == nil || !strings.Contains(err.Error(), `catalog tools unavailable: ["missing"]`) {
			t.Fatalf("expected missing tool diagnostic, got %v", err)
		}
	})
	t.Run("nil registry entry", func(t *testing.T) {
		var unavailable *tools.TypedTool
		runtime := runtimeWithModel()
		runtime.Tools = []zenforge.Tool{unavailable}
		_, err := BuildRun(context.Background(), CatalogAgent{Tools: []string{"echo"}}, Session{Message: "hello"}, runtime)
		if err == nil || !strings.Contains(err.Error(), `catalog tools unavailable: ["echo"]`) {
			t.Fatalf("expected nil tool diagnostic, got %v", err)
		}
	})
	t.Run("duplicate declaration selects once", func(t *testing.T) {
		runtime := runtimeWithModel()
		runtime.Tools = []zenforge.Tool{echo, hidden}
		run, err := BuildRun(context.Background(), CatalogAgent{Tools: []string{"echo", "echo"}}, Session{Message: "hello"}, runtime)
		if err != nil {
			t.Fatalf("BuildRun returned error: %v", err)
		}
		if len(run.Config.Tools) != 1 || run.Config.Tools[0] != echo {
			t.Fatalf("selected tools = %#v, want echo once", run.Config.Tools)
		}
	})
	t.Run("legacy alias", func(t *testing.T) {
		runtime := runtimeWithModel()
		runtime.Tools = []zenforge.Tool{echo, hidden}
		run, err := BuildRun(context.Background(), CatalogAgent{ToolNames: []string{"hidden"}}, Session{Message: "hello"}, runtime)
		if err != nil {
			t.Fatalf("BuildRun returned error: %v", err)
		}
		if len(run.Config.Tools) != 1 || run.Config.Tools[0] != hidden {
			t.Fatalf("selected tools = %#v, want alias-selected hidden", run.Config.Tools)
		}
	})
	t.Run("explicit empty primary overrides alias", func(t *testing.T) {
		runtime := runtimeWithModel()
		runtime.Tools = []zenforge.Tool{echo, hidden}
		run, err := BuildRun(context.Background(), CatalogAgent{Tools: []string{}, ToolNames: []string{"hidden"}}, Session{Message: "hello"}, runtime)
		if err != nil {
			t.Fatalf("BuildRun returned error: %v", err)
		}
		if run.Config.Tools == nil || len(run.Config.Tools) != 0 {
			t.Fatalf("selected tools = %#v, want explicit empty list", run.Config.Tools)
		}
	})
}

func TestBuildRunRequiresInput(t *testing.T) {
	_, err := BuildRun(context.Background(), CatalogAgent{}, Session{}, Runtime{})
	if err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("expected input validation error, got %v", err)
	}
}

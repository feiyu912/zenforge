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
	"github.com/feiyu912/zenforge/tools"
)

type fixture[T any] struct {
	Fixture map[string]string `json:"_fixture"`
	Data    T                 `json:"data"`
}

type stubModel struct{ key string }

func (*stubModel) Generate(context.Context, model.Request) (*model.Response, error)  { return nil, nil }
func (*stubModel) Stream(context.Context, model.Request) (<-chan model.Event, error) { return nil, nil }

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
		Tools: []zenforge.Tool{echo, hidden},
	})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}

	if resolvedKey != "platform-primary" || run.Config.Model != wantModel {
		t.Fatalf("model was not resolved from modelKey: key=%q model=%#v", resolvedKey, run.Config.Model)
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

func TestBuildRunRetainsLegacyAliases(t *testing.T) {
	legacyModel := &stubModel{key: "legacy"}
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
	}, Runtime{Model: legacyModel})
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

func TestBuildRunAcceptsPlatformYAMLIntegerBudget(t *testing.T) {
	run, err := BuildRun(context.Background(), CatalogAgent{
		Budget: map[string]any{"maxSteps": int64(23)},
	}, Session{Message: "hello"}, Runtime{})
	if err != nil {
		t.Fatalf("BuildRun returned error: %v", err)
	}
	if run.Config.MaxSteps != 23 {
		t.Fatalf("max steps = %d, want 23", run.Config.MaxSteps)
	}
}

func TestBuildRunRejectsUnknownModelAndMode(t *testing.T) {
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

func TestBuildRunRequiresInput(t *testing.T) {
	_, err := BuildRun(context.Background(), CatalogAgent{}, Session{}, Runtime{})
	if err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("expected input validation error, got %v", err)
	}
}

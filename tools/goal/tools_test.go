package goaltools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/goals"
	"github.com/feiyu912/zenforge/tool"
)

func newTools(t *testing.T) (map[string]tool.Tool, *goals.MemoryStore) {
	t.Helper()
	store := goals.NewMemoryStore()
	built, err := Tools(Config{Store: store, MaxRounds: 6})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	byName := map[string]tool.Tool{}
	for _, instance := range built {
		byName[instance.Name()] = instance
	}
	return byName, store
}

func call(t *testing.T, instance tool.Tool, raw string) (tool.Result, error) {
	t.Helper()
	return instance.Call(context.Background(), json.RawMessage(raw), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
}

func TestGoalToolsCreateReadAndUpdate(t *testing.T) {
	byName, store := newTools(t)

	// No goal yet: get_goal reports an empty view instead of failing.
	result, err := call(t, byName[GetName], `{}`)
	if err != nil {
		t.Fatalf("get_goal returned error: %v", err)
	}
	emptyGoal := result.Structured["goal"].(map[string]any)
	if emptyGoal["phase"] != "" || emptyGoal["nextAction"] != "create_goal" {
		t.Fatalf("empty goal view = %#v", emptyGoal)
	}

	result, err = call(t, byName[CreateName], `{"objective":"port the remaining capabilities","maxGoalRounds":4}`)
	if err != nil {
		t.Fatalf("create_goal returned error: %v", err)
	}
	goal := result.Structured["goal"].(map[string]any)
	if goal["phase"] != "active" || goal["revision"].(float64) != 1 || goal["armed"] != true {
		t.Fatalf("created goal = %#v", goal)
	}
	if goal["objective"] != "port the remaining capabilities" || goal["maxGoalRounds"].(float64) != 4 {
		t.Fatalf("created goal = %#v", goal)
	}
	goalID, _ := goal["id"].(string)

	// A second goal is refused while the first is active.
	if _, err := call(t, byName[CreateName], `{"objective":"another"}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("second create error = %v", err)
	}

	// update_goal checks the id and revision the model read.
	if _, err := call(t, byName[UpdateName], `{"action":"pause","goalId":"other"}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("wrong id error = %v", err)
	}
	if _, err := call(t, byName[UpdateName], `{"action":"pause","goalId":"`+goalID+`","revision":9}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("wrong revision error = %v", err)
	}
	if _, err := call(t, byName[UpdateName], `{"action":"wat","goalId":"`+goalID+`"}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("unknown action error = %v", err)
	}

	result, err = call(t, byName[UpdateName], `{"action":"edit","objective":"port everything","maxGoalRounds":5}`)
	if err != nil {
		t.Fatalf("edit returned error: %v", err)
	}
	goal = result.Structured["goal"].(map[string]any)
	if goal["objective"] != "port everything" || goal["revision"].(float64) != 2 {
		t.Fatalf("edited goal = %#v", goal)
	}

	result, err = call(t, byName[UpdateName], `{"action":"pause"}`)
	if err != nil || result.Structured["goal"].(map[string]any)["phase"] != "paused" {
		t.Fatalf("pause = %#v err=%v", result.Structured, err)
	}
	result, err = call(t, byName[UpdateName], `{"action":"resume"}`)
	if err != nil || result.Structured["goal"].(map[string]any)["phase"] != "active" {
		t.Fatalf("resume = %#v err=%v", result.Structured, err)
	}

	// blocked requires a reason and derives a kebab-case code.
	if _, err := call(t, byName[UpdateName], `{"action":"blocked"}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("missing reason error = %v", err)
	}
	// The same condition must survive the minimum number of rounds, which
	// the host admits; each rejected attempt is still recorded.
	blockReason := `{"action":"blocked","reason":"The upstream API key is missing"}`
	for round := 1; round <= goals.MinBlockedRounds; round++ {
		state, err := store.Load(context.Background(), "run_1")
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		state, err = goals.AdmitRound(state, round, time.Now())
		if err != nil {
			t.Fatalf("AdmitRound returned error: %v", err)
		}
		if err := store.Save(context.Background(), "run_1", state); err != nil {
			t.Fatalf("Save returned error: %v", err)
		}
		result, err = call(t, byName[UpdateName], blockReason)
		if round < goals.MinBlockedRounds {
			if !errors.Is(err, tool.ErrInvalidArguments) {
				t.Fatalf("round %d block error = %v", round, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("blocked returned error: %v", err)
		}
	}
	goal = result.Structured["goal"].(map[string]any)
	if goal["phase"] != "blocked" || !strings.Contains(goal["blockedReason"].(string), "the-upstream-api-key-is-missing") {
		t.Fatalf("blocked goal = %#v", goal)
	}

	result, err = call(t, byName[UpdateName], `{"action":"complete"}`)
	if err != nil || result.Structured["goal"].(map[string]any)["phase"] != "complete" {
		t.Fatalf("complete = %#v err=%v", result.Structured, err)
	}

	// The persisted state matches what the tools reported.
	stored, err := store.Load(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if stored.Goal.Phase != goals.PhaseComplete || len(stored.SeenGoalIDs) != 1 {
		t.Fatalf("stored state = %#v", stored)
	}
}

func TestGoalToolsShareOneGoalPerSession(t *testing.T) {
	byName, store := newTools(t)
	if _, err := call(t, byName[CreateName], `{"objective":"session one"}`); err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	// A different run is a different session, so it gets its own goal.
	other := tool.Context{RunID: "run_2", ToolCallID: "call_2"}
	result, err := byName[CreateName].Call(context.Background(), json.RawMessage(`{"objective":"session two"}`), other)
	if err != nil {
		t.Fatalf("second session create returned error: %v", err)
	}
	if _, ok := result.Structured["goal"].(map[string]any); !ok {
		t.Fatalf("second session result = %#v", result.Structured)
	}
	first, err := store.Load(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	second, err := store.Load(context.Background(), "run_2")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if first.Goal.Objective != "session one" || second.Goal.Objective != "session two" {
		t.Fatalf("sessions shared a goal: %#v %#v", first.Goal, second.Goal)
	}
}

func TestGoalToolsRequireAStore(t *testing.T) {
	if _, err := Tools(Config{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("missing store error = %v", err)
	}
}

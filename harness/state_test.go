package harness

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRunStateJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	state := RunState{
		Version:   RunStateVersion,
		RunID:     "run_1",
		Input:     "hello",
		Phase:     RunPhaseTool,
		Step:      2,
		CreatedAt: now,
		UpdatedAt: now,
		Messages: []MessageState{
			{Role: "user", Content: "hello"},
			{Role: "assistant", ToolCalls: []ToolCallSpec{{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hello"}`)}}},
		},
		Todos: []TodoState{{ID: "todo_1", Content: "test", Status: TodoInProgress}},
		Tool: ToolState{
			Pending: []ToolCallState{{ID: "call_2", Name: "next", Status: ToolCallPending}},
			Last:    &ToolResultState{ToolCallID: "call_1", Output: "ok"},
		},
		Control:   RunControlState{Status: RunStatusToolExecuting},
		Usage:     UsageState{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
		Workspace: WorkspaceState{Root: "/tmp/work"},
		Model:     ModelState{Provider: "test", Name: "model"},
		Meta:      map[string]any{"k": "v"},
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var got RunState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if got.Version != RunStateVersion || got.RunID != "run_1" || got.Phase != RunPhaseTool {
		t.Fatalf("unexpected state after round trip: %#v", got)
	}
	if got.Messages[1].ToolCalls[0].Name != "echo" {
		t.Fatalf("unexpected tool call after round trip: %#v", got.Messages[1].ToolCalls[0])
	}
}

func TestRunPhaseConstants(t *testing.T) {
	phases := []RunPhase{
		RunPhaseCreated,
		RunPhaseModel,
		RunPhaseTool,
		RunPhaseApproval,
		RunPhaseSubtask,
		RunPhaseFinalizing,
		RunPhaseCompleted,
		RunPhaseFailed,
		RunPhaseCancelled,
	}
	for _, phase := range phases {
		if phase == "" {
			t.Fatalf("phase constant must not be empty")
		}
	}
}

func TestRunStateZeroValueIsJSONSafe(t *testing.T) {
	var state RunState
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var got RunState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
}

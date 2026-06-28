package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRunStateJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	state := RunState{
		Version:   RunStateVersion,
		RunID:     "run_1",
		Input:     "hello",
		Mode:      "oneshot",
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
		Sandbox:   SandboxState{SessionID: "session_1", EnvironmentID: "go", WorkingDir: "/workspace"},
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
	if got.Version != RunStateVersion || got.RunID != "run_1" || got.Mode != "oneshot" || got.Phase != RunPhaseTool {
		t.Fatalf("unexpected state after round trip: %#v", got)
	}
	if got.Messages[1].ToolCalls[0].Name != "echo" {
		t.Fatalf("unexpected tool call after round trip: %#v", got.Messages[1].ToolCalls[0])
	}
	if got.Sandbox.SessionID != "session_1" {
		t.Fatalf("unexpected sandbox state after round trip: %#v", got.Sandbox)
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

func TestValidateRunStateVersionPhaseAndMode(t *testing.T) {
	valid := RunState{Version: RunStateVersion, Phase: RunPhaseModel, Mode: "react"}
	if err := ValidateRunState(valid); err != nil {
		t.Fatalf("ValidateRunState(valid) returned error: %v", err)
	}

	legacy := RunState{Phase: RunPhaseCreated}
	if err := ValidateRunState(legacy); err != nil {
		t.Fatalf("ValidateRunState(legacy) returned error: %v", err)
	}

	tests := []struct {
		name  string
		state RunState
		want  string
	}{
		{name: "version", state: RunState{Version: "zenforge.run_state.v2", Phase: RunPhaseModel}, want: "unsupported run state version"},
		{name: "phase", state: RunState{Version: RunStateVersion, Phase: "future"}, want: "unsupported run phase"},
		{name: "empty phase", state: RunState{Version: RunStateVersion}, want: "unsupported run phase"},
		{name: "mode", state: RunState{Version: RunStateVersion, Phase: RunPhaseModel, Mode: "future"}, want: "unsupported run mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRunState(test.state)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateRunState() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

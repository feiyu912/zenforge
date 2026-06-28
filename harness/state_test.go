package harness

import (
	"encoding/json"
	"fmt"
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

func TestValidateRunStateModelAttempts(t *testing.T) {
	now := time.Now().UTC()
	done := now.Add(time.Second)
	valid := RunState{
		Version: RunStateVersion,
		Phase:   RunPhaseModel,
		Mode:    "react",
		Step:    1,
		Model: ModelState{
			Attempts: []ModelAttempt{{
				ID: "attempt_1", LogicalStep: 1, Status: ModelAttemptSuperseded,
				StartedAt: now, CompletedAt: &done, ReplacementID: "attempt_2",
			}},
			Active: &ModelAttempt{
				ID: "attempt_2", LogicalStep: 1, Status: ModelAttemptStreaming,
				StartedAt: now, ReplacesID: "attempt_1",
			},
		},
	}
	if err := ValidateRunState(valid); err != nil {
		t.Fatalf("ValidateRunState(valid) returned error: %v", err)
	}

	tests := []struct {
		name string
		edit func(*RunState)
		want string
	}{
		{name: "empty id", edit: func(state *RunState) { state.Model.Active.ID = "" }, want: "id is required"},
		{name: "unknown status", edit: func(state *RunState) { state.Model.Active.Status = "future" }, want: "unsupported status"},
		{name: "active committed", edit: func(state *RunState) {
			state.Model.Active.Status = ModelAttemptCommitted
			state.Model.Active.CompletedAt = &done
		}, want: "active attempt cannot have status"},
		{name: "duplicate id", edit: func(state *RunState) { state.Model.Active.ID = "attempt_1" }, want: "duplicate id"},
		{name: "wrong step", edit: func(state *RunState) { state.Model.Active.LogicalStep = 2 }, want: "does not match run step"},
		{name: "missing replaces target", edit: func(state *RunState) {
			state.Model.Active.ReplacesID = "missing"
			state.Model.Attempts[0].Status = ModelAttemptCommitted
			state.Model.Attempts[0].ReplacementID = ""
		}, want: "replaces missing attempt"},
		{name: "historical streaming", edit: func(state *RunState) {
			state.Model.Attempts[0].Status = ModelAttemptStreaming
			state.Model.Attempts[0].CompletedAt = nil
		}, want: "history contains nonterminal status"},
		{name: "terminal without completion", edit: func(state *RunState) {
			state.Model.Attempts[0].CompletedAt = nil
		}, want: "requires completedAt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := valid
			state.Model.Attempts = append([]ModelAttempt(nil), valid.Model.Attempts...)
			active := *valid.Model.Active
			state.Model.Active = &active
			tt.edit(&state)
			err := ValidateRunState(state)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateRunState() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestModelStateAppendAttemptBoundsHistory(t *testing.T) {
	var state ModelState
	now := time.Now().UTC()
	for i := 0; i < ModelAttemptHistoryLimit+5; i++ {
		attempt := ModelAttempt{
			ID: fmt.Sprintf("attempt_%d", i), LogicalStep: i,
			Status: ModelAttemptCommitted, StartedAt: now, CompletedAt: &now,
		}
		if len(state.Attempts) > 0 {
			previous := &state.Attempts[len(state.Attempts)-1]
			previous.ReplacementID = attempt.ID
			attempt.ReplacesID = previous.ID
		}
		state.AppendAttempt(attempt)
	}
	if len(state.Attempts) != ModelAttemptHistoryLimit {
		t.Fatalf("attempt history length = %d, want %d", len(state.Attempts), ModelAttemptHistoryLimit)
	}
	if state.Attempts[0].ReplacesID != "" {
		t.Fatalf("retained history begins with dangling predecessor: %#v", state.Attempts[0])
	}
}

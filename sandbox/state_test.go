package sandbox

import "testing"

func TestStateFromSessionCopiesSandboxMetadata(t *testing.T) {
	session := &Session{
		ID:            "session_1",
		RunID:         "run_1",
		SubtaskID:     "task_1",
		EnvironmentID: "go",
		WorkingDir:    "/workspace",
		Metadata:      map[string]any{"tenantId": "tenant_1"},
	}

	state := StateFromSession(session)
	session.Metadata["tenantId"] = "mutated"

	if state.SessionID != "session_1" || state.RunID != "run_1" || state.SubtaskID != "task_1" || state.EnvironmentID != "go" || state.WorkingDir != "/workspace" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.Metadata["tenantId"] != "tenant_1" {
		t.Fatalf("state metadata was not copied: %#v", state.Metadata)
	}
}

func TestSessionFromStateRestoresRunScopedSession(t *testing.T) {
	state := State{
		SessionID:     "session_1",
		RunID:         "run_1",
		SubtaskID:     "task_1",
		EnvironmentID: "go",
		WorkingDir:    "/workspace",
		Metadata:      map[string]any{"resourceTicket": "ticket_1"},
	}

	session := SessionFromState(state, "run_1", "task_1")
	state.Metadata["resourceTicket"] = "mutated"

	if session == nil {
		t.Fatalf("session was nil")
	}
	if session.ID != "session_1" || session.RunID != "run_1" || session.SubtaskID != "task_1" {
		t.Fatalf("unexpected session identity: %#v", session)
	}
	if session.EnvironmentID != "go" || session.WorkingDir != "/workspace" {
		t.Fatalf("unexpected session runtime fields: %#v", session)
	}
	if session.Metadata["resourceTicket"] != "ticket_1" {
		t.Fatalf("session metadata was not copied: %#v", session.Metadata)
	}
}

func TestSessionFromStateRejectsDifferentRunOrSubtask(t *testing.T) {
	state := State{
		SessionID: "session_1",
		RunID:     "run_1",
		SubtaskID: "task_1",
	}
	if session := SessionFromState(state, "run_2", "task_1"); session != nil {
		t.Fatalf("restored session across runs: %#v", session)
	}
	if session := SessionFromState(state, "run_1", "task_2"); session != nil {
		t.Fatalf("restored session across subtasks: %#v", session)
	}
	if session := SessionFromState(State{SessionID: "legacy"}, "run_1", ""); session != nil {
		t.Fatalf("restored unscoped legacy session: %#v", session)
	}
}

func TestSessionFromEmptyStateReturnsNil(t *testing.T) {
	if session := SessionFromState(State{}, "run_1", ""); session != nil {
		t.Fatalf("session = %#v, want nil", session)
	}
}

func TestStateFromMetadataAcceptsJSONMap(t *testing.T) {
	state, ok := StateFromMetadata(map[string]any{
		MetadataStateKey: map[string]any{
			"sessionId":     "session_1",
			"runId":         "run_1",
			"subtaskId":     "task_1",
			"environmentId": "go",
			"workingDir":    "/workspace",
			"metadata":      map[string]any{"lease": "lease_1"},
		},
	})
	if !ok {
		t.Fatalf("StateFromMetadata did not find state")
	}
	if state.SessionID != "session_1" || state.RunID != "run_1" || state.SubtaskID != "task_1" || state.EnvironmentID != "go" || state.WorkingDir != "/workspace" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.Metadata["lease"] != "lease_1" {
		t.Fatalf("unexpected state metadata: %#v", state.Metadata)
	}
}

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

	if state.SessionID != "session_1" || state.EnvironmentID != "go" || state.WorkingDir != "/workspace" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.Metadata["tenantId"] != "tenant_1" {
		t.Fatalf("state metadata was not copied: %#v", state.Metadata)
	}
}

func TestSessionFromStateRestoresRunScopedSession(t *testing.T) {
	state := State{
		SessionID:     "session_1",
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

func TestSessionFromEmptyStateReturnsNil(t *testing.T) {
	if session := SessionFromState(State{}, "run_1", ""); session != nil {
		t.Fatalf("session = %#v, want nil", session)
	}
}

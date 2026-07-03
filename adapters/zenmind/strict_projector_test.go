package zenmind

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestProjectStrictEnforcesRunBinding(t *testing.T) {
	tests := []struct {
		name     string
		identity ProjectorIdentity
		runID    string
	}{
		{name: "unbound-projector", identity: ProjectorIdentity{}, runID: "run-1"},
		{name: "empty-event-run", identity: ProjectorIdentity{RunID: "run-1"}},
		{name: "different-run", identity: ProjectorIdentity{RunID: "run-1"}, runID: "run-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := NewProjectorWithIdentity(tt.identity)
			if got, err := projector.ProjectStrict(testEvent(zenforge.EventRunStarted, 1, tt.runID, nil)); err == nil || got != nil {
				t.Fatalf("ProjectStrict = %#v, %v; want validation error", got, err)
			}
			if projector.Snapshot().NextSeq != 0 {
				t.Fatal("failed strict projection mutated projector")
			}
		})
	}
}

func TestProjectStrictRejectsInvalidToolLifecycleWithoutMutation(t *testing.T) {
	projector := NewProjectorWithIdentity(ProjectorIdentity{RunID: "run-tools"})
	if _, err := projector.ProjectStrict(testEvent(zenforge.EventRunStarted, 1, "run-tools", nil)); err != nil {
		t.Fatal(err)
	}
	for _, event := range []zenforge.Event{
		testEvent(zenforge.EventToolCall, 2, "run-tools", nil),
		testEvent(zenforge.EventToolResult, 3, "run-tools", map[string]any{"toolCallId": ""}),
		testEvent(zenforge.EventToolResult, 4, "run-tools", map[string]any{"toolCallId": "missing", "toolName": "shell"}),
	} {
		if got, err := projector.ProjectStrict(event); err == nil || got != nil {
			t.Fatalf("ProjectStrict(%s) = %#v, %v; want validation error", event.Type, got, err)
		}
	}
	if projector.Snapshot().NextSeq != 1 || len(projector.Snapshot().OpenTools) != 0 {
		t.Fatalf("failed tool events mutated state: %#v", projector.Snapshot())
	}

	if _, err := projector.ProjectStrict(testEvent(zenforge.EventToolCall, 5, "run-tools", map[string]any{
		"toolCallId": "call-1", "toolName": "shell",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.ProjectStrict(testEvent(zenforge.EventToolResult, 6, "run-tools", map[string]any{
		"toolCallId": "call-1", "toolName": "shell",
	})); err != nil {
		t.Fatal(err)
	}
}

func TestProjectStrictRejectsDuplicateTerminalAndEventsAfterTerminal(t *testing.T) {
	terminalTypes := []zenforge.EventType{
		zenforge.EventRunDone,
		zenforge.EventRunError,
		zenforge.EventRunCancelled,
	}
	for _, terminalType := range terminalTypes {
		t.Run(string(terminalType), func(t *testing.T) {
			projector := NewProjectorWithIdentity(ProjectorIdentity{RunID: "run-terminal"})
			if _, err := projector.ProjectStrict(testEvent(zenforge.EventRunStarted, 1, "run-terminal", nil)); err != nil {
				t.Fatal(err)
			}
			if _, err := projector.ProjectStrict(testEvent(terminalType, 2, "run-terminal", nil)); err != nil {
				t.Fatal(err)
			}
			for _, afterType := range []zenforge.EventType{terminalType, zenforge.EventModelDelta} {
				before := projector.Snapshot()
				got, err := projector.ProjectStrict(testEvent(afterType, 2, "run-terminal", map[string]any{"textDelta": "late"}))
				if err == nil || got != nil || projector.Snapshot().NextSeq != before.NextSeq {
					t.Fatalf("event after terminal = %#v, %v, state=%#v", got, err, projector.Snapshot())
				}
			}
		})
	}
}

func TestProjectStrictEnforcesRunLifecycleAndEventValidity(t *testing.T) {
	projector := NewProjectorWithIdentity(ProjectorIdentity{RunID: "run-life"})
	if _, err := projector.ProjectStrict(testEvent(zenforge.EventModelDelta, 1, "run-life", map[string]any{"textDelta": "early"})); err == nil {
		t.Fatal("event before run.started accepted")
	}
	start := testEvent(zenforge.EventRunStarted, 2, "run-life", nil)
	if _, err := projector.ProjectStrict(start); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.ProjectStrict(start); err == nil {
		t.Fatal("duplicate run.started accepted")
	}
	invalid := testEvent(zenforge.EventModelDelta, 0, "run-life", map[string]any{"textDelta": "bad"})
	if _, err := projector.ProjectStrict(invalid); err == nil {
		t.Fatal("event without timestamp accepted")
	}
}

func TestProjectorStateVersionCompatibilityAndRunIdentity(t *testing.T) {
	legacy := ProjectorState{
		Version:    projectorStateVersionLegacy,
		Identity:   ProjectorIdentity{ChatID: "chat-old"},
		ContentSeq: map[string]int64{"run-old": 1},
	}
	projector, err := NewProjectorFromState(legacy)
	if err != nil {
		t.Fatalf("restore v1 state: %v", err)
	}
	if got := projector.Snapshot(); got.Version != ProjectorStateVersion || got.Identity.RunID != "" {
		t.Fatalf("legacy state was not upgraded as unbound v2: %#v", got)
	}
	if _, err := projector.ProjectStrict(testEvent(zenforge.EventRunStarted, 1, "run-old", nil)); err == nil {
		t.Fatal("legacy unbound state unexpectedly allowed strict projection")
	}

	current := NewProjectorWithIdentity(ProjectorIdentity{RunID: "run-current", ChatID: "chat"})
	data, err := json.Marshal(current.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runId":"run-current"`) {
		t.Fatalf("snapshot omitted run identity: %s", data)
	}
	restored, err := NewProjectorFromState(current.Snapshot())
	if err != nil || restored.Snapshot().Identity.RunID != "run-current" {
		t.Fatalf("restore v2 identity: %#v, %v", restored, err)
	}
}

func TestNewProjectorFromStateRejectsIllegalRunIdentity(t *testing.T) {
	tests := map[string]ProjectorState{
		"blank-run": {
			Version: ProjectorStateVersion, Identity: ProjectorIdentity{RunID: " "},
		},
		"legacy-run": {
			Version: projectorStateVersionLegacy, Identity: ProjectorIdentity{RunID: "run"},
		},
		"content-sequence-other-run": {
			Version: ProjectorStateVersion, Identity: ProjectorIdentity{RunID: "run"},
			ContentSeq: map[string]int64{"other": 1},
		},
		"active-content-other-run": {
			Version: ProjectorStateVersion, Identity: ProjectorIdentity{RunID: "run"},
			ContentSeq: map[string]int64{"other": 1}, ActiveContent: "other_c_1", ContentRunID: "other",
		},
	}
	for name, state := range tests {
		t.Run(name, func(t *testing.T) {
			if projector, err := NewProjectorFromState(state); err == nil || projector != nil {
				t.Fatalf("invalid state restored: %#v, %v", projector, err)
			}
		})
	}
}

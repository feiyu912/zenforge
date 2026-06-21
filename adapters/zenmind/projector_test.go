package zenmind

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestProjectorContentLifecycleGolden(t *testing.T) {
	events := []zenforge.Event{
		testEvent(zenforge.EventRunStarted, 100, "run_content", map[string]any{"input": "hello"}),
		testEvent(zenforge.EventModelStarted, 105, "run_content", map[string]any{"step": 1}),
		testEvent(zenforge.EventModelDelta, 110, "run_content", map[string]any{"textDelta": "Hello"}),
		testEvent(zenforge.EventModelDelta, 120, "run_content", map[string]any{"textDelta": " world"}),
		testEvent(zenforge.EventModelDone, 130, "run_content", map[string]any{"step": 1}),
		testEvent(zenforge.EventRunDone, 140, "run_content", map[string]any{"output": "Hello world"}),
	}
	assertProjectorGolden(t, "lifecycle_content.jsonl", ProjectorIdentity{ChatID: "chat_content", AgentKey: "zenmind"}, events)
}

func TestProjectorToolLifecycleGolden(t *testing.T) {
	events := []zenforge.Event{
		testEvent(zenforge.EventRunStarted, 200, "run_tool", nil),
		testEvent(zenforge.EventToolCall, 210, "run_tool", map[string]any{
			"toolCallId": "call_1",
			"toolName":   "shell",
			"arguments":  map[string]any{"timeout": 5, "command": "echo hi"},
		}),
		testEvent(zenforge.EventToolResult, 220, "run_tool", map[string]any{
			"toolCallId": "call_1", "toolName": "shell", "output": "hi\n", "exitCode": 0,
		}),
		testEvent(zenforge.EventRunDone, 230, "run_tool", map[string]any{"output": "done"}),
	}
	assertProjectorGolden(t, "lifecycle_tool.jsonl", ProjectorIdentity{ChatID: "chat_tool", AgentKey: "zenmind"}, events)
}

func TestProjectorTerminalErrorsAndCancellationNeverComplete(t *testing.T) {
	tests := []struct {
		name     string
		terminal zenforge.Event
		wantType string
	}{
		{name: "error", terminal: testEvent(zenforge.EventRunError, 20, "run_error", map[string]any{"error": "boom"}), wantType: "run.error"},
		{name: "cancel", terminal: testEvent(zenforge.EventRunCancelled, 20, "run_cancel", map[string]any{"error": "context canceled"}), wantType: "run.cancel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := NewProjector()
			projector.Project(testEvent(zenforge.EventModelDelta, 10, tt.terminal.RunID(), map[string]any{"textDelta": "partial"}))
			got := projector.Project(tt.terminal)
			if len(got) != 3 || got[0].Type != "content.end" || got[1].Type != "content.snapshot" || got[2].Type != tt.wantType {
				t.Fatalf("terminal projection = %#v", got)
			}
			for _, event := range got {
				if event.Type == "run.complete" {
					t.Fatal("error/cancel projection emitted run.complete")
				}
			}
			if after := projector.Project(testEvent(zenforge.EventRunDone, 30, tt.terminal.RunID(), nil)); len(after) != 0 {
				t.Fatalf("terminated projector emitted %#v", after)
			}
		})
	}
}

func TestProjectorIgnoresEventsWithoutPlatformWireEquivalent(t *testing.T) {
	ignored := []zenforge.EventType{
		zenforge.EventRunResumed,
		zenforge.EventStepStarted,
		zenforge.EventStepDone,
		zenforge.EventModelStarted,
		zenforge.EventTodoUpdated,
		zenforge.EventWorkspaceChanged,
		zenforge.EventApprovalRequested,
		zenforge.EventApprovalResolved,
		zenforge.EventApprovalExpired,
		zenforge.EventSubtaskStarted,
		zenforge.EventSubtaskEvent,
		zenforge.EventSubtaskDone,
		zenforge.EventSubtaskError,
		zenforge.EventTaskStarted,
		zenforge.EventTaskDone,
		zenforge.EventTaskError,
		zenforge.EventTaskCancelled,
		zenforge.EventCheckpointCreated,
		zenforge.EventType("custom.internal"),
	}
	projector := NewProjector()
	for _, eventType := range ignored {
		if got := projector.Project(testEvent(eventType, 10, "run_ignored", map[string]any{"value": "x"})); len(got) != 0 {
			t.Fatalf("%s projected as %#v", eventType, got)
		}
	}
	got := projector.Project(testEvent(zenforge.EventRunStarted, 20, "run_ignored", nil))
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("ignored events consumed live sequence numbers: %#v", got)
	}
}

func TestProjectorContentIDsAreDeterministicPerRun(t *testing.T) {
	projector := NewProjector()
	first := projector.Project(testEvent(zenforge.EventModelDelta, 1, "run_ids", map[string]any{"textDelta": "a"}))
	projector.Project(testEvent(zenforge.EventModelDone, 2, "run_ids", nil))
	second := projector.Project(testEvent(zenforge.EventModelDelta, 3, "run_ids", map[string]any{"textDelta": "b"}))
	if first[0].Payload["contentId"] != "run_ids_c_1" || second[0].Payload["contentId"] != "run_ids_c_2" {
		t.Fatalf("content IDs: first=%#v second=%#v", first, second)
	}
	if second[0].Seq != 5 || second[1].Seq != 6 {
		t.Fatalf("live sequence ordering: %#v", second)
	}
}

func TestMapperMarshalUsesFlatWireEnvelope(t *testing.T) {
	source := testEvent(zenforge.EventModelDelta, 42, "run_compat", map[string]any{"textDelta": "hello"}).WithSeq(3)
	data, err := json.Marshal(MapEvent(source))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"seq":3,"type":"content.delta","delta":"hello","runId":"run_compat","timestamp":42}`
	if string(data) != want {
		t.Fatalf("flat compatibility wire = %s, want %s", data, want)
	}
}

func TestProjectorRunStartIdentityFallbackAndPrecedence(t *testing.T) {
	source := testEvent(zenforge.EventRunStarted, 10, "run_identity", map[string]any{
		"chatId": "chat_from_event", "agentKey": "agent_from_event",
	})
	fallback := NewProjector().Project(source)
	if len(fallback) != 1 || fallback[0].Payload["chatId"] != "chat_from_event" || fallback[0].Payload["agentKey"] != "agent_from_event" {
		t.Fatalf("run.start payload fallback = %#v", fallback)
	}

	projector := NewProjectorWithIdentity(ProjectorIdentity{ChatID: "chat_explicit"})
	got := projector.Project(source)
	if len(got) != 1 || got[0].Payload["chatId"] != "chat_explicit" || got[0].Payload["agentKey"] != "agent_from_event" {
		t.Fatalf("run.start identity = %#v", got)
	}
}

func TestProjectorWithoutIdentityEmitsMinimalCompatibilityRunStart(t *testing.T) {
	got := NewProjector().Project(testEvent(zenforge.EventRunStarted, 10, "run_minimal", nil))
	if len(got) != 1 {
		t.Fatalf("run.start projection = %#v", got)
	}
	data, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"seq":1,"type":"run.start","runId":"run_minimal","timestamp":10}`
	if string(data) != want {
		t.Fatalf("minimal compatibility run.start = %s, want %s", data, want)
	}
}

func assertProjectorGolden(t *testing.T, name string, identity ProjectorIdentity, input []zenforge.Event) {
	t.Helper()
	projector := NewProjectorWithIdentity(identity)
	var actual bytes.Buffer
	for _, source := range input {
		for _, event := range projector.Project(source) {
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("Marshal(%s): %v", event.Type, err)
			}
			actual.Write(data)
			actual.WriteByte('\n')
		}
	}

	fixture, err := os.ReadFile(filepath.Join("testdata", "platform", name))
	if err != nil {
		t.Fatal(err)
	}
	newline := bytes.IndexByte(fixture, '\n')
	if newline < 0 {
		t.Fatalf("%s is missing fixture source metadata", name)
	}
	var metadata map[string]any
	if err := json.Unmarshal(fixture[:newline], &metadata); err != nil {
		t.Fatalf("%s metadata: %v", name, err)
	}
	if metadata["sourceCommit"] != "1893edb51b8dc691ae974cea2719a835e0e21de4" {
		t.Fatalf("%s source commit = %#v", name, metadata["sourceCommit"])
	}
	want := fixture[newline+1:]
	if !bytes.Equal(actual.Bytes(), want) {
		t.Fatalf("wire JSONL mismatch\n got: %s\nwant: %s", actual.Bytes(), want)
	}
}

func testEvent(eventType zenforge.EventType, timestamp int64, runID string, payload map[string]any) zenforge.Event {
	event := zenforge.NewEvent(eventType, runID, payload)
	event.Timestamp = timestamp
	return event
}

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

func TestProjectorPreservesSteerAsPlatformRequest(t *testing.T) {
	projector := NewProjector()
	got := projector.Project(testEvent(zenforge.EventRequestSteer, 210, "run_steer", map[string]any{
		"steerId": "steer_1", "message": "focus on tests",
	}))
	if len(got) != 1 || got[0].Type != "request.steer" || got[0].Seq != 1 {
		t.Fatalf("projection = %#v", got)
	}
	if got[0].Payload["runId"] != "run_steer" || got[0].Payload["steerId"] != "steer_1" || got[0].Payload["message"] != "focus on tests" {
		t.Fatalf("payload = %#v", got[0].Payload)
	}
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

func TestProjectorSnapshotResumePreservesContentAndLiveSequences(t *testing.T) {
	projector := NewProjectorWithIdentity(ProjectorIdentity{ChatID: "chat_resume", AgentKey: "zenmind"})
	projector.Project(testEvent(zenforge.EventRunStarted, 1, "run_resume", nil))
	before := projector.Project(testEvent(zenforge.EventModelDelta, 2, "run_resume", map[string]any{"textDelta": "hello"}))

	data, err := json.Marshal(projector.Snapshot())
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	var state ProjectorState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	resumed, err := NewProjectorFromState(state)
	if err != nil {
		t.Fatalf("NewProjectorFromState: %v", err)
	}
	continued := resumed.Project(testEvent(zenforge.EventModelDelta, 3, "run_resume", map[string]any{"textDelta": " world"}))
	closed := resumed.Project(testEvent(zenforge.EventModelDone, 4, "run_resume", nil))
	next := resumed.Project(testEvent(zenforge.EventModelDelta, 5, "run_resume", map[string]any{"textDelta": "again"}))

	if before[0].Payload["contentId"] != "run_resume_c_1" || continued[0].Payload["contentId"] != "run_resume_c_1" {
		t.Fatalf("resumed content ID changed: before=%#v continued=%#v", before, continued)
	}
	if continued[0].Seq != 4 || closed[0].Seq != 5 || closed[1].Seq != 6 || next[0].Seq != 7 || next[1].Seq != 8 {
		t.Fatalf("resumed live sequence is not monotonic: continued=%#v closed=%#v next=%#v", continued, closed, next)
	}
	if closed[1].Payload["text"] != "hello world" || next[0].Payload["contentId"] != "run_resume_c_2" {
		t.Fatalf("resumed content state = closed %#v, next %#v", closed, next)
	}
}

func TestProjectorSnapshotResumePreservesOpenTool(t *testing.T) {
	projector := NewProjector()
	first := projector.Project(testEvent(zenforge.EventToolCall, 1, "run_tool_resume", map[string]any{
		"toolCallId": "call_1", "toolName": "shell", "arguments": map[string]any{"command": "pwd"},
	}))
	state := projector.Snapshot()
	state.OpenTools["call_1"] = ProjectorToolState{Name: "mutated"}

	resumed, err := NewProjectorFromState(projector.Snapshot())
	if err != nil {
		t.Fatalf("NewProjectorFromState: %v", err)
	}
	second := resumed.Project(testEvent(zenforge.EventToolCall, 2, "run_tool_resume", map[string]any{
		"toolCallId": "call_1",
	}))
	result := resumed.Project(testEvent(zenforge.EventToolResult, 3, "run_tool_resume", map[string]any{
		"toolCallId": "call_1", "output": "/tmp",
	}))

	if len(first) != 2 || len(second) != 1 || second[0].Type != "tool.args" || second[0].Seq != 3 {
		t.Fatalf("resumed tool projection: first=%#v second=%#v", first, second)
	}
	if len(result) != 3 || result[0].Seq != 4 || result[1].Payload["toolId"] != "call_1" ||
		result[1].Payload["toolName"] != "shell" || result[1].Payload["arguments"] != `{"command":"pwd"}` || result[2].Seq != 6 {
		t.Fatalf("resumed tool close = %#v", result)
	}
}

func TestNewProjectorFromStateFailsClosed(t *testing.T) {
	tests := map[string]ProjectorState{
		"missing-version": {},
		"future-version":  {Version: "zenforge.zenmind_projector_state.v3"},
		"negative-live-seq": {
			Version: ProjectorStateVersion, NextSeq: -1,
		},
		"negative-content-seq": {
			Version: ProjectorStateVersion, ContentSeq: map[string]int64{"run": -1},
		},
		"orphaned-content-text": {Version: ProjectorStateVersion, ContentText: "partial"},
		"mismatched-content-id": {
			Version: ProjectorStateVersion, ContentSeq: map[string]int64{"run": 2}, ActiveContent: "run_c_1", ContentRunID: "run", ContentText: "partial",
		},
		"conflicting-tool-id": {
			Version: ProjectorStateVersion, ContentSeq: map[string]int64{"run": 1}, ActiveContent: "run_c_1", ContentRunID: "run",
			OpenTools: map[string]ProjectorToolState{"run_c_1": {}},
		},
		"terminated-with-open-tool": {
			Version: ProjectorStateVersion, Terminated: true, OpenTools: map[string]ProjectorToolState{"call_1": {}},
		},
	}
	for name, state := range tests {
		t.Run(name, func(t *testing.T) {
			projector, err := NewProjectorFromState(state)
			if err == nil || projector != nil {
				t.Fatalf("invalid state restored: projector=%#v err=%v", projector, err)
			}
		})
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

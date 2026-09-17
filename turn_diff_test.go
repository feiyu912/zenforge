package zenforge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	"github.com/feiyu912/zenforge/workspace/local"
)

func TestAgentEmitsTurnDiffAtTurnBoundary(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	store := workspacetools.NewTurnDiffStore()
	tools, err := workspacetools.Tools(workspacetools.Config{
		Workspace: ws,
		Snapshots: workspacetools.NewSnapshotStore(),
		TurnDiffs: store,
	})
	if err != nil {
		t.Fatalf("workspace Tools returned error: %v", err)
	}

	writeArgs, err := json.Marshal(map[string]string{
		"path":        "notes.txt",
		"content":     "hello\nturn diff\n",
		"description": "seed the turn-diff test",
	})
	if err != nil {
		t.Fatalf("marshal write args: %v", err)
	}
	editArgs, err := json.Marshal(map[string]any{
		"path":        "notes.txt",
		"oldString":   "hello",
		"newString":   "goodbye",
		"description": "second-turn edit",
	})
	if err != nil {
		t.Fatalf("marshal edit args: %v", err)
	}

	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{ToolCalls: []model.ToolCallSpec{{
			ID: "call_write", Name: "workspace_write", Arguments: writeArgs,
		}}}}},
		{events: []model.Event{{ToolCalls: []model.ToolCallSpec{{
			ID: "call_edit", Name: "workspace_edit", Arguments: editArgs,
		}}}}},
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       tools,
		Events:      events,
		Checkpoints: checkpoints,
		TurnDiffs:   store,
		MaxSteps:    6,
	})

	stream, err := agent.Stream(ctx, Task{Input: "write then edit"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}

	turnDiffs := eventsByType(collected, EventTurnDiff)
	if len(turnDiffs) != 2 {
		t.Fatalf("turn.diff events = %d, want 2 (one per mutating turn)", len(turnDiffs))
	}

	first := turnDiffs[0].Payload
	if first["fileCount"] != 1 {
		t.Fatalf("first turn payload = %v", first)
	}
	files, ok := first["files"].([]workspacetools.TurnFileDiff)
	if !ok || len(files) != 1 || files[0].Path != "notes.txt" {
		t.Fatalf("first turn files = %#v", first["files"])
	}
	if !strings.Contains(files[0].Diff, "+hello") || !strings.Contains(files[0].Diff, "@@ -0,0 +1,2 @@") {
		t.Fatalf("creation diff = %q", files[0].Diff)
	}

	second := turnDiffs[1].Payload
	files, ok = second["files"].([]workspacetools.TurnFileDiff)
	if !ok || len(files) != 1 {
		t.Fatalf("second turn files = %#v", second["files"])
	}
	if !strings.Contains(files[0].Diff, "-hello") || !strings.Contains(files[0].Diff, "+goodbye") {
		t.Fatalf("edit diff = %q", files[0].Diff)
	}
}

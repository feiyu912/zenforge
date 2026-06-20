package task

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/feiyu912/zenforge/tool"
)

func TestTaskToolSchemaAndAlias(t *testing.T) {
	primary := Must(Config{})
	if primary.Name() != Name {
		t.Fatalf("primary name = %q", primary.Name())
	}
	alias := Must(Config{Alias: true})
	if alias.Name() != Alias {
		t.Fatalf("alias name = %q", alias.Name())
	}
	if primary.Schema()["type"] != "object" {
		t.Fatalf("unexpected schema: %#v", primary.Schema())
	}
	properties := primary.Schema()["properties"].(map[string]any)
	if properties["options"] == nil {
		t.Fatalf("expected options schema: %#v", primary.Schema())
	}
}

func TestDecodeDefersHostTaskCeiling(t *testing.T) {
	tasks := make([]map[string]string, 9)
	for i := range tasks {
		tasks[i] = map[string]string{"agent": "worker", "input": fmt.Sprintf("task %d", i+1)}
	}
	raw, err := json.Marshal(map[string]any{"tasks": tasks})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if _, err := Decode(raw); err != nil {
		t.Fatalf("Decode applied an implicit host ceiling: %v", err)
	}

	withRequestLimit, err := json.Marshal(map[string]any{
		"tasks":   tasks[:2],
		"options": map[string]any{"maxTasks": 1},
	})
	if err != nil {
		t.Fatalf("Marshal request limit returned error: %v", err)
	}
	if _, err := Decode(withRequestLimit); err == nil {
		t.Fatalf("expected request-level maxTasks error")
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	raw := json.RawMessage(`{"tasks":[{"agent":"worker","input":"one"}]} {}`)
	if _, err := Decode(raw); err == nil {
		t.Fatalf("expected trailing JSON error")
	}
}

func TestDecodeRejectsNegativeTaskLimit(t *testing.T) {
	raw := json.RawMessage(`{"tasks":[{"agent":"worker","input":"one"}],"options":{"maxTasks":-1}}`)
	if _, err := Decode(raw); err == nil {
		t.Fatalf("expected negative maxTasks error")
	}
}

func TestTaskToolValidatesArgs(t *testing.T) {
	req, err := Decode(json.RawMessage(`{"tasks":[{"agent":"researcher","input":"read docs"}],"options":{"parallel":true,"failFast":true,"maxTasks":3}}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if !req.Options.Parallel || !req.Options.FailFast || req.Options.MaxTasks != 3 {
		t.Fatalf("options were not decoded: %#v", req.Options)
	}
	if _, err := Decode(json.RawMessage(`{"tasks":[{"input":"missing agent"}]}`)); err == nil {
		t.Fatalf("expected missing agent error")
	}
	if _, err := Decode(json.RawMessage(`{"tasks":[{"agent":"researcher","input":"read docs"}],"options":{"allowNested":true}}`)); err == nil {
		t.Fatalf("expected unknown option error")
	}
	result, err := Must(Config{}).Call(context.Background(), json.RawMessage(`{"tasks":[{"agent":"researcher","input":"read docs"}]}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected harness runtime error, got result=%#v err=%v", result, err)
	}
}

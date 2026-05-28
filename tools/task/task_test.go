package task

import (
	"context"
	"encoding/json"
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
}

func TestTaskToolValidatesArgs(t *testing.T) {
	_, err := Decode(json.RawMessage(`{"tasks":[{"agent":"researcher","input":"read docs"}]}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if _, err := Decode(json.RawMessage(`{"tasks":[{"input":"missing agent"}]}`)); err == nil {
		t.Fatalf("expected missing agent error")
	}
	result, err := Must(Config{}).Call(context.Background(), json.RawMessage(`{"tasks":[{"agent":"researcher","input":"read docs"}]}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected harness runtime error, got result=%#v err=%v", result, err)
	}
}

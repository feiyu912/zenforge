package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/workspace/local"
)

func TestWorkspaceToolsReadListGrepWrite(t *testing.T) {
	ws, err := local.New(local.Config{Root: t.TempDir(), CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	write, err := Write(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Write tool returned error: %v", err)
	}
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"hello\nTODO\n","description":"seed file"}`), tool.Context{}); err != nil {
		t.Fatalf("write Call returned error: %v", err)
	}

	read, err := Read(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Read tool returned error: %v", err)
	}
	result, err := read.Call(context.Background(), json.RawMessage(`{"path":"README.md","limit":5}`), tool.Context{})
	if err != nil {
		t.Fatalf("read Call returned error: %v", err)
	}
	if result.Structured["content"] != "hello" {
		t.Fatalf("unexpected read result: %#v", result.Structured)
	}

	list, err := List(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("List tool returned error: %v", err)
	}
	result, err = list.Call(context.Background(), json.RawMessage(`{"path":"."}`), tool.Context{})
	if err != nil {
		t.Fatalf("list Call returned error: %v", err)
	}
	if result.Structured["entries"] == nil {
		t.Fatalf("expected entries: %#v", result.Structured)
	}

	grep, err := Grep(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Grep tool returned error: %v", err)
	}
	result, err = grep.Call(context.Background(), json.RawMessage(`{"path":".","pattern":"TODO"}`), tool.Context{})
	if err != nil {
		t.Fatalf("grep Call returned error: %v", err)
	}
	if result.Structured["matches"] == nil {
		t.Fatalf("expected matches: %#v", result.Structured)
	}
}

func TestWorkspaceWriteRequiresDescription(t *testing.T) {
	ws, err := local.New(local.Config{Root: t.TempDir(), CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	write, err := Write(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Write tool returned error: %v", err)
	}
	result, err := write.Call(context.Background(), json.RawMessage(`{"path":"a.txt","content":"x"}`), tool.Context{})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("expected description error, got result=%#v err=%v", result, err)
	}
}

func TestWorkspaceWriteRequiresFreshReadSnapshot(t *testing.T) {
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	snapshots := NewSnapshotStore()
	config := Config{Workspace: ws, Snapshots: snapshots, RequireReadBeforeWrite: true}
	write, err := Write(config)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"new","description":"overwrite"}`), tool.Context{}); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("expected snapshot required, got %v", err)
	}

	read, err := Read(config)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if _, err := read.Call(context.Background(), json.RawMessage(`{"path":"README.md"}`), tool.Context{}); err != nil {
		t.Fatalf("read Call returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed elsewhere"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"new","description":"overwrite"}`), tool.Context{}); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("expected stale snapshot, got %v", err)
	}

	if _, err := read.Call(context.Background(), json.RawMessage(`{"path":"README.md"}`), tool.Context{}); err != nil {
		t.Fatalf("second read Call returned error: %v", err)
	}
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"new","description":"fresh overwrite"}`), tool.Context{}); err != nil {
		t.Fatalf("fresh write returned error: %v", err)
	}
}

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	workspacepkg "github.com/feiyu912/zenforge/workspace"
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

func TestWorkspaceWriteSnapshotsAreRunScoped(t *testing.T) {
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	config := Config{Workspace: ws, Snapshots: NewSnapshotStore(), RequireReadBeforeWrite: true}
	read, err := Read(config)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	write, err := Write(config)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := read.Call(context.Background(), json.RawMessage(`{"path":"README.md"}`), tool.Context{RunID: "run_1"}); err != nil {
		t.Fatalf("read Call returned error: %v", err)
	}
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"other","description":"other run"}`), tool.Context{RunID: "run_2"}); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("expected run-scoped snapshot required, got %v", err)
	}
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"new","description":"same run"}`), tool.Context{RunID: "run_1"}); err != nil {
		t.Fatalf("same-run write returned error: %v", err)
	}
}

func TestWorkspaceSnapshotDetectsContentHashChange(t *testing.T) {
	store := NewSnapshotStore()
	old := workspacepkg.FileInfo{Path: "README.md", Size: 3, ModTime: 10, SHA256: "old"}
	current := workspacepkg.FileInfo{Path: "README.md", Size: 3, ModTime: 10, SHA256: "new"}
	store.RecordForRun("run_1", old)
	if err := store.CheckForRun("run_1", current); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("expected stale snapshot, got %v", err)
	}
}

func TestWorkspacePolicyBlocksOutsideRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("seed docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tmp", "b.md"), []byte("blocked"), 0o644); err != nil {
		t.Fatalf("seed tmp: %v", err)
	}
	ws, err := local.New(local.Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	read, err := Read(Config{Workspace: ws, Policy: policy.FilePolicy{ReadRoots: []string{"docs"}}})
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if _, err := read.Call(context.Background(), json.RawMessage(`{"path":"docs/a.md"}`), tool.Context{}); err != nil {
		t.Fatalf("allowed read returned error: %v", err)
	}
	result, err := read.Call(context.Background(), json.RawMessage(`{"path":"tmp/b.md"}`), tool.Context{})
	if !errors.Is(err, policy.ErrFileAccessDenied) {
		t.Fatalf("expected file access denial, got result=%#v err=%v", result, err)
	}
	if result.Structured["accessPlan"] == nil {
		t.Fatalf("expected access plan in denial result: %#v", result.Structured)
	}
}

func TestWorkspacePolicyReturnsApprovalRequest(t *testing.T) {
	ws, err := local.New(local.Config{Root: t.TempDir(), CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	write, err := Write(Config{
		Workspace: ws,
		Policy: policy.FilePolicy{
			WriteRoots:      []string{"docs"},
			RequireApproval: true,
		},
	})
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	raw := json.RawMessage(`{"path":"tmp/out.md","content":"approved","description":"write generated note"}`)
	call := tool.Context{RunID: "run_1", ToolCallID: "call_1"}
	result, err := write.Call(context.Background(), raw, call)
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("expected approval required, got result=%#v err=%v", result, err)
	}
	req, ok := approval.RequestFromResult(result)
	if !ok {
		t.Fatalf("expected approval request result: %#v", result.Structured)
	}
	if req.Operation != "workspace.write" || req.Risk != approval.RiskHigh {
		t.Fatalf("unexpected request: %#v", req)
	}
	if req.Payload["writePlan"] == nil || req.Payload["fingerprint"] == "" || req.Payload["ruleKey"] == "" {
		t.Fatalf("expected write plan and approval keys: %#v", req.Payload)
	}
	metadata := approval.ApprovedMetadata(nil, req, approval.Decision{Action: approval.DecisionApprove})
	if _, err := write.Call(context.Background(), raw, tool.Context{RunID: "run_1", ToolCallID: "call_2", Metadata: metadata}); err != nil {
		t.Fatalf("approved write returned error: %v", err)
	}
}

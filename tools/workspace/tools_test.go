package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
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

func TestWorkspaceReadHandlesMaximumLimitWithoutOverflow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	ws, err := local.New(local.Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	read, err := Read(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"path":"a.txt","offset":1,"limit":%d}`, math.MaxInt))
	result, err := read.Call(context.Background(), raw, tool.Context{})
	if err != nil {
		t.Fatalf("read Call returned error: %v", err)
	}
	if result.Structured["content"] != "bcdef" {
		t.Fatalf("unexpected read result: %#v", result.Structured)
	}
}

func TestWorkspaceReadReturnsStatFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	ws, err := local.New(local.Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	statErr := errors.New("stat failed")
	read, err := Read(Config{Workspace: statFailWorkspace{Workspace: ws, err: statErr}})
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	result, err := read.Call(context.Background(), json.RawMessage(`{"path":"a.txt"}`), tool.Context{})
	if !errors.Is(err, statErr) || result.ExitCode == 0 {
		t.Fatalf("expected stat failure, got result=%#v err=%v", result, err)
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
	if _, err := write.Call(context.Background(), json.RawMessage(`{"path":"README.md","content":"newer","description":"overwrite again"}`), tool.Context{}); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("expected a new read after writing, got %v", err)
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

type statFailWorkspace struct {
	workspacepkg.Workspace
	err error
}

func (w statFailWorkspace) Stat(context.Context, string) (workspacepkg.FileInfo, error) {
	return workspacepkg.FileInfo{}, w.err
}

func editTestSetup(t *testing.T, content string, requireRead bool) (tool.Tool, tool.Tool, Config, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	config := Config{Workspace: ws, Snapshots: NewSnapshotStore(), RequireReadBeforeWrite: requireRead}
	edit, err := Edit(config)
	if err != nil {
		t.Fatalf("Edit returned error: %v", err)
	}
	read, err := Read(config)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	return edit, read, config, root
}

func TestWorkspaceEditReplacesExactMatch(t *testing.T) {
	edit, read, _, root := editTestSetup(t, "alpha\nbeta\ngamma\n", true)
	ctx := context.Background()
	if _, err := read.Call(ctx, json.RawMessage(`{"path":"notes.md"}`), tool.Context{}); err != nil {
		t.Fatalf("read Call returned error: %v", err)
	}
	raw := json.RawMessage(`{"path":"notes.md","oldString":"beta","newString":"BETA","description":"upper case beta"}`)
	result, err := edit.Call(ctx, raw, tool.Context{})
	if err != nil {
		t.Fatalf("edit Call returned error: %v (result %#v)", err, result)
	}
	if result.Structured["message"] != "The file notes.md has been updated successfully." {
		t.Fatalf("unexpected message: %#v", result.Structured["message"])
	}
	if result.Structured["replacements"] != float64(1) && result.Structured["replacements"] != 1 {
		t.Fatalf("unexpected replacements: %#v", result.Structured["replacements"])
	}
	data, err := os.ReadFile(filepath.Join(root, "notes.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestWorkspaceEditRejectsMissingAndAmbiguousMatches(t *testing.T) {
	edit, read, _, root := editTestSetup(t, "dup\ndup\n", true)
	ctx := context.Background()
	if _, err := read.Call(ctx, json.RawMessage(`{"path":"notes.md"}`), tool.Context{}); err != nil {
		t.Fatalf("read Call returned error: %v", err)
	}
	if _, err := edit.Call(ctx, json.RawMessage(`{"path":"notes.md","oldString":"absent","newString":"x","description":"missing"}`), tool.Context{}); !errors.Is(err, ErrEditNotFound) {
		t.Fatalf("expected ErrEditNotFound, got %v", err)
	}
	if _, err := read.Call(ctx, json.RawMessage(`{"path":"notes.md"}`), tool.Context{}); err != nil {
		t.Fatalf("second read returned error: %v", err)
	}
	_, err := edit.Call(ctx, json.RawMessage(`{"path":"notes.md","oldString":"dup","newString":"one","description":"ambiguous"}`), tool.Context{})
	if !errors.Is(err, ErrEditAmbiguous) {
		t.Fatalf("expected ErrEditAmbiguous, got %v", err)
	}
	if !strings.Contains(err.Error(), "matched 2 times") || !strings.Contains(err.Error(), "replaceAll") {
		t.Fatalf("ambiguous error lacks guidance: %v", err)
	}
	if _, err := read.Call(ctx, json.RawMessage(`{"path":"notes.md"}`), tool.Context{}); err != nil {
		t.Fatalf("third read returned error: %v", err)
	}
	result, err := edit.Call(ctx, json.RawMessage(`{"path":"notes.md","oldString":"dup","newString":"one","replaceAll":true,"description":"replace both"}`), tool.Context{})
	if err != nil {
		t.Fatalf("replaceAll edit returned error: %v", err)
	}
	if result.Structured["message"] != "All 2 occurrences in notes.md were successfully replaced." {
		t.Fatalf("unexpected replaceAll message: %#v", result.Structured["message"])
	}
	data, err := os.ReadFile(filepath.Join(root, "notes.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "one\none\n" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestWorkspaceEditRequiresFreshReadSnapshot(t *testing.T) {
	edit, read, _, root := editTestSetup(t, "old text\n", true)
	ctx := context.Background()
	raw := json.RawMessage(`{"path":"notes.md","oldString":"old","newString":"new","description":"freshen"}`)
	if _, err := edit.Call(ctx, raw, tool.Context{}); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("expected snapshot required, got %v", err)
	}
	if _, err := read.Call(ctx, json.RawMessage(`{"path":"notes.md"}`), tool.Context{}); err != nil {
		t.Fatalf("read returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("changed elsewhere\n"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	if _, err := edit.Call(ctx, raw, tool.Context{}); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("expected stale snapshot, got %v", err)
	}
	if _, err := read.Call(ctx, json.RawMessage(`{"path":"notes.md"}`), tool.Context{}); err != nil {
		t.Fatalf("second read returned error: %v", err)
	}
	if _, err := edit.Call(ctx, json.RawMessage(`{"path":"notes.md","oldString":"changed","newString":"edited","description":"freshen"}`), tool.Context{}); err != nil {
		t.Fatalf("fresh edit returned error: %v", err)
	}
	// A successful edit bumps the file version, so the next edit needs a
	// fresh read — the compare-and-swap guard applies to edits too.
	if _, err := edit.Call(ctx, json.RawMessage(`{"path":"notes.md","oldString":"edited","newString":"again","description":"second edit"}`), tool.Context{}); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("expected stale snapshot after edit, got %v", err)
	}
}

func TestWorkspaceEditValidatesArguments(t *testing.T) {
	edit, _, _, _ := editTestSetup(t, "content\n", false)
	ctx := context.Background()
	cases := []struct {
		name string
		raw  string
	}{
		{"empty oldString", `{"path":"notes.md","oldString":"","newString":"x","description":"d"}`},
		{"identical strings", `{"path":"notes.md","oldString":"x","newString":"x","description":"d"}`},
		{"missing description", `{"path":"notes.md","oldString":"x","newString":"y"}`},
	}
	for _, tc := range cases {
		if _, err := edit.Call(ctx, json.RawMessage(tc.raw), tool.Context{}); !errors.Is(err, tool.ErrInvalidArguments) {
			t.Fatalf("%s: expected ErrInvalidArguments, got %v", tc.name, err)
		}
	}
}

func TestWorkspaceEditDeletesWithEmptyNewString(t *testing.T) {
	edit, _, _, root := editTestSetup(t, "keep\ndrop\n", false)
	if _, err := edit.Call(context.Background(), json.RawMessage(`{"path":"notes.md","oldString":"drop\n","newString":"","description":"remove line"}`), tool.Context{}); err != nil {
		t.Fatalf("delete edit returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "notes.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestWorkspaceEditMissingFile(t *testing.T) {
	edit, _, _, _ := editTestSetup(t, "content\n", false)
	_, err := edit.Call(context.Background(), json.RawMessage(`{"path":"absent.md","oldString":"x","newString":"y","description":"missing file"}`), tool.Context{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

func TestWorkspaceEditRoutesThroughFilePolicyApproval(t *testing.T) {
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	edit, err := Edit(Config{
		Workspace: ws,
		Policy: policy.FilePolicy{
			WriteRoots:      []string{"docs"},
			RequireApproval: true,
		},
	})
	if err != nil {
		t.Fatalf("Edit returned error: %v", err)
	}
	raw := json.RawMessage(`{"path":"notes.md","oldString":"old","newString":"new","description":"policy edit"}`)
	result, err := edit.Call(context.Background(), raw, tool.Context{RunID: "run_1", ToolCallID: "call_1"})
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
	writePlan, _ := req.Payload["writePlan"].(policy.FileWritePlan)
	if writePlan.Description != "policy edit" || writePlan.FilePath != "notes.md" {
		t.Fatalf("unexpected write plan: %#v", writePlan)
	}
	metadata := approval.ApprovedMetadata(nil, req, approval.Decision{Action: approval.DecisionApprove})
	if _, err := edit.Call(context.Background(), raw, tool.Context{RunID: "run_1", ToolCallID: "call_2", Metadata: metadata}); err != nil {
		t.Fatalf("approved edit returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "notes.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "new\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestWorkspaceToolsIncludesEdit(t *testing.T) {
	ws, err := local.New(local.Config{Root: t.TempDir(), CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	list, err := Tools(Config{Workspace: ws, Snapshots: NewSnapshotStore()})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	var names []string
	for _, current := range list {
		names = append(names, current.Name())
	}
	want := []string{"workspace_read", "workspace_list", "workspace_glob", "workspace_grep", "workspace_write", "workspace_edit"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", names, want)
	}
}

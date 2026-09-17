package patch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

func newTestTool(t *testing.T, requireRead bool) (tool.Tool, *workspacetools.SnapshotStore, string, *workspacetools.TurnDiffStore) {
	t.Helper()
	root := t.TempDir()
	ws, err := workspacelocal.New(workspacelocal.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	snapshots := workspacetools.NewSnapshotStore()
	turnDiffs := workspacetools.NewTurnDiffStore()
	instance, err := New(Config{
		Workspace:              ws,
		Snapshots:              snapshots,
		RequireReadBeforeWrite: requireRead,
		TurnDiffs:              turnDiffs,
	})
	if err != nil {
		t.Fatalf("patch New returned error: %v", err)
	}
	return instance, snapshots, root, turnDiffs
}

func callPatch(t *testing.T, instance tool.Tool, call tool.Context, patch, description string) (tool.Result, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"patch": patch, "description": description})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	return instance.Call(context.Background(), args, call)
}

func mustApply(t *testing.T, instance tool.Tool, call tool.Context, patch, description string) tool.Result {
	t.Helper()
	result, err := callPatch(t, instance, call, patch, description)
	if err != nil {
		t.Fatalf("patch returned error: %v (%+v)", err, result)
	}
	return result
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", name, err)
	}
	return string(data)
}

func TestApplyPatchAddsUpdatesMovesAndDeletes(t *testing.T) {
	instance, _, root, turnDiffs := newTestTool(t, false)
	writeFile(t, root, "update.txt", "one\ntwo\n")
	writeFile(t, root, "move.txt", "before\n")
	writeFile(t, root, "delete.txt", "gone\n")

	result := mustApply(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_1"}, `*** Begin Patch
*** Add File: nested/new.txt
+hello
*** Update File: update.txt
@@
-two
+TWO
*** Update File: move.txt
*** Move to: renamed.txt
*** Delete File: delete.txt
*** End Patch`, "restructure files")

	if result.Error != "" {
		t.Fatalf("patch failed: %+v", result)
	}
	if got := readFile(t, root, "nested/new.txt"); got != "hello\n" {
		t.Fatalf("added file = %q", got)
	}
	if got := readFile(t, root, "update.txt"); got != "one\nTWO\n" {
		t.Fatalf("updated file = %q", got)
	}
	if got := readFile(t, root, "renamed.txt"); got != "before\n" {
		t.Fatalf("moved file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "move.txt")); !os.IsNotExist(err) {
		t.Fatalf("original move source survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file survived: %v", err)
	}

	added, _ := result.Structured["added"].([]any)
	modified, _ := result.Structured["modified"].([]any)
	deleted, _ := result.Structured["deleted"].([]any)
	if len(added) != 1 || len(modified) != 2 || len(deleted) != 1 {
		t.Fatalf("structured result = %#v", result.Structured)
	}
	if message, _ := result.Structured["message"].(string); !strings.Contains(message, "Success. Updated the following files:") {
		t.Fatalf("message = %q", message)
	}

	// Turn diffs captured every mutation, including the deletion.
	drained := turnDiffs.Drain("run_1", time.Second)
	rendered := map[string]string{}
	for _, entry := range drained {
		rendered[entry.Path] = entry.Diff + entry.Note
	}
	for _, want := range []string{"update.txt", "move.txt", "delete.txt", "renamed.txt", "nested/new.txt"} {
		if _, ok := rendered[want]; !ok {
			t.Fatalf("turn diff captured %v, missing %s", rendered, want)
		}
	}
	if !strings.Contains(rendered["update.txt"], "-two") || !strings.Contains(rendered["update.txt"], "+TWO") {
		t.Fatalf("update diff = %q", rendered["update.txt"])
	}
	if diff := rendered["delete.txt"]; !strings.Contains(diff, "-gone") {
		t.Fatalf("delete diff = %q", diff)
	}
}

func TestApplyPatchEnforcesObservationPolicy(t *testing.T) {
	instance, snapshots, root, _ := newTestTool(t, true)
	writeFile(t, root, "exists.txt", "content\n")
	store, err := workspacelocal.New(workspacelocal.Config{Root: root})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	info, err := store.Stat(context.Background(), "exists.txt")
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}

	// Updating a file without a same-run read is refused.
	result, _ := callPatch(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_1"},
		"*** Begin Patch\n*** Update File: exists.txt\n@@\n-content\n+changed\n*** End Patch", "update")
	if result.Error == "" || !strings.Contains(result.Error, "snapshot") {
		t.Fatalf("unread update result = %+v", result)
	}

	// Creating a file whose absence was never observed is refused.
	result, _ = callPatch(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_2"},
		"*** Begin Patch\n*** Add File: brand-new.txt\n+hello\n*** End Patch", "create")
	if result.Error == "" || !strings.Contains(result.Error, "does not exist; read it first") {
		t.Fatalf("unobserved create result = %+v", result)
	}

	// After the run observed the read and the absence, both succeed.
	snapshots.RecordForRun("run_1", info)
	snapshots.RecordAbsentForRun("run_1", "brand-new.txt")
	if result := mustApply(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_3"},
		"*** Begin Patch\n*** Update File: exists.txt\n@@\n-content\n+changed\n*** Add File: brand-new.txt\n+hello\n*** End Patch", "update and create"); result.Error != "" {
		t.Fatalf("observed patch failed: %+v", result)
	}
	if got := readFile(t, root, "exists.txt"); got != "changed\n" {
		t.Fatalf("updated content = %q", got)
	}
}

func TestApplyPatchRequiresDescriptionAndValidEnvelope(t *testing.T) {
	instance, _, _, _ := newTestTool(t, false)

	_, err := callPatch(t, instance, tool.Context{}, "*** Begin Patch\n*** Add File: a.txt\n+1\n*** End Patch", "")
	if err == nil || !strings.Contains(err.Error(), "description is required") {
		t.Fatalf("missing description error = %v", err)
	}
	_, err = callPatch(t, instance, tool.Context{}, "not a patch", "bad")
	if err == nil || !strings.Contains(err.Error(), "The first line of the patch") {
		t.Fatalf("invalid envelope error = %v", err)
	}
	_, err = callPatch(t, instance, tool.Context{}, "*** Begin Patch\n*** End Patch", "empty")
	if err == nil || !strings.Contains(err.Error(), "does not change any file") {
		t.Fatalf("empty patch error = %v", err)
	}
}

func TestApplyPatchReportsMissingContext(t *testing.T) {
	instance, _, root, _ := newTestTool(t, false)
	writeFile(t, root, "f.txt", "present\n")
	_, err := callPatch(t, instance, tool.Context{}, "*** Begin Patch\n*** Update File: f.txt\n@@\n-missing\n+other\n*** End Patch", "edit")
	if err == nil || !strings.Contains(err.Error(), "Failed to find expected lines in f.txt") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyPatchDeniesPathsOutsidePolicyRoots(t *testing.T) {
	root := t.TempDir()
	ws, err := workspacelocal.New(workspacelocal.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	instance, err := New(Config{
		Workspace:  ws,
		FilePolicy: policy.FilePolicy{WriteRoots: []string{"src"}, RequireApproval: false},
		TurnDiffs:  workspacetools.NewTurnDiffStore(),
	})
	if err != nil {
		t.Fatalf("patch New returned error: %v", err)
	}
	result, err := callPatch(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_1"},
		"*** Begin Patch\n*** Add File: other/new.txt\n+hello\n*** End Patch", "outside the write root")
	if !errors.Is(err, policy.ErrFileAccessDenied) {
		t.Fatalf("denial error = %v", err)
	}
	if result.Structured["paths"] == nil {
		t.Fatalf("denial payload = %#v", result.Structured)
	}

	allowed := mustApply(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_2"},
		"*** Begin Patch\n*** Add File: src/new.txt\n+hello\n*** End Patch", "inside the write root")
	if allowed.Error != "" {
		t.Fatalf("allowed patch failed: %+v", allowed)
	}
}

func TestApplyPatchRequiresApprovalWhenPolicyAsks(t *testing.T) {
	root := t.TempDir()
	ws, err := workspacelocal.New(workspacelocal.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	instance, err := New(Config{
		Workspace:  ws,
		FilePolicy: policy.FilePolicy{RequireApproval: true},
		TurnDiffs:  workspacetools.NewTurnDiffStore(),
	})
	if err != nil {
		t.Fatalf("patch New returned error: %v", err)
	}
	result, err := callPatch(t, instance, tool.Context{RunID: "run_1", ToolCallID: "call_1"},
		"*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch", "needs approval")
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("approval error = %v", err)
	}
	request, ok := result.Structured["approval"].(approval.Request)
	if !ok {
		t.Fatalf("structured approval = %#v", result.Structured)
	}
	if request.Operation != "workspace.patch" || request.Title != "Approve patch" {
		t.Fatalf("request = %#v", request)
	}
	if request.Payload["fingerprint"] == nil || request.Payload["patch"] == nil || request.Payload["paths"] == nil {
		t.Fatalf("approval payload = %#v", request.Payload)
	}
}

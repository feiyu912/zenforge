package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

func testWorkspace(t *testing.T) (*workspace, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatalf("newWorkspace: %v", err)
	}
	return ws, root
}

func toolByName(t *testing.T, tools []tool.BaseTool, name string) tool.InvokableTool {
	t.Helper()
	for _, tl := range tools {
		inv, ok := tl.(tool.InvokableTool)
		if !ok {
			t.Fatalf("tool %T is not an InvokableTool", tl)
		}
		info, err := inv.Info(context.Background())
		if err != nil {
			t.Fatalf("Info(%s): %v", name, err)
		}
		if info.Name == name {
			return inv
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

// TestToolNamesAndSchemas pins the exact contract names and arguments.
func TestToolNamesAndSchemas(t *testing.T) {
	ws, _ := testWorkspace(t)
	tools, err := newTools(ws, ApprovalApprove)
	if err != nil {
		t.Fatalf("newTools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("got %d tools, want 3", len(tools))
	}

	want := map[string][]string{
		"read_file":  {"path"},
		"write_file": {"path", "content"},
		"run_shell":  {"command"},
	}
	for name, props := range want {
		inv := toolByName(t, tools, name)
		info, err := inv.Info(context.Background())
		if err != nil {
			t.Fatalf("Info(%s): %v", name, err)
		}
		if info.ParamsOneOf == nil {
			t.Fatalf("%s: no parameter schema", name)
		}
		js, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatalf("%s: ToJSONSchema: %v", name, err)
		}
		b, err := json.Marshal(js)
		if err != nil {
			t.Fatalf("%s: marshal schema: %v", name, err)
		}
		var decoded struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("%s: unmarshal schema: %v (%s)", name, err, b)
		}
		if len(decoded.Properties) != len(props) {
			t.Errorf("%s: schema properties = %v, want exactly %v", name, keys(decoded.Properties), props)
		}
		for _, p := range props {
			if _, ok := decoded.Properties[p]; !ok {
				t.Errorf("%s: schema is missing argument %q (%s)", name, p, b)
			}
		}
		if len(decoded.Required) != len(props) {
			t.Errorf("%s: required = %v, want all of %v", name, decoded.Required, props)
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestReadWriteFileToolsConfinement(t *testing.T) {
	ws, _ := testWorkspace(t)
	tools, err := newTools(ws, ApprovalApprove)
	if err != nil {
		t.Fatalf("newTools: %v", err)
	}
	readFile := toolByName(t, tools, "read_file")
	writeFile := toolByName(t, tools, "write_file")
	ctx := context.Background()

	out, err := writeFile.InvokableRun(ctx, `{"path":"out.txt","content":"hello\nworld"}`)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out, "out.txt") {
		t.Errorf("write_file result = %q, want it to name the file", out)
	}

	got, err := readFile.InvokableRun(ctx, `{"path":"out.txt"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if got != "hello\nworld" {
		t.Errorf("read_file returned %q, want the raw content", got)
	}

	if _, err := readFile.InvokableRun(ctx, `{"path":"../escape.txt"}`); err == nil {
		t.Error("read_file accepted a path outside the workspace")
	}
	if _, err := writeFile.InvokableRun(ctx, `{"path":"/tmp/zenforge-eino-tool-escape.txt","content":"x"}`); err == nil {
		t.Error("write_file accepted a path outside the workspace")
	}
	if _, err := os.Stat("/tmp/zenforge-eino-tool-escape.txt"); err == nil {
		t.Fatal("write_file created a file outside the workspace")
	}
}

// TestRunShellInterruptsBeforeRunning proves the tool does not execute the
// command on its first invocation: it returns Eino's interrupt signal, and the
// command's side effect is absent.
func TestRunShellInterruptsBeforeRunning(t *testing.T) {
	ws, root := testWorkspace(t)
	tools, err := newTools(ws, ApprovalApprove)
	if err != nil {
		t.Fatalf("newTools: %v", err)
	}
	runShell := toolByName(t, tools, "run_shell")
	ctx := context.Background()

	_, err = runShell.InvokableRun(ctx, `{"command":"touch ran.txt"}`)
	if err == nil {
		t.Fatal("run_shell returned no error on first invocation; it should interrupt")
	}
	if _, ok := compose.IsInterruptRerunError(err); !ok {
		t.Fatalf("run_shell error %v is not an Eino interrupt error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "ran.txt")); statErr == nil {
		t.Fatal("run_shell executed the command before approval")
	}
}

func TestRunShellRejectsEmptyCommand(t *testing.T) {
	ws, _ := testWorkspace(t)
	tools, err := newTools(ws, ApprovalApprove)
	if err != nil {
		t.Fatalf("newTools: %v", err)
	}
	runShell := toolByName(t, tools, "run_shell")
	if _, err := runShell.InvokableRun(context.Background(), `{"command":"   "}`); err == nil {
		t.Fatal("run_shell accepted an empty command")
	}
}

// TestExecShellRunsRealSubprocess proves run_shell is a real subprocess in the
// workspace: it exercises the same execShell the approved path calls.
func TestExecShellRunsRealSubprocess(t *testing.T) {
	ws, root := testWorkspace(t)

	out, err := execShell(context.Background(), ws, "echo approved > approval.txt && pwd && echo done")
	if err != nil {
		t.Fatalf("execShell: %v", err)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("execShell output = %q, want it to contain the command's stdout", out)
	}
	if !strings.Contains(out, root) {
		t.Errorf("execShell output = %q, want it to contain the workspace %q as cwd", out, root)
	}

	b, err := os.ReadFile(filepath.Join(root, "approval.txt"))
	if err != nil {
		t.Fatalf("command side effect missing: %v", err)
	}
	if strings.TrimSpace(string(b)) != "approved" {
		t.Errorf("approval.txt = %q, want %q", string(b), "approved")
	}

	// The result is the command's output verbatim: no "[exit status N]" suffix
	// and no other decoration.
	want := root + "\ndone\n"
	if out != want {
		t.Errorf("execShell output = %q, want the verbatim stdout %q", out, want)
	}

	// A non-zero exit is still a tool result, and the exit status stays on
	// stderr rather than being appended to what the model sees.
	out, err = execShell(context.Background(), ws, "exit 3")
	if err != nil {
		t.Fatalf("execShell(non-zero): %v", err)
	}
	if out != "" {
		t.Errorf("non-zero exit output = %q, want the verbatim (empty) stdout", out)
	}
}

func TestApprovalRequiredTextIsOneLine(t *testing.T) {
	got := approvalRequired("echo a\necho b")
	if strings.Contains(got, "\n") {
		t.Errorf("approvalRequired produced a multi-line string: %q", got)
	}
	if !strings.Contains(got, "echo a") {
		t.Errorf("approvalRequired lost the command: %q", got)
	}
}

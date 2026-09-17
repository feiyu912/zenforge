package present

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/tool"
	workspacepkg "github.com/feiyu912/zenforge/workspace"
	"github.com/feiyu912/zenforge/workspace/local"
)

func presentSetup(t *testing.T) (tool.Tool, workspacepkg.Workspace) {
	t.Helper()
	root := t.TempDir()
	ws, err := local.New(local.Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("workspace New returned error: %v", err)
	}
	presenter, err := New(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "out"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "out", "report.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return presenter, ws
}

func callPresent(t *testing.T, presenter tool.Tool, args string) tool.Result {
	t.Helper()
	result, _ := presenter.Call(context.Background(), json.RawMessage(args), tool.Context{})
	return result
}

func TestPresentValidatesAndRendersDeliverables(t *testing.T) {
	presenter, _ := presentSetup(t)
	result := callPresent(t, presenter, `{"files":[{"path":"out/report.md","description":"Final report"}]}`)
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("present failed: %+v", result)
	}
	files, ok := result.Structured["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("structured files = %#v", result.Structured["files"])
	}
	entry := files[0].(map[string]any)
	if entry["path"] != "out/report.md" || entry["description"] != "Final report" {
		t.Fatalf("file entry = %#v", entry)
	}
	if !strings.Contains(result.Output, "Presented out/report.md") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestPresentRejectsMissingAndNonRegularFiles(t *testing.T) {
	presenter, _ := presentSetup(t)

	missing := callPresent(t, presenter, `{"files":[{"path":"out/missing.md"}]}`)
	if !strings.Contains(missing.Error, "file not found") ||
		!strings.Contains(missing.Error, "create the file if needed, and retry") {
		t.Fatalf("missing-file error = %q", missing.Error)
	}
	if !strings.Contains(missing.Error, workspacepkg.ErrPathNotFound.Error()) {
		t.Fatalf("missing-file error lost the sentinel: %q", missing.Error)
	}

	directory := callPresent(t, presenter, `{"files":[{"path":"out"}]}`)
	if !strings.Contains(directory.Error, "not a regular file") {
		t.Fatalf("directory error = %q", directory.Error)
	}
}

func TestPresentRejectsEmptyPathAndCountLimits(t *testing.T) {
	presenter, ws := presentSetup(t)
	blank := callPresent(t, presenter, `{"files":[{"path":"   "}]}`)
	if !strings.Contains(blank.Error, tool.ErrInvalidArguments.Error()) {
		t.Fatalf("blank path error = %q", blank.Error)
	}
	none := callPresent(t, presenter, `{"files":[]}`)
	if !strings.Contains(none.Error, "present accepts 1 to 8 files") {
		t.Fatalf("empty list error = %q", none.Error)
	}

	limited, err := New(Config{Workspace: ws, MaxFiles: 1})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	tooMany := callPresent(t, limited, `{"files":[{"path":"out/report.md"},{"path":"out/report.md"}]}`)
	if !strings.Contains(tooMany.Error, "present accepts 1 to 1 files") {
		t.Fatalf("max files error = %q", tooMany.Error)
	}
}

func TestPresentRequiresWorkspace(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted a nil workspace")
	}
}

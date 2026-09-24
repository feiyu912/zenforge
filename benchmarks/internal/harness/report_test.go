package harness

import (
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/benchmarks/tasks"
)

// syntheticReport is the report shape the table tests render: one success, one
// unsupported cell, one failure, and a runner that could not start.
func syntheticReport() *Report {
	return &Report{
		Tasks:   []string{"edit-file", "durable-task"},
		Runners: []string{"zenforge", "deepagents", "langgraph"},
		Cells: []Cell{
			{Task: "edit-file", Runner: "zenforge", Status: "completed", Success: true, Samples: 3, MinWallMillis: 100, MaxWallMillis: 140, WallMillis: 120, ModelMillis: 5, OverheadMillis: 115, Requests: 3, PromptBytes: 5000, ToolSchemaBytes: 3000, Recovery: "n/a"},
			{Task: "edit-file", Runner: "deepagents", Status: "completed", Success: true, Samples: 3, MinWallMillis: 280, MaxWallMillis: 330, WallMillis: 300, ModelMillis: 5, OverheadMillis: 295, Requests: 3, PromptBytes: 9000, ToolSchemaBytes: 4000, Recovery: "n/a"},
			{Task: "durable-task", Runner: "zenforge", Status: "completed", Success: true, Samples: 3, MinWallMillis: 180, MaxWallMillis: 240, WallMillis: 200, ModelMillis: 8, OverheadMillis: 192, Requests: 5, PromptBytes: 11000, ToolSchemaBytes: 5000, Recovery: "paused -> completed"},
			{Task: "durable-task", Runner: "deepagents", Status: "unsupported", Unsupported: true, Samples: 3, Recovery: "not run", Detail: "no durable pause"},
			{Task: "durable-task", Runner: "langgraph", Status: "failed", Samples: 3, Recovery: "paused -> failed", Failures: []string{"approval.txt does not exist"}},
		},
		Unavailable: []Unavailable{{
			Runner:  "eino",
			Display: "eino",
			Detail:  "build runners/eino: missing dependency",
			Install: "(cd runners/eino && go mod download)",
		}},
	}
}

// TestRenderTextKeepsUnsupportedVisible is the contract's rule that a framework
// which cannot do a task is neither dropped nor counted as a success.
func TestRenderTextKeepsUnsupportedVisible(t *testing.T) {
	text := RenderText(syntheticReport())
	if !strings.Contains(text, "unsupported") {
		t.Fatalf("table does not show an unsupported cell:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "durable-task  deepagents") {
			if !strings.Contains(line, "unsupported") {
				t.Fatalf("unsupported cell rendered as %q", line)
			}
			if strings.Contains(line, "success") {
				t.Fatalf("unsupported cell was counted as a success: %q", line)
			}
		}
	}
	if !strings.Contains(text, "TASK") || !strings.Contains(text, "RECOVERY") {
		t.Fatalf("table is missing its header:\n%s", text)
	}
	if !strings.Contains(text, "TASK  RUNNER") && !strings.Contains(text, "TASK ") {
		t.Fatalf("table header is unreadable:\n%s", text)
	}
	if !strings.Contains(text, "N") || !strings.Contains(text, "median") {
		t.Fatalf("table does not publish the sample count:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "edit-file") && strings.Contains(line, "zenforge") {
			if !strings.Contains(line, " 3 ") {
				t.Fatalf("row does not show N=3: %q", line)
			}
		}
	}
	if !strings.Contains(text, "unavailable runners:") || !strings.Contains(text, "(cd runners/eino && go mod download)") {
		t.Fatalf("table does not name the unavailable runner's install command:\n%s", text)
	}
}

// TestRenderTextOrdersTasksThenRunners keeps comparisons readable: a reader
// comparing frameworks reads down one task's rows.
func TestRenderTextOrdersTasksThenRunners(t *testing.T) {
	ordered := orderedCells(syntheticReport())
	want := []string{
		"edit-file/zenforge", "edit-file/deepagents",
		"durable-task/zenforge", "durable-task/deepagents", "durable-task/langgraph",
	}
	if len(ordered) != len(want) {
		t.Fatalf("ordered %d cells, want %d", len(ordered), len(want))
	}
	for index, cell := range ordered {
		if got := cell.Task + "/" + cell.Runner; got != want[index] {
			t.Fatalf("cell %d = %s, want %s", index, got, want[index])
		}
	}
}

// TestRenderMarkdownTable covers the machine-facing second rendering.
func TestRenderMarkdownTable(t *testing.T) {
	markdown := RenderMarkdown(syntheticReport())
	lines := strings.Split(markdown, "\n")
	header := ""
	rows := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "| Task |") {
			header = line
		}
		if strings.HasPrefix(line, "| edit-file |") || strings.HasPrefix(line, "| durable-task |") {
			rows++
		}
	}
	if strings.Count(header, "|") != 11 {
		t.Fatalf("markdown header has %d columns, want 10:\n%s", strings.Count(header, "|")-1, header)
	}
	if rows != 5 {
		t.Fatalf("markdown rows = %d, want 5:\n%s", rows, markdown)
	}
	if !strings.Contains(markdown, "| durable-task | deepagents | unsupported |") {
		t.Fatalf("markdown does not mark the unsupported cell:\n%s", markdown)
	}
	if !strings.Contains(header, "| N |") {
		t.Fatalf("markdown does not publish the sample count:\n%s", header)
	}
}

// TestExitErrorIgnoresUnsupported pins the exit rule: unsupported is a result,
// a failure and an unavailable runner are not.
func TestExitErrorIgnoresUnsupported(t *testing.T) {
	if err := syntheticReport().ExitError(); err == nil {
		t.Fatalf("ExitError() = nil, want the failed cell and unavailable runner named")
	} else {
		message := err.Error()
		if !strings.Contains(message, "durable-task/langgraph failed") {
			t.Fatalf("ExitError() = %q, want the failed cell", message)
		}
		if !strings.Contains(message, "runner eino is unavailable") {
			t.Fatalf("ExitError() = %q, want the unavailable runner", message)
		}
		if strings.Contains(message, "deepagents failed") {
			t.Fatalf("ExitError() = %q, want the unsupported cell ignored", message)
		}
	}

	// A report whose only non-success cells are unsupported exits zero.
	unsupportedOnly := &Report{
		Tasks:   []string{"durable-task"},
		Runners: []string{"deepagents"},
		Cells: []Cell{{
			Task: "durable-task", Runner: "deepagents",
			Status: "unsupported", Unsupported: true, Recovery: "not run",
		}},
	}
	if err := unsupportedOnly.ExitError(); err != nil {
		t.Fatalf("ExitError() = %v, want nil for an unsupported-only report", err)
	}
}

// TestCellStatusPrecedence covers the aggregation rules that decide a row.
func TestCellStatusPrecedence(t *testing.T) {
	for name, testCase := range map[string]struct {
		passes []PassReport
		want   string
	}{
		"all completed":                   {passes: []PassReport{{Status: "completed"}, {Status: "completed"}}, want: "completed"},
		"one unsupported":                 {passes: []PassReport{{Status: "completed"}, {Status: "unsupported"}}, want: "unsupported"},
		"failure outranks unsupported":    {passes: []PassReport{{Status: "failed"}, {Status: "unsupported"}}, want: "failed"},
		"unavailable outranks everything": {passes: []PassReport{{Status: "failed"}, {Status: "unavailable"}}, want: "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cellStatus(testCase.passes); got != testCase.want {
				t.Fatalf("cellStatus = %s, want %s", got, testCase.want)
			}
		})
	}
}

// TestPassStatusRequiresVerification: a run whose processes all exited zero is
// still a failure when the artifact or the call order does not hold.
func TestPassStatusRequiresVerification(t *testing.T) {
	completed := []PhaseReport{{Phase: "run", Status: "completed"}}
	if got := passStatus(completed, true); got != "completed" {
		t.Fatalf("passStatus(completed, true) = %s", got)
	}
	if got := passStatus(completed, false); got != "failed" {
		t.Fatalf("passStatus(completed, false) = %s, want failed", got)
	}
	if got := passStatus([]PhaseReport{{Phase: "run", Status: "unsupported"}}, false); got != "unsupported" {
		t.Fatalf("passStatus(unsupported) = %s, want unsupported", got)
	}
	if got := passStatus([]PhaseReport{{Phase: "run", Status: "failed"}}, false); got != "failed" {
		t.Fatalf("passStatus(failed) = %s, want failed", got)
	}
}

// TestRecoveryText covers the durable task's one-cell description.
func TestRecoveryText(t *testing.T) {
	durable := Cell{Passes: []PassReport{{Phases: []PhaseReport{
		{Phase: "run", Status: "paused"},
		{Phase: "resume", Status: "completed"},
	}}}}
	metadata, ok := tasks.Lookup("durable-task")
	if !ok {
		t.Fatalf("durable-task metadata not found")
	}
	if got := recoveryText(metadata, durable.Passes); got != "paused -> completed" {
		t.Fatalf("recoveryText = %q", got)
	}
	edit, _ := tasks.Lookup("edit-file")
	if got := recoveryText(edit, durable.Passes); got != "n/a" {
		t.Fatalf("recoveryText(edit-file) = %q, want n/a", got)
	}
	if got := recoveryText(metadata, []PassReport{{Phases: []PhaseReport{{Phase: "run", Status: "unsupported"}}}}); got != "unsupported" {
		t.Fatalf("recoveryText(unsupported) = %q", got)
	}
}

// TestStderrLinesBoundsOutput keeps a failed run's diagnostics readable.
func TestStderrLinesBoundsOutput(t *testing.T) {
	if got := stderrLines("one\ntwo\nthree\n", 10); len(got) != 3 {
		t.Fatalf("stderrLines = %v, want three lines", got)
	}
	got := stderrLines("one\ntwo\nthree\n", 2)
	if len(got) != 3 || got[2] != "..." {
		t.Fatalf("stderrLines limit = %v, want a truncation marker", got)
	}
}

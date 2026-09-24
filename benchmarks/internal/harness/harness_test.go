package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/benchmarks/tasks"
)

// TestHarnessHelperProcess is not a test. It is the runner the orchestration
// tests start: the test binary re-invoked as a subprocess, so a cell's whole
// path -- two fresh directories, one or two processes, status mapping, and the
// verification decision -- can be exercised without building a real framework.
func TestHarnessHelperProcess(t *testing.T) {
	mode := os.Getenv("BENCH_TEST_HELPER")
	if mode == "" {
		return
	}
	status, code := mode, 0
	switch mode {
	case "completed":
		status, code = "completed", 0
	case "failed":
		status, code = "failed", 1
	case "unsupported":
		status, code = "unsupported", 78
	case "paused":
		status, code = "paused", 75
	}
	payload, err := json.Marshal(map[string]any{
		"task":      os.Getenv("BENCH_TASK"),
		"phase":     os.Getenv("BENCH_PHASE"),
		"status":    status,
		"detail":    "helper " + mode,
		"framework": "helper",
	})
	if err == nil {
		_ = os.WriteFile(os.Getenv("BENCH_RESULT"), payload, 0o644)
	}
	os.Exit(code)
}

func helperRegistration() runnerRegistration {
	return runnerRegistration{
		id:      "helper",
		display: "helper",
		install: "install the helper",
		prepare: func(context.Context, *buildEnvironment) (preparedRunner, error) {
			return preparedRunner{}, nil
		},
	}
}

func helperPrepared(mode string) preparedRunner {
	return preparedRunner{
		command:  []string{os.Args[0], "-test.run=TestHarnessHelperProcess", "--"},
		extraEnv: []string{"BENCH_TEST_HELPER=" + mode},
	}
}

// TestRunCellKeepsUnsupportedVisibleAndUnjudged is the report rule at the cell
// level: a framework that says it cannot do the task is recorded as
// unsupported, is not a success, and is not judged against an artifact it never
// claimed to produce.
func TestRunCellKeepsUnsupportedVisibleAndUnjudged(t *testing.T) {
	cell := runCell(context.Background(), tasks.DurableTask, helperRegistration(), helperPrepared("unsupported"), t.TempDir(), 1)
	if cell.Status != "unsupported" || cell.Unsupported != true || cell.Success {
		t.Fatalf("cell = %+v, want an unsupported non-success", cell)
	}
	if len(cell.Failures) != 0 {
		t.Fatalf("unsupported cell carries failures: %v", cell.Failures)
	}
	if cell.Recovery != "unsupported" {
		t.Fatalf("recovery = %q, want unsupported", cell.Recovery)
	}
	if len(cell.Passes) != 1 || cell.Passes[0].Status != "unsupported" {
		t.Fatalf("passes = %+v, want one unsupported pass", cell.Passes)
	}
	report := &Report{Tasks: []string{"durable-task"}, Runners: []string{"helper"}, Cells: []Cell{cell}}
	if err := report.ExitError(); err != nil {
		t.Fatalf("ExitError() = %v, want nil for an unsupported cell", err)
	}
}

// TestRunCellFailsWhenThePhaseFails: a failed process is judged, and the
// verifier's reasons are kept so the report says why.
func TestRunCellFailsWhenThePhaseFails(t *testing.T) {
	cell := runCell(context.Background(), tasks.DurableTask, helperRegistration(), helperPrepared("failed"), t.TempDir(), 1)
	if cell.Status != "failed" || cell.Success {
		t.Fatalf("cell = %+v, want a failed cell", cell)
	}
	if len(cell.Failures) == 0 {
		t.Fatalf("failed cell names no failure")
	}
	if len(cell.Passes) != 1 || cell.Passes[0].Phases[0].ExitCode != 1 {
		t.Fatalf("passes = %+v, want the exit code recorded", cell.Passes)
	}
	report := &Report{Tasks: []string{"durable-task"}, Runners: []string{"helper"}, Cells: []Cell{cell}}
	if err := report.ExitError(); err == nil || !strings.Contains(err.Error(), "durable-task/helper failed") {
		t.Fatalf("ExitError() = %v, want the failed cell named", err)
	}
}

// TestRunCellFailsWhenADurableRunDoesNotPause: exiting zero is not enough. A
// recovery task whose first process never paused has nothing to resume, and the
// verifier is what says so.
func TestRunCellFailsWhenADurableRunDoesNotPause(t *testing.T) {
	cell := runCell(context.Background(), tasks.DurableTask, helperRegistration(), helperPrepared("completed"), t.TempDir(), 1)
	if cell.Status != "failed" {
		t.Fatalf("cell status = %s, want failed: %v", cell.Status, cell.Failures)
	}
	if len(cell.Passes[0].Phases) != 1 {
		t.Fatalf("phases = %+v, want the resume phase skipped when nothing paused", cell.Passes[0].Phases)
	}
	found := false
	for _, failure := range cell.Failures {
		if strings.Contains(failure, "want paused") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures = %v, want the missing pause named", cell.Failures)
	}
}

// TestRunCellKeepsAMissingRunnerBinaryVisible covers the unavailable path at
// the cell level: it is not judged, it keeps its exit code unset, and the
// registration's install command is what a reader is given.
func TestRunCellKeepsAMissingRunnerBinaryVisible(t *testing.T) {
	prepared := preparedRunner{command: []string{"harness-helper-definitely-not-installed"}}
	cell := runCell(context.Background(), tasks.EditFile, helperRegistration(), prepared, t.TempDir(), 1)
	if cell.Status != "unavailable" || cell.Success {
		t.Fatalf("cell = %+v, want unavailable", cell)
	}
	if len(cell.Failures) != 0 {
		t.Fatalf("unavailable cell carries failures: %v", cell.Failures)
	}
	if cell.Recovery != "n/a" {
		t.Fatalf("recovery = %q, want n/a for a task with one phase", cell.Recovery)
	}
}

// TestRunPassCreatesFreshDirectories proves each pass works in its own
// workspace: the task's seed files appear there and nowhere shared.
func TestRunPassCreatesFreshDirectories(t *testing.T) {
	tempRoot := t.TempDir()
	cell := runCell(context.Background(), tasks.EditFile, helperRegistration(), helperPrepared("failed"), tempRoot, 1)
	if len(cell.Passes) != 1 {
		t.Fatalf("passes = %d, want 1", len(cell.Passes))
	}
	seeded := filepath.Join(tempRoot, "helper", "edit-file", "repeat-1", "approve", "workspace", "input.txt")
	if _, err := os.Stat(seeded); err != nil {
		t.Fatalf("seeded workspace file missing: %v", err)
	}
}

// TestProcessStatusPrecedence pins the rule that decides whether verification
// runs at all.
func TestProcessStatusPrecedence(t *testing.T) {
	for name, testCase := range map[string]struct {
		phases []PhaseReport
		want   string
	}{
		"completed":                {phases: []PhaseReport{{Status: "completed"}}, want: "completed"},
		"unsupported":              {phases: []PhaseReport{{Status: "unsupported"}}, want: "unsupported"},
		"failed beats unsupported": {phases: []PhaseReport{{Status: "unsupported"}, {Status: "failed"}}, want: "failed"},
		"unavailable wins":         {phases: []PhaseReport{{Status: "failed"}, {Status: "unavailable"}}, want: "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(processStatus(testCase.phases)); got != testCase.want {
				t.Fatalf("processStatus = %s, want %s", got, testCase.want)
			}
		})
	}
}

// TestRepeatRunsTheWholeCellAgain: a repeat is a whole task run in its own
// directories, and the median is what the cell publishes.
func TestRepeatRunsTheWholeCellAgain(t *testing.T) {
	cell := runCell(context.Background(), tasks.EditFile, helperRegistration(), helperPrepared("failed"), t.TempDir(), 3)
	if cell.Samples != 3 || len(cell.Repeats) != 3 {
		t.Fatalf("samples = %d, repeats = %d, want 3 each", cell.Samples, len(cell.Repeats))
	}
	for index, sample := range cell.Repeats {
		if sample.Repeat != index {
			t.Fatalf("sample %d carries repeat %d", index, sample.Repeat)
		}
		if sample.WallMillis < cell.MinWallMillis || sample.WallMillis > cell.MaxWallMillis {
			t.Fatalf("sample %d wall %d is outside [%d, %d]", index, sample.WallMillis, cell.MinWallMillis, cell.MaxWallMillis)
		}
	}
	if cell.WallMillis < cell.MinWallMillis || cell.WallMillis > cell.MaxWallMillis {
		t.Fatalf("median wall %d is outside [%d, %d]", cell.WallMillis, cell.MinWallMillis, cell.MaxWallMillis)
	}
	repeats := map[int]bool{}
	for _, pass := range cell.Passes {
		repeats[pass.Repeat] = true
	}
	if len(repeats) != 3 {
		t.Fatalf("passes cover repeats %v, want three repeats", repeats)
	}
	// A failure in a repeated cell says which repeat it came from.
	joined := strings.Join(cell.Failures, "\n")
	if !strings.Contains(joined, "[repeat 2]") {
		t.Fatalf("failures are not tagged with their repeat:\n%s", joined)
	}
}

// TestMedianAndExtremes covers the statistic the latency column is.
func TestMedianAndExtremes(t *testing.T) {
	for name, testCase := range map[string]struct {
		values     []int64
		wantMedian int64
		wantLow    int64
		wantHigh   int64
	}{
		"one sample": {values: []int64{7}, wantMedian: 7, wantLow: 7, wantHigh: 7},
		"odd count":  {values: []int64{30, 10, 20}, wantMedian: 20, wantLow: 10, wantHigh: 30},
		"even count": {values: []int64{10, 20, 30, 44}, wantMedian: 25, wantLow: 10, wantHigh: 44},
		"empty":      {values: nil, wantMedian: 0, wantLow: 0, wantHigh: 0},
	} {
		t.Run(name, func(t *testing.T) {
			low, high := extremes(testCase.values)
			if got := medianInt64(testCase.values); got != testCase.wantMedian {
				t.Fatalf("medianInt64 = %d, want %d", got, testCase.wantMedian)
			}
			if low != testCase.wantLow || high != testCase.wantHigh {
				t.Fatalf("extremes = (%d, %d), want (%d, %d)", low, high, testCase.wantLow, testCase.wantHigh)
			}
		})
	}
	// The samples keep their order: the report prints them as they were run.
	values := []int64{3, 1, 2}
	_ = medianInt64(values)
	if values[0] != 3 || values[1] != 1 || values[2] != 2 {
		t.Fatalf("medianInt64 reordered its input: %v", values)
	}
}

// TestDeterminismFailures is the rule the parent will quote: the frozen script
// fixes the cost metrics, so a repeat that changes one is a finding.
func TestDeterminismFailures(t *testing.T) {
	stable := []RepeatSample{
		{Repeat: 0, Requests: 3, PromptBytes: 5000, ToolSchemaBytes: 3000},
		{Repeat: 1, Requests: 3, PromptBytes: 5000, ToolSchemaBytes: 3000},
		{Repeat: 2, Requests: 3, PromptBytes: 5000, ToolSchemaBytes: 3000},
	}
	if got := checkDeterminism(tasks.EditFile, stable); got != "" {
		t.Fatalf("checkDeterminism(stable) = %q, want no finding", got)
	}
	if got := checkDeterminism(tasks.EditFile, stable[:1]); got != "" {
		t.Fatalf("a single repeat cannot be nondeterministic, got %q", got)
	}

	flaky := append([]RepeatSample(nil), stable...)
	flaky[2].Requests = 4
	finding := checkDeterminism(tasks.EditFile, flaky)
	if !strings.Contains(finding, "repeat 3 of edit-file made 4 model request(s); repeat 1 made 3") {
		t.Fatalf("finding = %q, want it to name the task, the repeats and both counts", finding)
	}
	flaky = append([]RepeatSample(nil), stable...)
	flaky[1].PromptBytes = 5001
	if finding := checkDeterminism(tasks.DurableTask, flaky); !strings.Contains(finding, "prompt bytes") {
		t.Fatalf("finding = %q, want it to name the varying metric", finding)
	}
	flaky = append([]RepeatSample(nil), stable...)
	flaky[1].ToolSchemaBytes = 2999
	if finding := checkDeterminism(tasks.DurableTask, flaky); !strings.Contains(finding, "tool-schema bytes") {
		t.Fatalf("finding = %q, want it to name the varying metric", finding)
	}
}

// TestRepeatSampleSumsApprovalPasses: approve-command has two passes, so one
// repeat's sample is both of them together.
func TestRepeatSampleSumsApprovalPasses(t *testing.T) {
	passes := []PassReport{
		{Repeat: 0, Approval: "approve", Requests: 4, PromptBytes: 7000, ToolSchemaBytes: 4000, WallMillis: 100, ModelMillis: 10, Success: true, Status: "completed"},
		{Repeat: 0, Approval: "reject", Requests: 4, PromptBytes: 7000, ToolSchemaBytes: 4000, WallMillis: 200, ModelMillis: 20, Success: true, Status: "completed"},
		{Repeat: 1, Approval: "approve", Requests: 4, PromptBytes: 7000, ToolSchemaBytes: 4000, WallMillis: 110, ModelMillis: 10, Success: true, Status: "completed"},
		{Repeat: 1, Approval: "reject", Requests: 4, PromptBytes: 7000, ToolSchemaBytes: 4000, WallMillis: 210, ModelMillis: 20, Success: true, Status: "completed"},
	}
	samples := repeatSamples(passes, 2)
	if len(samples) != 2 {
		t.Fatalf("samples = %+v, want 2", samples)
	}
	if samples[0].Requests != 8 || samples[0].WallMillis != 300 || samples[0].ModelMillis != 30 {
		t.Fatalf("sample 0 = %+v, want both passes summed", samples[0])
	}
	if samples[0].OverheadMillis != 270 || !samples[0].Success {
		t.Fatalf("sample 0 = %+v", samples[0])
	}
}

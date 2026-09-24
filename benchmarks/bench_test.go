// Package benchmarks_test holds the benchmark's end-to-end test: it runs the
// ZenForge runner through the harness's own code path, exactly as
// `cmd/bench` does, and judges the report.
//
// It is here, at the module root, rather than beside the harness, because it is
// the module's integration point: everything the pieces claim individually --
// the scripted endpoint's turn selection, the subprocess protocol, the runner,
// the verifier, the report -- has to hold together for this test to pass. It
// needs nothing but Go: the runner is built from this module and the model is
// the loopback scripted endpoint.
package benchmarks_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/benchmarks/internal/harness"
	"github.com/feiyu912/zenforge/benchmarks/internal/runner"
)

// TestEditFileEndToEndThroughTheHarness is the real run: build the ZenForge
// runner, point it at the scripted endpoint, let it read and write, and check
// both the artifact and the endpoint's recorded call order.
func TestEditFileEndToEndThroughTheHarness(t *testing.T) {
	var out strings.Builder
	report, err := harness.Run(context.Background(), harness.Options{
		TaskSpec:   "edit-file",
		RunnerSpec: "zenforge",
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("harness.Run: %v", err)
	}
	if len(report.Unavailable) != 0 {
		t.Fatalf("unavailable runners = %+v", report.Unavailable)
	}
	if len(report.Cells) != 1 {
		t.Fatalf("cells = %d, want 1: %+v", len(report.Cells), report.Cells)
	}
	cell := report.Cells[0]
	if cell.Status != string(runner.StatusCompleted) || !cell.Success {
		t.Fatalf("cell = %+v, want a completed success: %v", cell, cell.Failures)
	}
	if cell.Requests < 3 {
		t.Fatalf("requests = %d, want at least the script's three turns", cell.Requests)
	}
	if cell.PromptBytes <= 0 || cell.ToolSchemaBytes <= 0 || cell.ToolSchemaBytes >= cell.PromptBytes {
		t.Fatalf("cost metrics = prompt %d, tools %d; want a nonzero strict subset",
			cell.PromptBytes, cell.ToolSchemaBytes)
	}
	if cell.Recovery != "n/a" {
		t.Fatalf("recovery = %q, want n/a for a task with one phase", cell.Recovery)
	}
	if cell.OverheadMillis < 0 {
		t.Fatalf("overhead = %d, want >= 0", cell.OverheadMillis)
	}

	// The verifier must have judged the artifact and the call order, not just
	// the exit code: an end-to-end test that only saw "completed" would pass
	// even if the write never happened.
	if len(cell.Passes) != 1 {
		t.Fatalf("passes = %d, want 1", len(cell.Passes))
	}
	checks := strings.Join(cell.Passes[0].Checks, "\n")
	if !strings.Contains(checks, "out.txt holds the expected") {
		t.Fatalf("verifier checks do not include the artifact:\n%s", checks)
	}
	if !strings.Contains(checks, "reached the model before write_file was issued") {
		t.Fatalf("verifier checks do not include the call order:\n%s", checks)
	}
	if len(cell.Passes[0].Phases) != 1 || cell.Passes[0].Phases[0].Phase != "run" {
		t.Fatalf("phases = %+v, want a single run phase", cell.Passes[0].Phases)
	}

	if err := report.ExitError(); err != nil {
		t.Fatalf("ExitError() = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "run edit-file/zenforge: success") {
		t.Fatalf("per-run line missing:\n%s", out.String())
	}
	table := harness.RenderText(report)
	if !strings.Contains(table, "edit-file") || !strings.Contains(table, "success") {
		t.Fatalf("table missing the run:\n%s", table)
	}
	markdown := harness.RenderMarkdown(report)
	if !strings.Contains(markdown, "| edit-file | zenforge | success |") {
		t.Fatalf("markdown table missing the run:\n%s", markdown)
	}

	// The -json artifact is the report round-tripped; it must decode back into
	// the same shapes a consumer reads.
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var decoded harness.Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(decoded.Cells) != 1 || decoded.Cells[0].Status != cell.Status {
		t.Fatalf("round-tripped report = %+v", decoded.Cells)
	}
}

// TestDurableTaskResumesInASecondProcess is the recovery metric's end-to-end
// proof: the first ZenForge process must stop with exit 75 and leave a
// checkpoint, and a second, fresh process must load it and finish the artifact
// without losing the steps recorded before the pause.
func TestDurableTaskResumesInASecondProcess(t *testing.T) {
	var out strings.Builder
	report, err := harness.Run(context.Background(), harness.Options{
		TaskSpec:   "durable-task",
		RunnerSpec: "zenforge",
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("harness.Run: %v", err)
	}
	if len(report.Cells) != 1 {
		t.Fatalf("cells = %d, want 1", len(report.Cells))
	}
	cell := report.Cells[0]
	if cell.Status != string(runner.StatusCompleted) || !cell.Success {
		t.Fatalf("cell = %+v, want a completed success: %v", cell, cell.Failures)
	}
	if cell.Recovery != "paused -> completed" {
		t.Fatalf("recovery = %q, want paused -> completed", cell.Recovery)
	}
	phases := cell.Passes[0].Phases
	if len(phases) != 2 {
		t.Fatalf("phases = %+v, want a run and a resume process", phases)
	}
	if phases[0].Status != string(runner.StatusPaused) || phases[0].ExitCode != 75 {
		t.Fatalf("run phase = %+v, want paused with exit 75", phases[0])
	}
	if phases[1].Status != string(runner.StatusCompleted) || phases[1].ExitCode != 0 {
		t.Fatalf("resume phase = %+v, want completed with exit 0", phases[1])
	}
	if cell.Requests < 5 {
		t.Fatalf("requests = %d, want at least the script's five turns", cell.Requests)
	}
	checks := strings.Join(cell.Passes[0].Checks, "\n")
	for _, want := range []string{
		"steps.txt holds the expected",
		"out.txt holds the expected",
		"milestone.txt holds the expected",
		"durable state file(s) before the resume",
		`the approved run_shell result (stdout "milestone") reached the model`,
	} {
		if !strings.Contains(checks, want) {
			t.Fatalf("checks do not include %q:\n%s", want, checks)
		}
	}
	if err := report.ExitError(); err != nil {
		t.Fatalf("ExitError() = %v, want nil", err)
	}
}

// TestRepeatPublishesSamplesAndMedians runs one cell three times through the
// same code path `-repeat 3` uses and checks that the samples are real. The
// point of the flag is that no published latency is a single sample, so the
// test insists on the samples themselves, not just on the median.
func TestRepeatPublishesSamplesAndMedians(t *testing.T) {
	report, err := harness.Run(context.Background(), harness.Options{
		TaskSpec:   "edit-file",
		RunnerSpec: "zenforge",
		Repeat:     3,
	})
	if err != nil {
		t.Fatalf("harness.Run: %v", err)
	}
	cell := report.Cells[0]
	if cell.Status != string(runner.StatusCompleted) || !cell.Success {
		t.Fatalf("cell = %+v, want a completed success: %v", cell, cell.Failures)
	}
	if cell.Samples != 3 || len(cell.Repeats) != 3 {
		t.Fatalf("samples = %d, repeats = %d, want 3 each", cell.Samples, len(cell.Repeats))
	}
	if cell.WallMillis < cell.MinWallMillis || cell.WallMillis > cell.MaxWallMillis {
		t.Fatalf("median wall %d is outside [%d, %d]", cell.WallMillis, cell.MinWallMillis, cell.MaxWallMillis)
	}
	for index, sample := range cell.Repeats {
		if sample.WallMillis <= 0 {
			t.Fatalf("sample %d has no wall clock", index)
		}
		// The frozen script's cost is the same every time; the harness turns a
		// difference into a failure, so a successful cell here is itself the
		// determinism assertion.
		if sample.Requests != cell.Requests || sample.PromptBytes != cell.PromptBytes ||
			sample.ToolSchemaBytes != cell.ToolSchemaBytes {
			t.Fatalf("sample %d = %+v differs from the cell's cost metrics (%d requests, %d/%d bytes)",
				index, sample, cell.Requests, cell.PromptBytes, cell.ToolSchemaBytes)
		}
	}
	markdown := harness.RenderMarkdown(report)
	if !strings.Contains(markdown, "| N |") {
		t.Fatalf("markdown does not publish the sample count:\n%s", markdown)
	}
}

// TestLiveModeIsRefusedClearly: live mode is documented but not part of this
// chain, and the refusal has to say so rather than silently running the
// scripted endpoint under a live flag.
func TestLiveModeIsRefusedClearly(t *testing.T) {
	_, err := harness.Run(context.Background(), harness.Options{
		TaskSpec:   "edit-file",
		RunnerSpec: "zenforge",
		LiveModel:  "openai/gpt-4o-mini",
	})
	if err == nil {
		t.Fatalf("Run with -live-model = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "not implemented in this chain") {
		t.Fatalf("refusal = %q, want it to name the missing implementation", err.Error())
	}
}

// TestUnknownSelectionsAreRefused keeps a typo from silently shrinking the
// comparison.
func TestUnknownSelectionsAreRefused(t *testing.T) {
	for name, opts := range map[string]harness.Options{
		"task":   {TaskSpec: "no-such-task", RunnerSpec: "zenforge"},
		"runner": {TaskSpec: "edit-file", RunnerSpec: "no-such-runner"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := harness.Run(context.Background(), opts); err == nil {
				t.Fatalf("Run(%+v) = nil error, want a refusal", opts)
			}
		})
	}
}

// TestRepeatBoundsRefuseAnUnboundedRun: a repeat multiplies every task by every
// runner, so an absurd count is refused before anything is built.
func TestRepeatBoundsRefuseAnUnboundedRun(t *testing.T) {
	for name, repeat := range map[string]int{"negative": -1, "absurd": 1000} {
		t.Run(name, func(t *testing.T) {
			_, err := harness.Run(context.Background(), harness.Options{
				TaskSpec:   "edit-file",
				RunnerSpec: "zenforge",
				Repeat:     repeat,
			})
			if err == nil {
				t.Fatalf("Run with -repeat %d = nil error, want a refusal", repeat)
			}
			if !strings.Contains(err.Error(), "-repeat") {
				t.Fatalf("refusal = %q, want it to name the flag", err.Error())
			}
		})
	}
}

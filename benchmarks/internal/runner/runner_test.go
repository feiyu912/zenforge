package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunnerHelperProcess is not a test. It is the process the other tests
// start: the test binary re-invoked with -test.run, so a runner outcome can be
// produced without depending on an external script, an interpreter, or the
// host's shell.
func TestRunnerHelperProcess(t *testing.T) {
	mode := os.Getenv("BENCH_TEST_HELPER")
	if mode == "" {
		return
	}
	resultPath := os.Getenv("BENCH_RESULT")
	write := func(task, phase, status string, code int) {
		payload := Result{Task: task, Phase: phase, Status: Status(status), Detail: "helper " + mode, Framework: "helper"}
		data, err := json.Marshal(payload)
		if err != nil {
			os.Exit(90)
		}
		if resultPath != "" {
			_ = os.WriteFile(resultPath, data, 0o644)
		}
		os.Exit(code)
	}
	task, phase := os.Getenv("BENCH_TASK"), os.Getenv("BENCH_PHASE")
	switch mode {
	case "ok":
		write(task, phase, "completed", 0)
	case "paused":
		write(task, phase, "paused", 75)
	case "unsupported":
		write(task, phase, "unsupported", 78)
	case "failed":
		write(task, phase, "failed", 1)
	case "missing-result":
		os.Exit(0)
	case "bad-json":
		_ = os.WriteFile(resultPath, []byte("{not json"), 0o644)
		os.Exit(0)
	case "status-mismatch":
		write(task, phase, "completed", 75)
	case "wrong-task":
		write("some-other-task", phase, "completed", 0)
	case "unknown-status":
		write(task, phase, "banana", 0)
	case "noisy":
		_, _ = os.Stderr.WriteString("diagnostic from the helper\n")
		write(task, phase, "completed", 0)
	default:
		os.Exit(91)
	}
}

func helperCommand() []string {
	return []string{os.Args[0], "-test.run=TestRunnerHelperProcess", "--"}
}

func helperSpec(t *testing.T, mode string) Spec {
	t.Helper()
	dir := t.TempDir()
	return Spec{
		RunnerID:   "helper",
		Task:       "edit-file",
		Phase:      "run",
		Command:    helperCommand(),
		Dir:        dir,
		BaseURL:    "http://127.0.0.1:1/v1",
		APIKey:     "key",
		Model:      "scripted-model",
		Workspace:  filepath.Join(dir, "workspace"),
		StateDir:   filepath.Join(dir, "state"),
		Approval:   "approve",
		ResultPath: filepath.Join(dir, "result.json"),
		Install:    "install the helper",
		ExtraEnv:   []string{"BENCH_TEST_HELPER=" + mode},
	}
}

// TestStatusForExitCode is the contract's mapping, including the codes it does
// not name and the signal case the OS reports as -1.
func TestStatusForExitCode(t *testing.T) {
	for code, want := range map[int]Status{
		0:  StatusCompleted,
		75: StatusPaused,
		78: StatusUnsupported,
		1:  StatusFailed,
		2:  StatusFailed,
		70: StatusFailed,
		-1: StatusFailed,
	} {
		if got := StatusForExitCode(code); got != want {
			t.Fatalf("StatusForExitCode(%d) = %s, want %s", code, got, want)
		}
	}
}

// TestRunMapsHelperOutcomes walks every exit code and every way a result file
// can be wrong.
func TestRunMapsHelperOutcomes(t *testing.T) {
	for _, testCase := range []struct {
		mode       string
		wantStatus Status
		wantDetail string
	}{
		{mode: "ok", wantStatus: StatusCompleted},
		{mode: "paused", wantStatus: StatusPaused},
		{mode: "unsupported", wantStatus: StatusUnsupported},
		{mode: "failed", wantStatus: StatusFailed},
		{mode: "missing-result", wantStatus: StatusFailed, wantDetail: "BENCH_RESULT was not written"},
		{mode: "bad-json", wantStatus: StatusFailed, wantDetail: "not valid result JSON"},
		{mode: "status-mismatch", wantStatus: StatusFailed, wantDetail: "but the result reports"},
		{mode: "wrong-task", wantStatus: StatusFailed, wantDetail: "result names task"},
		{mode: "unknown-status", wantStatus: StatusFailed, wantDetail: "unknown status"},
	} {
		t.Run(testCase.mode, func(t *testing.T) {
			outcome := Run(context.Background(), helperSpec(t, testCase.mode))
			if outcome.Status != testCase.wantStatus {
				t.Fatalf("status = %s, want %s (detail %q)", outcome.Status, testCase.wantStatus, outcome.Detail)
			}
			if testCase.wantDetail != "" && !strings.Contains(outcome.Detail, testCase.wantDetail) {
				t.Fatalf("detail = %q, want it to mention %q", outcome.Detail, testCase.wantDetail)
			}
			if outcome.WallClock <= 0 {
				t.Fatalf("WallClock = %v, want > 0", outcome.WallClock)
			}
			if testCase.wantStatus == StatusCompleted {
				if outcome.Framework != "helper" {
					t.Fatalf("Framework = %q, want helper", outcome.Framework)
				}
				if outcome.Result == nil || outcome.Result.Task != "edit-file" {
					t.Fatalf("Result = %+v, want the helper's result", outcome.Result)
				}
			}
		})
	}
}

// TestRunTreatsAMissingBinaryAsUnavailable is what keeps a missing dependency
// from looking like a framework failure.
func TestRunTreatsAMissingBinaryAsUnavailable(t *testing.T) {
	spec := helperSpec(t, "ok")
	spec.Command = []string{"zenforge-benchmark-definitely-not-installed"}
	outcome := Run(context.Background(), spec)
	if outcome.Status != StatusUnavailable {
		t.Fatalf("status = %s, want unavailable", outcome.Status)
	}
	if outcome.Install != "install the helper" {
		t.Fatalf("Install = %q, want the registration's install command", outcome.Install)
	}
	if !strings.Contains(outcome.Detail, "not installed") {
		t.Fatalf("detail = %q, want it to name the missing executable", outcome.Detail)
	}

	empty := helperSpec(t, "ok")
	empty.Command = nil
	if got := Run(context.Background(), empty); got.Status != StatusUnavailable {
		t.Fatalf("empty command status = %s, want unavailable", got.Status)
	}
}

// TestRunRemovesAStaleResult proves an earlier phase's file cannot be read as
// this phase's answer.
func TestRunRemovesAStaleResult(t *testing.T) {
	spec := helperSpec(t, "missing-result")
	if err := os.WriteFile(spec.ResultPath, []byte(`{"task":"edit-file","phase":"run","status":"completed"}`), 0o644); err != nil {
		t.Fatalf("seed stale result: %v", err)
	}
	outcome := Run(context.Background(), spec)
	if outcome.Status != StatusFailed {
		t.Fatalf("status = %s, want failed: a stale result was accepted", outcome.Status)
	}
}

// TestRunCapturesStderrAndBoundsIt covers diagnostics, which are what a reader
// needs when a runner fails.
func TestRunCapturesStderrAndBoundsIt(t *testing.T) {
	outcome := Run(context.Background(), helperSpec(t, "noisy"))
	if !strings.Contains(outcome.Stderr, "diagnostic from the helper") {
		t.Fatalf("Stderr = %q, want the helper's diagnostic", outcome.Stderr)
	}

	bounded := helperSpec(t, "noisy")
	bounded.StderrLimit = 4
	outcome = Run(context.Background(), bounded)
	if !strings.Contains(outcome.Stderr, "[stderr truncated]") {
		t.Fatalf("Stderr = %q, want a truncation marker", outcome.Stderr)
	}
}

// TestEnvironmentIsTheDocumentedProtocol pins the variables a runner is given.
func TestEnvironmentIsTheDocumentedProtocol(t *testing.T) {
	spec := helperSpec(t, "ok")
	environment := strings.Join(spec.Environment(), "\n")
	for _, want := range []string{
		"BENCH_BASE_URL=", "BENCH_API_KEY=", "BENCH_MODEL=", "BENCH_TASK=",
		"BENCH_QUERY=", "BENCH_WORKSPACE=", "BENCH_STATE_DIR=", "BENCH_PHASE=",
		"BENCH_APPROVAL=", "BENCH_RESULT=", "BENCH_TEST_HELPER=ok",
	} {
		if !strings.Contains(environment, want) {
			t.Fatalf("environment is missing %s:\n%s", want, environment)
		}
	}
	if strings.Contains(environment, "BENCH_REQUIRE_PAUSE") {
		t.Fatalf("BENCH_REQUIRE_PAUSE is set when the harness does not need a pause")
	}
	spec.RequirePause = true
	if !strings.Contains(strings.Join(spec.Environment(), "\n"), "BENCH_REQUIRE_PAUSE=1") {
		t.Fatalf("BENCH_REQUIRE_PAUSE is not set when the harness needs a pause")
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/schedule"
)

func runScheduleCommand(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	ioStreams := IO{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}
	code := Main(context.Background(), append([]string{"schedule"}, args...), ioStreams)
	return code, stdout.String(), stderr.String()
}

func TestScheduleCommandAddsListsAndRemoves(t *testing.T) {
	path := t.TempDir() + "/schedules.json"
	code, stdout, stderr := runScheduleCommand(t, "add", "--schedule-file", path, "--spec", "every 1h", "--task", "review the repo")
	if code != 0 {
		t.Fatalf("add exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "added schedule sched_") || !strings.Contains(stdout, "every 1h") {
		t.Fatalf("add stdout = %q", stdout)
	}
	// The schedule outlives the process that added it: a second invocation
	// reads the same file and sees it.
	code, stdout, stderr = runScheduleCommand(t, "list", "--schedule-file", path)
	if code != 0 || !strings.Contains(stdout, "next ") || !strings.Contains(stdout, "never ran") {
		t.Fatalf("list exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}
	file, err := schedule.Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	entries := file.List()
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}

	code, stdout, _ = runScheduleCommand(t, "remove", entries[0].ID, "--schedule-file", path)
	if code != 0 || !strings.Contains(stdout, "removed schedule "+entries[0].ID) {
		t.Fatalf("remove exit = %d stdout = %q", code, stdout)
	}
	code, stdout, _ = runScheduleCommand(t, "list", "--schedule-file", path)
	if code != 0 || !strings.Contains(stdout, "no schedules found") {
		t.Fatalf("list after remove exit = %d stdout = %q", code, stdout)
	}
}

func TestScheduleCommandRejectsBadUsage(t *testing.T) {
	path := t.TempDir() + "/schedules.json"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown verb", []string{"every"}, "unknown schedule verb"},
		{"no verb", nil, "schedule needs a verb"},
		{"bad spec", []string{"add", "--schedule-file", path, "--spec", "whenever", "--task", "x"}, "schedule"},
		{"no task", []string{"add", "--schedule-file", path, "--spec", "every 1h"}, "a schedule needs a task"},
		{"add with positional", []string{"add", "--schedule-file", path, "--spec", "every 1h", "--task", "x", "extra"}, "takes no arguments"},
		{"remove without id", []string{"remove", "--schedule-file", path}, "needs an id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, _, stderr := runScheduleCommand(t, test.args...)
			if code != exitInvalidUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, exitInvalidUsage, stderr)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("stderr = %q, want %q", stderr, test.want)
			}
		})
	}
}

func TestScheduleRunDueRunsAnOverdueScheduleAndReportsWhatItSkipped(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	model := newOpenAISSEStub(t, textChunk("scheduled answer"))
	workspace := t.TempDir()
	checkpointDir := t.TempDir()
	path := checkpointDir + "/schedules.json"
	file, err := schedule.Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	// Added three hours ago and never fired: the process that owns it was
	// gone, which is exactly what a durable schedule has to survive.
	if _, err := file.Add(schedule.Entry{ID: "sched_x", Spec: "every 1h", Task: "say hello"}, time.Now().Add(-3*time.Hour)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	// A dry run reports the backlog without running anything.
	code, stdout, stderr := runScheduleCommand(t, "run-due", "--schedule-file", path, "--dry-run")
	if code != 0 || !strings.Contains(stdout, "due: sched_x") {
		t.Fatalf("dry run exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}
	if after, _ := schedule.Open(path); len(after.Due(time.Now())) != 1 {
		t.Fatal("a dry run consumed the schedule")
	}

	code, stdout, stderr = runScheduleCommand(t, "run-due", "--schedule-file", path,
		"--workspace", workspace, "--checkpoint-dir", checkpointDir, "--base-url", model.url,
		"--planning", "disabled")
	if code != 0 {
		t.Fatalf("run-due exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "ran schedule sched_x: completed") || !strings.Contains(stdout, "2 missed windows were skipped") {
		t.Fatalf("run-due stdout = %q", stdout)
	}
	updated, ok := mustOpenSchedules(t, path).Get("sched_x")
	if !ok {
		t.Fatal("the schedule disappeared")
	}
	if updated.LastStatus != "completed" || !strings.HasPrefix(updated.LastRunID, "run_") {
		t.Fatalf("recorded = %#v", updated)
	}
	if updated.Missed != 2 || !updated.NextRunAt.After(time.Now()) {
		t.Fatalf("after a firing = %#v", updated)
	}
	if due := mustOpenSchedules(t, path).Due(time.Now()); len(due) != 0 {
		t.Fatalf("the schedule is still due: %#v", due)
	}

	// The recorded run is real: the durable store knows it.
	summaries, closeStore, err := listRuns(context.Background(), "jsonl", checkpointDir)
	if err != nil {
		t.Fatalf("listRuns returned error: %v", err)
	}
	defer func() { _ = closeStore() }()
	found := false
	for _, summary := range summaries {
		if summary.RunID == updated.LastRunID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the recorded run %s is not in the checkpoint store: %#v", updated.LastRunID, summaries)
	}
}

func TestScheduleRunDueJSONIsMachineReadableAndEmptyWhenNothingIsDue(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	path := t.TempDir() + "/schedules.json"
	code, stdout, _ := runScheduleCommand(t, "run-due", "--schedule-file", path, "--json")
	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("exit = %d stdout = %q", code, stdout)
	}

	model := newOpenAISSEStub(t, textChunk("answer"))
	file, err := schedule.Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, err := file.Add(schedule.Entry{ID: "sched_y", Spec: "every 1h", Task: "say hello"}, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	code, stdout, stderr := runScheduleCommand(t, "run-due", "--schedule-file", path, "--json",
		"--workspace", t.TempDir(), "--checkpoint-dir", t.TempDir(), "--base-url", model.url,
		"--planning", "disabled")
	if code != 0 {
		t.Fatalf("exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}
	var results []scheduleResult
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("run-due --json is not JSON (%v): %q", err, stdout)
	}
	if len(results) != 1 || results[0].Schedule != "sched_y" || results[0].Status != "completed" {
		t.Fatalf("results = %#v", results)
	}
	if !strings.HasPrefix(results[0].RunID, "run_") || results[0].NextRunAt == "" {
		t.Fatalf("results = %#v", results)
	}
}

// TestScheduleRunDueFailsLoudly is the timer contract: the firing is recorded
// either way, and the exit status is how cron or launchd learns that one of
// them failed.
func TestScheduleRunDueFailsLoudly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	checkpointDir := t.TempDir()
	path := checkpointDir + "/schedules.json"
	file, err := schedule.Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, err := file.Add(schedule.Entry{ID: "sched_bad", Spec: "every 1h", Task: "say hello"}, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	code, stdout, stderr := runScheduleCommand(t, "run-due", "--schedule-file", path,
		"--workspace", t.TempDir(), "--checkpoint-dir", checkpointDir,
		"--base-url", "http://127.0.0.1:1", "--planning", "disabled")
	if code != exitRuntimeError {
		t.Fatalf("exit = %d, want %d (stdout %q stderr %q)", code, exitRuntimeError, stdout, stderr)
	}
	updated, _ := mustOpenSchedules(t, path).Get("sched_bad")
	if updated.LastStatus != "failed" || updated.LastRunID == "" {
		t.Fatalf("a failed firing was not recorded: %#v", updated)
	}
	if !updated.NextRunAt.After(time.Now()) {
		t.Fatalf("a failed firing did not advance the schedule: %#v", updated)
	}
}

func mustOpenSchedules(t *testing.T, path string) *schedule.File {
	t.Helper()
	file, err := schedule.Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return file
}

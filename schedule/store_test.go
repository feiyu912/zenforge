package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreKeepsSchedulesAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	file, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	entry, err := file.Add(Entry{Spec: "every 1h", Task: "review the repo"}, now)
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if entry.ID == "" || !entry.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("entry = %#v", entry)
	}

	// A restart is just another Open of the same file.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopening returned error: %v", err)
	}
	got, ok := reopened.Get(entry.ID)
	if !ok || got.Task != "review the repo" || !got.NextRunAt.Equal(entry.NextRunAt) {
		t.Fatalf("reopened = %#v (found %v)", got, ok)
	}
	if _, err := reopened.Remove(entry.ID); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	after, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, ok := after.Get(entry.ID); ok || len(after.List()) != 0 {
		t.Fatalf("removal did not persist: %#v", after.List())
	}
	if _, err := after.Remove(entry.ID); err == nil {
		t.Fatal("removing a missing schedule was accepted")
	}
}

func TestStoreValidatesWhatItIsGiven(t *testing.T) {
	file, err := Open(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	now := time.Now()
	if _, err := file.Add(Entry{Spec: "not a schedule", Task: "x"}, now); err == nil {
		t.Fatal("a bad spec was accepted")
	}
	if _, err := file.Add(Entry{Spec: "every 1h", Task: "  "}, now); err == nil {
		t.Fatal("a schedule with no task was accepted")
	}
	if _, err := file.Add(Entry{ID: "sched_a", Spec: "every 1h", Task: "x"}, now); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if _, err := file.Add(Entry{ID: "sched_a", Spec: "every 2h", Task: "y"}, now); err == nil {
		t.Fatal("a duplicate id was accepted")
	}
}

func TestDueAndRecordRunAdvancePastNowWithoutReplaying(t *testing.T) {
	file, err := Open(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	entry, err := file.Add(Entry{ID: "sched_a", Spec: "every 1h", Task: "x"}, start)
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if due := file.Due(start); len(due) != 0 {
		t.Fatalf("a schedule that is not due yet was due: %#v", due)
	}
	// The process was down for three and a half hours. This firing handles the
	// 10:00 window late, and the 11:00 and 12:00 windows are counted as
	// skipped rather than queued: a long downtime must not fire repeatedly.
	now := start.Add(3*time.Hour + 30*time.Minute)
	if due := file.Due(now); len(due) != 1 || due[0].ID != "sched_a" {
		t.Fatalf("due = %#v", due)
	}
	updated, exhausted, err := file.RecordRun("sched_a", "run_1", "completed", now)
	if err != nil {
		t.Fatalf("RecordRun returned error: %v", err)
	}
	if exhausted {
		t.Fatal("an interval schedule was reported as exhausted")
	}
	if updated.Missed != 2 {
		t.Fatalf("missed = %d, want 2", updated.Missed)
	}
	if !updated.NextRunAt.After(now) {
		t.Fatalf("a fired schedule is still due: %#v", updated)
	}
	if updated.LastStatus != "completed" || updated.LastRunID != "run_1" || updated.LastRunAt == nil {
		t.Fatalf("recorded = %#v", updated)
	}
	if due := file.Due(now); len(due) != 0 {
		t.Fatalf("a fired schedule is still due: %#v", due)
	}
	_ = entry
}

func TestRecordRunRemovesAScheduleThatCanNeverMatchAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	file, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := file.Add(Entry{ID: "sched_a", Spec: "every 1h", Task: "x"}, now); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	// A leap-day schedule is legal and simply has no next time once the last
	// leap day has passed; the entry must not sit in the file looking active.
	entry := file.entries["sched_a"]
	entry.Spec = "0 0 29 2 *"
	file.entries["sched_a"] = entry
	// 2100 is not a leap year, so from 2097 no 29 February falls inside the
	// five-year search horizon and the schedule can never match again.
	_, exhausted, err := file.RecordRun("sched_a", "run_1", "completed", time.Date(2097, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RecordRun returned error: %v", err)
	}
	if !exhausted {
		t.Fatal("an exhausted schedule was kept")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(data) != "[]\n" {
		t.Fatalf("file = %q", data)
	}
}

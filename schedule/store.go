package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one durable schedule: the task, when it runs next, and what
// happened the last time something ran it. Everything the loop needs is in
// the entry, because the process that runs it may not be the one that added
// it — that is what makes a schedule survive a restart.
type Entry struct {
	ID        string     `json:"id"`
	Spec      string     `json:"spec"`
	Task      string     `json:"task"`
	Workspace string     `json:"workspace,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	NextRunAt time.Time  `json:"nextRunAt"`
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
	LastRunID string     `json:"lastRunId,omitempty"`
	// LastStatus is the outcome of the last run: completed, failed, or
	// cancelled.
	LastStatus string `json:"lastStatus,omitempty"`
	// Missed counts windows that passed while nothing ran them. A scheduler
	// that starts after downtime reports them instead of replaying them: a
	// weekend of downtime must not mean a hundred queued runs.
	Missed int `json:"missed,omitempty"`
}

// File is a set of schedules stored as one JSON document. It is deliberately
// a plain readable file rather than a store interface: it is small, a person
// may want to look at or edit it, and the run history it points at lives in
// the checkpoint store, not here.
type File struct {
	path    string
	entries map[string]Entry
}

// Open reads the schedules stored at path. A missing or empty file is an
// empty set, which is what a fresh install has.
func Open(path string) (*File, error) {
	file := &File{path: path, entries: map[string]Entry{}}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("no schedules file path")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read schedules %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return file, nil
	}
	var stored []Entry
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("parse schedules %s: %w", path, err)
	}
	for _, entry := range stored {
		file.entries[entry.ID] = entry
	}
	return file, nil
}

// Path is where this set is stored.
func (f *File) Path() string { return f.path }

// List returns every schedule, soonest first.
func (f *File) List() []Entry {
	if f == nil {
		return nil
	}
	out := make([]Entry, 0, len(f.entries))
	for _, entry := range f.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextRunAt.Equal(out[j].NextRunAt) {
			return out[i].NextRunAt.Before(out[j].NextRunAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns one schedule.
func (f *File) Get(id string) (Entry, bool) {
	if f == nil {
		return Entry{}, false
	}
	entry, ok := f.entries[strings.TrimSpace(id)]
	return entry, ok
}

// Add validates and stores a new schedule, returning it with the fields the
// call filled in: the id, the creation time, and the first time it runs. now
// is a parameter rather than a clock read so a test can place a schedule in
// time, and so catch-up logic can be exercised without waiting.
func (f *File) Add(entry Entry, now time.Time) (Entry, error) {
	if f == nil {
		return Entry{}, errors.New("no schedule store")
	}
	spec, err := Parse(entry.Spec)
	if err != nil {
		return Entry{}, err
	}
	entry.Task = strings.TrimSpace(entry.Task)
	if entry.Task == "" {
		return Entry{}, errors.New("a schedule needs a task")
	}
	entry.Workspace = strings.TrimSpace(entry.Workspace)
	entry.ID = strings.TrimSpace(entry.ID)
	if entry.ID == "" {
		entry.ID = newID(now)
	}
	if _, exists := f.entries[entry.ID]; exists {
		return Entry{}, fmt.Errorf("schedule %s already exists", entry.ID)
	}
	next := spec.Next(now)
	if next.IsZero() {
		return Entry{}, fmt.Errorf("schedule %q never matches", spec.Describe())
	}
	entry.CreatedAt = now.UTC()
	entry.NextRunAt = next
	f.entries[entry.ID] = entry
	if err := f.save(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Remove deletes a schedule and returns what it was.
func (f *File) Remove(id string) (Entry, error) {
	if f == nil {
		return Entry{}, errors.New("no schedule store")
	}
	id = strings.TrimSpace(id)
	entry, ok := f.entries[id]
	if !ok {
		return Entry{}, fmt.Errorf("no schedule %s", id)
	}
	delete(f.entries, id)
	if err := f.save(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Due returns the schedules whose next run is at or before now, soonest first.
// A schedule with no next time is never due: it is exhausted, not overdue.
func (f *File) Due(now time.Time) []Entry {
	if f == nil {
		return nil
	}
	var out []Entry
	for _, entry := range f.List() {
		if entry.NextRunAt.IsZero() || entry.NextRunAt.After(now) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// RecordRun notes the outcome of a firing and advances the schedule past now.
// It returns the updated entry and whether the schedule is exhausted (its spec
// can never match again), in which case it has been removed: an entry that can
// never be due again would otherwise sit in the file looking active.
func (f *File) RecordRun(id, runID, status string, now time.Time) (Entry, bool, error) {
	if f == nil {
		return Entry{}, false, errors.New("no schedule store")
	}
	id = strings.TrimSpace(id)
	entry, ok := f.entries[id]
	if !ok {
		return Entry{}, false, fmt.Errorf("no schedule %s", id)
	}
	spec, err := Parse(entry.Spec)
	if err != nil {
		return Entry{}, false, err
	}
	at := now.UTC()
	entry.LastRunAt = &at
	entry.LastRunID = runID
	entry.LastStatus = status
	entry.Missed += missedWindows(spec, entry.NextRunAt, now)
	next := spec.Next(now)
	if next.IsZero() {
		delete(f.entries, id)
		if err := f.save(); err != nil {
			return entry, true, err
		}
		return entry, true, nil
	}
	entry.NextRunAt = next
	f.entries[id] = entry
	if err := f.save(); err != nil {
		return Entry{}, false, err
	}
	return entry, false, nil
}

// maxMissedCount bounds the catch-up scan: a schedule that has been down for
// years reports a bounded count rather than walking every window.
const maxMissedCount = 1000

// missedWindows counts the windows between the last known next-run time and
// now. The window at `from` itself is not counted: it is the one being run.
func missedWindows(spec Spec, from, now time.Time) int {
	if from.IsZero() || !from.Before(now) {
		return 0
	}
	count := 0
	cursor := from
	for count < maxMissedCount {
		next := spec.Next(cursor)
		if next.IsZero() || next.After(now) {
			return count
		}
		count++
		cursor = next
	}
	return count
}

func (f *File) save() error {
	list := f.List()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encode schedules: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create schedules directory: %w", err)
	}
	// Write beside the file and rename, so a reader never sees half a
	// document and a crash leaves the previous set intact.
	temp, err := os.CreateTemp(dir, ".schedules-*")
	if err != nil {
		return fmt.Errorf("create schedules file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write schedules: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write schedules: %w", err)
	}
	if err := os.Rename(tempName, f.path); err != nil {
		return fmt.Errorf("replace schedules: %w", err)
	}
	return nil
}

func newID(now time.Time) string {
	return fmt.Sprintf("sched_%d", now.UnixNano())
}

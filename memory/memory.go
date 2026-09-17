// Package memory keeps durable, cross-run learnings: what an agent found out
// in one run that should shape the next one. It ports the reference's memory
// pipeline in the shape this project can verify: a store of scoped entries,
// a distiller that turns a finished run into new entries, and a
// consolidation step that renders a bounded summary that can be injected
// into the system prompt as instructions.
//
// The reference runs its pipeline in two phases with a state database and a
// consolidation sub-agent. This package keeps the contract (distil, store,
// consolidate, inject) and drops the machinery: the store is a file, the
// consolidation is deterministic, and distillation is one optional model
// call. That keeps memory reviewable by a human, which matters because the
// injected text shapes every later run.
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Scope selects what a memory applies to.
type Scope string

const (
	// ScopeProject applies to one workspace, identified by Project.
	ScopeProject Scope = "project"
	// ScopeUser applies to every run by this user.
	ScopeUser Scope = "user"
)

// ParseScope resolves a scope name.
func ParseScope(name string) (Scope, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case string(ScopeProject):
		return ScopeProject, nil
	case string(ScopeUser), "":
		return ScopeUser, nil
	default:
		return "", fmt.Errorf("unknown memory scope %q (want %s or %s)", name, ScopeProject, ScopeUser)
	}
}

// Entry is one distilled learning.
type Entry struct {
	// ID is stable and derived from the run and content, so re-distilling
	// the same run does not duplicate a memory.
	ID string `json:"id"`
	// Scope and Project decide which runs see the entry.
	Scope   Scope  `json:"scope"`
	Project string `json:"project,omitempty"`
	// RunID is the run the entry came from.
	RunID string `json:"runId,omitempty"`
	// Text is the learning itself, one self-contained statement.
	Text string `json:"text"`
	// CreatedAt is when the entry was written.
	CreatedAt time.Time `json:"createdAt"`
	// Source names the distiller.
	Source string `json:"source,omitempty"`
	// Tags are free-form labels for filtering and diagnostics.
	Tags []string `json:"tags,omitempty"`
	// Citations are the run artifacts the learning came from, so a reader
	// can check it rather than trust it.
	Citations []string `json:"citations,omitempty"`
}

// RunSummary is what a distiller is given about a finished run.
type RunSummary struct {
	RunID    string
	Project  string
	Task     string
	Output   string
	Commands []string
	Failures []string
	Files    []string
	// Events is the run's event trail, already rendered as text and
	// bounded by the caller, for distillers that want more than the
	// structured fields.
	Events string
}

// Distiller turns a finished run into memories.
type Distiller interface {
	// Name identifies the distiller in stored entries.
	Name() string
	// Distill returns zero or more memories. Returning no memories for a
	// run is normal and must not be an error.
	Distill(ctx context.Context, summary RunSummary) ([]Entry, error)
}

// Store persists memories.
type Store interface {
	// Append adds entries, skipping ones whose ID is already stored.
	Append(ctx context.Context, entries ...Entry) (int, error)
	// List returns the entries visible to a project, oldest first.
	List(ctx context.Context, scope Scope, project string) ([]Entry, error)
	// Summary returns the text injected into a run's instructions.
	Summary(ctx context.Context, project string) (string, error)
}

// Defaults for the file store.
const (
	DefaultSummaryBytes = 8 << 10
	DefaultMaxEntries   = 200
	rawFileName         = "raw_memories.md"
	summaryFileName     = "memory_summary.md"
	headerLine          = "# Agent memories"
)

// FileStore is a human-readable memory store: an append-only raw file and a
// consolidated summary. Both are plain markdown on purpose, because a user
// must be able to read and delete what the agent has learned about them.
type FileStore struct {
	// Root is the directory holding the memory files.
	Root string
	// SummaryBytes caps the injected summary.
	SummaryBytes int
	// MaxEntries caps how many entries the summary renders, newest last.
	MaxEntries int
}

// NewFileStore returns a store rooted at dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{Root: dir, SummaryBytes: DefaultSummaryBytes, MaxEntries: DefaultMaxEntries}
}

// Append writes entries to the raw file, skipping duplicates by ID.
func (s *FileStore) Append(ctx context.Context, entries ...Entry) (int, error) {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return 0, fmt.Errorf("memory store root is required")
	}
	if len(entries) == 0 {
		return 0, nil
	}
	existing, err := s.readRaw(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, entry := range existing {
		known[entry.ID] = true
	}
	added := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Text) == "" || entry.ID == "" || known[entry.ID] {
			continue
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = time.Now().UTC()
		}
		known[entry.ID] = true
		added = append(added, entry)
	}
	if len(added) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return 0, fmt.Errorf("create memory root: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(s.Root, rawFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open raw memories: %w", err)
	}
	defer file.Close()
	for _, entry := range added {
		if _, err := file.WriteString(renderEntry(entry)); err != nil {
			return 0, fmt.Errorf("write raw memories: %w", err)
		}
	}
	if err := s.consolidate(ctx); err != nil {
		return 0, err
	}
	return len(added), nil
}

// List returns the entries visible to a project, oldest first.
func (s *FileStore) List(ctx context.Context, scope Scope, project string) ([]Entry, error) {
	entries, err := s.readRaw(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if !entry.Visible(scope, project) {
			continue
		}
		visible = append(visible, entry)
	}
	return visible, nil
}

// Summary returns the consolidated summary, or an empty string when the
// store has nothing to say. It is safe on a nil store, and a missing
// summary file is not an error: no memories is a normal state.
func (s *FileStore) Summary(ctx context.Context, project string) (string, error) {
	_ = ctx
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(s.Root, summaryFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read memory summary: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == headerLine {
		return "", nil
	}
	return text, nil
}

// consolidate renders the summary from the raw entries. It is deterministic:
// the newest entries win the byte budget, and the output is stable for a
// given raw file, so a run's injected instructions can be reproduced.
func (s *FileStore) consolidate(ctx context.Context) error {
	entries, err := s.readRaw(ctx)
	if err != nil {
		return err
	}
	limit := s.MaxEntries
	if limit <= 0 {
		limit = DefaultMaxEntries
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	budget := s.SummaryBytes
	if budget <= 0 {
		budget = DefaultSummaryBytes
	}
	var builder strings.Builder
	builder.WriteString(headerLine)
	for _, entry := range entries {
		rendered := renderSummaryLine(entry)
		if builder.Len()+len(rendered) > budget {
			// The budget is a hard cap: a memory file that grows without
			// bound would silently consume the model's context.
			break
		}
		builder.WriteString(rendered)
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return fmt.Errorf("create memory root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, summaryFileName), []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write memory summary: %w", err)
	}
	return nil
}

// readRaw parses the raw memory file. A malformed block is skipped rather
// than failing the read: one bad line must not hide every memory.
func (s *FileStore) readRaw(ctx context.Context) ([]Entry, error) {
	_ = ctx
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(s.Root, rawFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read raw memories: %w", err)
	}
	return parseRaw(string(data)), nil
}

// Visible reports whether an entry applies to a project.
func (e Entry) Visible(scope Scope, project string) bool {
	if e.Scope == ScopeUser {
		return true
	}
	if e.Scope != ScopeProject {
		return false
	}
	// A project memory without a project name cannot be attributed, so it
	// is not shown anywhere.
	return project != "" && e.Project == project
}

// Storage is the on-disk block format. It is a small key/value block rather
// than JSON so the raw file stays greppable and hand-editable.
func renderEntry(entry Entry) string {
	var builder strings.Builder
	builder.WriteString("\n## " + entry.ID + "\n")
	fmt.Fprintf(&builder, "- scope: %s\n", entry.Scope)
	if entry.Project != "" {
		fmt.Fprintf(&builder, "- project: %s\n", entry.Project)
	}
	if entry.RunID != "" {
		fmt.Fprintf(&builder, "- run: %s\n", entry.RunID)
	}
	if entry.Source != "" {
		fmt.Fprintf(&builder, "- source: %s\n", entry.Source)
	}
	fmt.Fprintf(&builder, "- created: %s\n", entry.CreatedAt.UTC().Format(time.RFC3339))
	if len(entry.Tags) > 0 {
		fmt.Fprintf(&builder, "- tags: %s\n", strings.Join(entry.Tags, ", "))
	}
	for _, citation := range entry.Citations {
		fmt.Fprintf(&builder, "- citation: %s\n", citation)
	}
	builder.WriteString("- text: " + singleLine(entry.Text) + "\n")
	return builder.String()
}

// renderSummaryLine is the injected form: a citation-tagged bullet, so a
// reader (human or model) can always tell which run a memory came from.
func renderSummaryLine(entry Entry) string {
	line := "\n- " + singleLine(entry.Text)
	if entry.RunID != "" {
		line += " (run " + entry.RunID + ")"
	}
	if len(entry.Citations) > 0 {
		line += " [" + strings.Join(entry.Citations, ", ") + "]"
	}
	return line
}

// singleLine flattens text so a block cannot inject new structure.
func singleLine(text string) string {
	fields := strings.Fields(strings.ReplaceAll(text, "\n", " "))
	return strings.Join(fields, " ")
}

// parseRaw reads the block format, tolerating anything malformed.
func parseRaw(text string) []Entry {
	var entries []Entry
	var current *Entry
	flush := func() {
		if current != nil && strings.TrimSpace(current.Text) != "" && current.ID != "" {
			entries = append(entries, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "):
			flush()
			current = &Entry{ID: strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))}
		case current == nil:
		case strings.HasPrefix(trimmed, "- scope: "):
			current.Scope = Scope(strings.TrimSpace(strings.TrimPrefix(trimmed, "- scope: ")))
		case strings.HasPrefix(trimmed, "- project: "):
			current.Project = strings.TrimSpace(strings.TrimPrefix(trimmed, "- project: "))
		case strings.HasPrefix(trimmed, "- run: "):
			current.RunID = strings.TrimSpace(strings.TrimPrefix(trimmed, "- run: "))
		case strings.HasPrefix(trimmed, "- source: "):
			current.Source = strings.TrimSpace(strings.TrimPrefix(trimmed, "- source: "))
		case strings.HasPrefix(trimmed, "- created: "):
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(trimmed, "- created: "))); err == nil {
				current.CreatedAt = parsed
			}
		case strings.HasPrefix(trimmed, "- tags: "):
			for _, tag := range strings.Split(strings.TrimPrefix(trimmed, "- tags: "), ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					current.Tags = append(current.Tags, tag)
				}
			}
		case strings.HasPrefix(trimmed, "- citation: "):
			current.Citations = append(current.Citations, strings.TrimSpace(strings.TrimPrefix(trimmed, "- citation: ")))
		case strings.HasPrefix(trimmed, "- text: "):
			current.Text = strings.TrimSpace(strings.TrimPrefix(trimmed, "- text: "))
		}
	}
	flush()
	return entries
}

// SortEntries orders entries oldest first, breaking ties by ID so the result
// is stable.
func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
}

// Provider is the memory surface the agent uses: a summary to inject at run
// start and a place to record a finished run. It is an interface so the
// agent does not depend on the store or the distiller.
type Provider interface {
	// Summary returns the text to inject into a run's instructions; empty
	// means the run has no memories yet.
	Summary(ctx context.Context) (string, error)
	// Record distils a finished run and stores the result, returning how
	// many entries were added.
	Record(ctx context.Context, summary RunSummary) (int, error)
}

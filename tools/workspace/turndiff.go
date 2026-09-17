package workspace

import (
	"sync"
	"time"

	"github.com/feiyu912/zenforge/diff"
)

// Capture limits keep the in-memory turn-diff store bounded: 2 MiB per
// captured file side, 32 MiB per run window, 256 files per window.
// Over-limit files degrade to path-only notes, mirroring the codex
// turn-diff fallback behavior.
const (
	turnDiffMaxFileBytes = 2 << 20
	turnDiffMaxRunBytes  = 32 << 20
	turnDiffMaxFiles     = 256
)

// TurnFileDiff is one file entry of a drained turn window.
type TurnFileDiff struct {
	Path string `json:"path"`
	// Diff is the unified diff for the turn; empty when Note is set.
	Diff string `json:"diff,omitempty"`
	// Note explains why no diff was rendered (too large, over budget).
	Note string `json:"note,omitempty"`
}

// TurnDiffStore accumulates per-run workspace mutations captured by the
// Write/Edit tools — original and updated content at mutation time —
// and drains them as unified diffs at turn boundaries. The first
// original recorded for a path within a window wins, so a file edited
// three times in one turn diffs once, against the turn's starting
// content. The zero value is not usable; call NewTurnDiffStore.
type TurnDiffStore struct {
	mu   sync.Mutex
	runs map[string]*turnRunState
}

type turnRunState struct {
	order   []string
	entries map[string]*turnEntry
	bytes   int
}

type turnEntry struct {
	original        string
	updated         string
	existedOriginal bool
	tooLarge        bool
}

// NewTurnDiffStore returns an empty store.
func NewTurnDiffStore() *TurnDiffStore {
	return &TurnDiffStore{runs: map[string]*turnRunState{}}
}

// RecordChange captures one successful mutation. A nil store ignores
// the call so tools need no guard of their own.
func (s *TurnDiffStore) RecordChange(runID, path, original string, existedOriginal bool, updated string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		run = &turnRunState{entries: map[string]*turnEntry{}}
		s.runs[runID] = run
	}
	if existing, ok := run.entries[path]; ok {
		if !existing.tooLarge && len(updated) <= turnDiffMaxFileBytes &&
			run.bytes-len(existing.updated)+len(updated) <= turnDiffMaxRunBytes {
			run.bytes += len(updated) - len(existing.updated)
			existing.updated = updated
		} else {
			existing.tooLarge = true
			existing.original = ""
			existing.updated = ""
		}
		return
	}
	entry := &turnEntry{existedOriginal: existedOriginal}
	if len(run.order) >= turnDiffMaxFiles {
		return
	}
	tooLarge := (existedOriginal && len(original) > turnDiffMaxFileBytes) ||
		len(updated) > turnDiffMaxFileBytes ||
		run.bytes+len(original)+len(updated) > turnDiffMaxRunBytes
	if tooLarge {
		entry.tooLarge = true
	} else {
		entry.original = original
		entry.updated = updated
		run.bytes += len(original) + len(updated)
	}
	run.entries[path] = entry
	run.order = append(run.order, path)
}

// Drain returns the unified diffs recorded for the run since the last
// Drain and clears the window. A positive budget bounds diff rendering
// time; files that do not fit degrade to path-only notes. Files whose
// net content is unchanged within the turn are omitted.
func (s *TurnDiffStore) Drain(runID string, budget time.Duration) []TurnFileDiff {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	run := s.runs[runID]
	delete(s.runs, runID)
	s.mu.Unlock()
	if run == nil || len(run.order) == 0 {
		return nil
	}
	var deadline time.Time
	if budget > 0 {
		deadline = time.Now().Add(budget)
	}
	out := make([]TurnFileDiff, 0, len(run.order))
	for _, path := range run.order {
		entry := run.entries[path]
		switch {
		case entry.tooLarge:
			out = append(out, TurnFileDiff{Path: path, Note: "file too large to diff"})
		case !deadline.IsZero() && time.Now().After(deadline):
			out = append(out, TurnFileDiff{Path: path, Note: "diff budget exceeded"})
		default:
			original := ""
			if entry.existedOriginal {
				original = entry.original
			}
			rendered := diff.Unified(original, entry.updated, "a/"+path, "b/"+path)
			if rendered == "" {
				continue
			}
			out = append(out, TurnFileDiff{Path: path, Diff: rendered})
		}
	}
	return out
}

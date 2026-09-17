package workspace

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"

	workspacepkg "github.com/feiyu912/zenforge/workspace"
)

var (
	ErrSnapshotRequired = errors.New("workspace read snapshot required")
	ErrSnapshotStale    = errors.New("workspace read snapshot is stale")
)

type SnapshotStore struct {
	mu        sync.RWMutex
	snapshots map[string]map[string]workspacepkg.FileInfo
	absent    map[string]map[string]struct{}
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{
		snapshots: map[string]map[string]workspacepkg.FileInfo{},
		absent:    map[string]map[string]struct{}{},
	}
}

// RecordAbsentForRun records that the run observed path as absent, for
// example through a read that reported not-found. A later write may
// create the file without a present-version snapshot, mirroring the DSH
// observation policy: the model must look before it creates.
func (s *SnapshotStore) RecordAbsentForRun(runID, path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.absent[runID] == nil {
		s.absent[runID] = map[string]struct{}{}
	}
	s.absent[runID][path] = struct{}{}
}

// AbsentObservedForRun reports whether the run has observed path as
// absent. A nil store observes nothing.
func (s *SnapshotStore) AbsentObservedForRun(runID, path string) bool {
	if s == nil || path == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.absent[runID][path]
	return ok
}

func (s *SnapshotStore) Record(info workspacepkg.FileInfo) {
	s.RecordForRun("", info)
}

func (s *SnapshotStore) RecordForRun(runID string, info workspacepkg.FileInfo) {
	if s == nil || info.Path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshots[runID] == nil {
		s.snapshots[runID] = map[string]workspacepkg.FileInfo{}
	}
	s.snapshots[runID][info.Path] = info
}

func (s *SnapshotStore) Check(info workspacepkg.FileInfo) error {
	return s.CheckForRun("", info)
}

func (s *SnapshotStore) CheckForRun(runID string, info workspacepkg.FileInfo) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	snapshot, ok := s.snapshots[runID][info.Path]
	s.mu.RUnlock()
	if !ok {
		return ErrSnapshotRequired
	}
	if snapshot.Size != info.Size || snapshot.ModTime != info.ModTime || snapshot.IsDir != info.IsDir || snapshot.SHA256 != info.SHA256 {
		return ErrSnapshotStale
	}
	return nil
}

// normalizeSnapshotPath canonicalizes a caller-supplied workspace path so
// absence observations recorded by a read match the path a later write
// checks, regardless of "./" prefixes or redundant separators.
func normalizeSnapshotPath(path string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "." {
		return ""
	}
	return clean
}

package workspace

import (
	"errors"
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
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{snapshots: map[string]map[string]workspacepkg.FileInfo{}}
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

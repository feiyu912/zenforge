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
	snapshots map[string]workspacepkg.FileInfo
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{snapshots: map[string]workspacepkg.FileInfo{}}
}

func (s *SnapshotStore) Record(info workspacepkg.FileInfo) {
	if s == nil || info.Path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[info.Path] = info
}

func (s *SnapshotStore) Check(info workspacepkg.FileInfo) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	snapshot, ok := s.snapshots[info.Path]
	s.mu.RUnlock()
	if !ok {
		return ErrSnapshotRequired
	}
	if snapshot.Size != info.Size || snapshot.ModTime != info.ModTime || snapshot.IsDir != info.IsDir {
		return ErrSnapshotStale
	}
	return nil
}

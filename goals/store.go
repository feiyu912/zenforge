package goals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotFound is returned when a session has no persisted goal state.
var ErrNotFound = fmt.Errorf("goal state not found")

// IsNotFound reports whether err means the session has no goal state.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// Store persists one goal state per session.
type Store interface {
	Load(ctx context.Context, sessionID string) (State, error)
	Save(ctx context.Context, sessionID string, state State) error
	Delete(ctx context.Context, sessionID string) error
}

// MemoryStore keeps goal states in memory.
type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]State
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: map[string]State{}}
}

// Load implements Store.
func (s *MemoryStore) Load(ctx context.Context, sessionID string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return State{}, fmt.Errorf("goal session id is required")
	}
	s.mu.RLock()
	state, ok := s.states[sessionID]
	s.mu.RUnlock()
	if !ok {
		return State{}, ErrNotFound
	}
	return cloneState(state), nil
}

// Save implements Store.
func (s *MemoryStore) Save(ctx context.Context, sessionID string, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("goal session id is required")
	}
	if err := state.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[sessionID] = cloneState(state)
	return nil
}

// Delete implements Store.
func (s *MemoryStore) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[sessionID]; !ok {
		return ErrNotFound
	}
	delete(s.states, sessionID)
	return nil
}

// FileStore keeps goal states as one JSON document per session under a
// root directory. Writes are atomic (temporary file plus rename), so a
// crash cannot leave a half-written goal.
type FileStore struct {
	root string
	mu   sync.Mutex
}

// NewFileStore returns a file-backed store rooted at dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{root: dir}
}

// path returns the session's state file, confined to the store root.
func (s *FileStore) path(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("goal session id is required")
	}
	if strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
		return "", fmt.Errorf("invalid goal session id %q", sessionID)
	}
	return filepath.Join(s.root, sessionID+".json"), nil
}

// Load implements Store.
func (s *FileStore) Load(ctx context.Context, sessionID string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	path, err := s.path(sessionID)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse goal state %s: %w", path, err)
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("read goal state %s: %w", path, err)
	}
	return state, nil
}

// Save implements Store.
func (s *FileStore) Save(ctx context.Context, sessionID string, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return err
	}
	path, err := s.path(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// Delete implements Store.
func (s *FileStore) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// Validate checks the projection's internal consistency.
func (s State) Validate() error {
	if s.Goal != nil {
		if err := s.Goal.Validate(); err != nil {
			return err
		}
	}
	if s.RoundsStarted < 0 {
		return fmt.Errorf("goal roundsStarted must be non-negative")
	}
	if s.Goal != nil && s.RoundsStarted > s.Goal.MaxGoalRounds {
		return fmt.Errorf("goal roundsStarted %d exceeds maxGoalRounds %d", s.RoundsStarted, s.Goal.MaxGoalRounds)
	}
	if s.Goal == nil && (s.RoundsStarted != 0 || !s.CreatedAt.IsZero() || len(s.SeenGoalIDs) > 0) {
		return fmt.Errorf("goal state without a goal must not carry counters")
	}
	if !s.CreatedAt.IsZero() && s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("goal updatedAt cannot precede createdAt")
	}
	for _, id := range s.SeenGoalIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("goal seenGoalIds entries must be non-empty")
		}
	}
	if s.BlockedStreak < 0 {
		return fmt.Errorf("goal blockedStreak must be non-negative")
	}
	if s.BlockedStreak > 0 && strings.TrimSpace(s.LastBlocker) == "" {
		return fmt.Errorf("goal blockedStreak requires a lastBlocker")
	}
	if s.BlockedRound > s.RoundsStarted {
		return fmt.Errorf("goal blockedRound cannot exceed roundsStarted")
	}
	return nil
}

// cloneState deep-copies a state so callers cannot mutate stored values.
func cloneState(state State) State {
	cloned := state
	cloned.SeenGoalIDs = append([]string(nil), state.SeenGoalIDs...)
	if state.Goal != nil {
		goal := *state.Goal
		if state.Goal.BlockedReason != nil {
			reason := *state.Goal.BlockedReason
			goal.BlockedReason = &reason
		}
		cloned.Goal = &goal
	}
	return cloned
}

package jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"golang.org/x/sys/unix"
)

const (
	eventsFileName = "events.jsonl"
	lockFileName   = ".eventlog.lock"
)

type Store struct {
	root string
	mu   *sync.Mutex
	// tails caches each run's last sequence and log size so an append is
	// O(1) instead of a full rescan. The size check is what keeps the
	// cache honest across processes: a concurrent writer changes the
	// file size, which invalidates the entry, and the flock ensures no
	// write is in flight while the cache is read.
	tails sync.Map
}

// tailState is one run's cached log tail.
type tailState struct {
	seq  int64
	size int64
}

// rootLocks coordinates Store instances that target the same on-disk log.
var rootLocks sync.Map

func New(root string) *Store {
	key := filepath.Clean(root)
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}
	lock, _ := rootLocks.LoadOrStore(key, &sync.Mutex{})
	return &Store{root: root, mu: lock.(*sync.Mutex)}
}

func (s *Store) Append(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return fmt.Errorf("event log root is required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	runID := event.RunID()
	if err := validateRunID(runID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lockRoot(ctx)
	if err != nil {
		return err
	}
	defer unlockFile(lock)

	latest, err := s.latestSeqLocked(ctx, runID)
	if err != nil {
		return err
	}
	next := zenforge.NextEventSeq(latest)
	if event.Seq == 0 {
		event.Seq = next
	}
	if event.Seq != next {
		return fmt.Errorf("event seq must be %d, got %d", next, event.Seq)
	}
	if err := event.ValidatePersisted(); err != nil {
		return err
	}

	runDir := filepath.Join(s.root, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(runDir, eventsFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	s.rememberTail(runID, event.Seq)
	return nil
}

func (s *Store) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("event log root is required")
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if seq, ok := s.cachedTail(runID); ok && afterSeq >= seq {
		// The log ends at or before the requested cursor: nothing new
		// can be returned without reopening the file.
		return nil, nil
	}

	var out []zenforge.Event
	err := s.scanLocked(ctx, runID, func(event zenforge.Event) bool {
		if event.Seq <= afterSeq {
			return true
		}
		out = append(out, event)
		return limit <= 0 || len(out) < limit
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) LatestSeq(ctx context.Context, runID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.root == "" {
		return 0, fmt.Errorf("event log root is required")
	}
	if runID == "" {
		return 0, fmt.Errorf("runID is required")
	}
	if err := validateRunID(runID); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.latestSeqLocked(ctx, runID)
}

func (s *Store) latestSeqLocked(ctx context.Context, runID string) (int64, error) {
	if seq, ok := s.cachedTail(runID); ok {
		return seq, nil
	}
	var latest int64
	err := s.scanLocked(ctx, runID, func(event zenforge.Event) bool {
		latest = event.Seq
		return true
	})
	if err != nil {
		return 0, err
	}
	s.rememberTail(runID, latest)
	return latest, nil
}

// cachedTail returns the cached last sequence when the log file still
// has the size it had when the entry was recorded.
func (s *Store) cachedTail(runID string) (int64, bool) {
	cached, ok := s.tails.Load(runID)
	if !ok {
		return 0, false
	}
	state, ok := cached.(tailState)
	if !ok {
		return 0, false
	}
	info, err := os.Stat(filepath.Join(s.root, runID, eventsFileName))
	if err != nil {
		return 0, false
	}
	if info.Size() != state.size {
		return 0, false
	}
	return state.seq, true
}

// rememberTail records the tail for a run, reading the size after a
// write. A missing file clears the entry.
func (s *Store) rememberTail(runID string, seq int64) {
	info, err := os.Stat(filepath.Join(s.root, runID, eventsFileName))
	if err != nil {
		s.tails.Delete(runID)
		return
	}
	s.tails.Store(runID, tailState{seq: seq, size: info.Size()})
}

func (s *Store) scanLocked(ctx context.Context, runID string, visit func(zenforge.Event) bool) error {
	path := filepath.Join(s.root, runID, eventsFileName)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var previousSeq int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var event zenforge.Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("parse JSONL %s: %w", path, err)
		}
		if err := event.ValidatePersisted(); err != nil {
			return fmt.Errorf("read event %s: %w", path, err)
		}
		if event.RunID() != runID {
			return fmt.Errorf("read event %s: runID mismatch %q", path, event.RunID())
		}
		expectedSeq := zenforge.NextEventSeq(previousSeq)
		if event.Seq != expectedSeq {
			return fmt.Errorf("read event %s: event seq must be %d, got %d", path, expectedSeq, event.Seq)
		}
		previousSeq = event.Seq
		if !visit(event) {
			return nil
		}
	}
	return nil
}

func (s *Store) lockRoot(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(s.root, lockFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func unlockFile(file *os.File) {
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func validateRunID(runID string) error {
	if runID == "." || runID == ".." || filepath.IsAbs(runID) || strings.ContainsAny(runID, `/\\`) {
		return fmt.Errorf("invalid runID %q", runID)
	}
	return nil
}

// RunIDs enumerates the runs this store holds, so a caller can list the
// conversations that survived a restart (eventlog.RunLister). A directory entry
// that is not a run is skipped: the store's root also holds its lock file and,
// for a console host, the run registry beside the runs it lists.
func (s *Store) RunIDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("event log root is required")
	}
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := validateRunID(entry.Name()); err != nil {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out, nil
}

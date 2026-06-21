package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	"golang.org/x/sys/unix"
)

const (
	checkpointsFileName = "checkpoints.jsonl"
	latestFileName      = "latest.json"
	pendingFileName     = ".pending.json"
	lockFileName        = ".checkpoint.lock"
)

type Store struct {
	root        string
	mu          *sync.Mutex
	writeLatest func(string, []byte) error
}

type pendingSave struct {
	Checkpoint    checkpoint.Checkpoint `json:"checkpoint"`
	HistoryOffset int64                 `json:"historyOffset"`
}

// rootLocks coordinates Store instances that target the same checkpoint root.
var rootLocks sync.Map

// Summary is the latest checkpoint metadata for a run.
type Summary struct {
	RunID   string    `json:"runId"`
	Seq     int64     `json:"seq"`
	Phase   string    `json:"phase"`
	Status  string    `json:"status"`
	Step    int       `json:"step"`
	SavedAt time.Time `json:"savedAt"`
}

func New(root string) *Store {
	key := filepath.Clean(root)
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}
	lock, _ := rootLocks.LoadOrStore(key, &sync.Mutex{})
	return &Store{root: root, mu: lock.(*sync.Mutex)}
}

func (s *Store) Save(ctx context.Context, cp checkpoint.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return fmt.Errorf("checkpoint root is required")
	}
	if err := checkpoint.Validate(cp); err != nil {
		return err
	}
	if err := validateRunID(cp.RunID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lockRoot(ctx)
	if err != nil {
		return err
	}
	defer unlockFile(lock)

	recovered, err := s.recoverPending(ctx, cp.RunID)
	if err != nil {
		return err
	}
	if recovered != nil && recovered.Seq == cp.Seq {
		want, marshalErr := json.Marshal(cp)
		got, recoveredMarshalErr := json.Marshal(*recovered)
		if marshalErr == nil && recoveredMarshalErr == nil && bytes.Equal(got, want) {
			return nil
		}
	}

	latest, err := s.loadLocked(ctx, cp.RunID)
	if err != nil && !errors.Is(err, checkpoint.ErrNotFound) {
		return err
	}
	if err == nil && cp.Seq <= latest.Seq {
		return fmt.Errorf("%w: runId %q latest seq %d, got %d", checkpoint.ErrStaleCheckpoint, cp.RunID, latest.Seq, cp.Seq)
	}

	runDir := filepath.Join(s.root, cp.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	history, err := os.OpenFile(filepath.Join(runDir, checkpointsFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	info, err := history.Stat()
	closeErr := history.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	txn := pendingSave{Checkpoint: cp, HistoryOffset: info.Size()}
	txnData, err := json.Marshal(txn)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(runDir, pendingFileName, append(txnData, '\n')); err != nil {
		return err
	}
	return s.finishPending(ctx, runDir, txn, encoded)
}

func (s *Store) Load(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("checkpoint root is required")
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lockRoot(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockFile(lock)
	if _, err := s.recoverPending(ctx, runID); err != nil {
		return nil, err
	}
	return s.loadLocked(ctx, runID)
}

func (s *Store) Delete(ctx context.Context, runID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == "" {
		return fmt.Errorf("checkpoint root is required")
	}
	if runID == "" {
		return checkpoint.ErrNotFound
	}
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

	path := filepath.Join(s.root, runID)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return checkpoint.ErrNotFound
	} else if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// List returns latest checkpoint summaries sorted by newest saved time first.
func (s *Store) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("checkpoint root is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lockRoot(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockFile(lock)

	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := validateRunID(entry.Name()); err != nil {
			return nil, err
		}
		if _, err := s.recoverPending(ctx, entry.Name()); err != nil {
			return nil, err
		}
		cp, err := s.loadLocked(ctx, entry.Name())
		if errors.Is(err, checkpoint.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, Summary{
			RunID:   cp.RunID,
			Seq:     cp.Seq,
			Phase:   string(cp.State.Phase),
			Status:  string(cp.State.Control.Status),
			Step:    cp.State.Step,
			SavedAt: cp.SavedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].SavedAt.Equal(summaries[j].SavedAt) {
			return summaries[i].RunID < summaries[j].RunID
		}
		return summaries[i].SavedAt.After(summaries[j].SavedAt)
	})
	return summaries, nil
}

func (s *Store) loadLocked(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, runID, latestFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, checkpoint.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var cp checkpoint.Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	if err := checkpoint.Validate(cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (s *Store) recoverPending(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	runDir := filepath.Join(s.root, runID)
	data, err := os.ReadFile(filepath.Join(runDir, pendingFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var txn pendingSave
	if err := json.Unmarshal(data, &txn); err != nil {
		return nil, fmt.Errorf("parse pending checkpoint: %w", err)
	}
	if txn.Checkpoint.RunID != runID || txn.HistoryOffset < 0 {
		return nil, fmt.Errorf("invalid pending checkpoint for runId %q", runID)
	}
	if err := checkpoint.Validate(txn.Checkpoint); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(txn.Checkpoint)
	if err != nil {
		return nil, err
	}
	if err := s.finishPending(ctx, runDir, txn, encoded); err != nil {
		return nil, err
	}
	return &txn.Checkpoint, nil
}

func (s *Store) finishPending(ctx context.Context, runDir string, txn pendingSave, encoded []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	historyPath := filepath.Join(runDir, checkpointsFileName)
	history, err := os.OpenFile(historyPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	line := append(append([]byte(nil), encoded...), '\n')
	info, err := history.Stat()
	if err == nil && info.Size() < txn.HistoryOffset {
		err = fmt.Errorf("checkpoint history shrank below pending offset")
	}
	if err == nil {
		var existing []byte
		if info.Size() == txn.HistoryOffset+int64(len(line)) {
			existing = make([]byte, len(line))
			_, err = history.ReadAt(existing, txn.HistoryOffset)
		}
		if err == nil && !bytes.Equal(existing, line) {
			err = history.Truncate(txn.HistoryOffset)
			if err == nil {
				_, err = history.WriteAt(line, txn.HistoryOffset)
			}
		}
	}
	if err == nil {
		err = history.Sync()
	}
	closeErr := history.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	writeLatest := s.writeLatest
	if writeLatest == nil {
		writeLatest = func(dir string, data []byte) error {
			return atomicWriteFile(dir, latestFileName, data)
		}
	}
	if err := writeLatest(runDir, line); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(runDir, pendingFileName)); err != nil {
		return err
	}
	return syncDir(runDir)
}

func atomicWriteFile(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	closeErr := tmp.Close()
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
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

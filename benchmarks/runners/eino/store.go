package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// fileCheckPointStore is the durable store the runner hands to Eino.
//
// It implements Eino's own persistence interface,
// github.com/cloudwego/eino/compose.CheckPointStore
// (Get(ctx, id) ([]byte, bool, error) / Set(ctx, id, []byte) error), and the
// optional github.com/cloudwego/eino/compose.CheckPointDeleter. Eino encodes
// the checkpoint payload itself (gob for the ADK runner); this store only
// writes those opaque bytes to BENCH_STATE_DIR and reads them back, which is
// exactly what the interface asks a store to do.
//
// checkPointID is hashed into the file name so a hostile id cannot escape the
// state directory, and writes go through a temp file + rename so a checkpoint
// is either fully present or absent.
type fileCheckPointStore struct {
	dir string
}

func newFileCheckPointStore(dir string) (*fileCheckPointStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating checkpoint directory: %w", err)
	}
	return &fileCheckPointStore{dir: dir}, nil
}

func (s *fileCheckPointStore) path(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.dir, "checkpoint-"+hex.EncodeToString(sum[:16])+".gob")
}

func (s *fileCheckPointStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading checkpoint %q: %w", id, err)
	}
	return b, true, nil
}

func (s *fileCheckPointStore) Set(_ context.Context, id string, checkpoint []byte) error {
	final := s.path(id)
	tmp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("writing checkpoint %q: %w", id, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(checkpoint); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing checkpoint %q: %w", id, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("writing checkpoint %q: %w", id, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("writing checkpoint %q: %w", id, err)
	}
	return nil
}

// Delete satisfies compose.CheckPointDeleter.
func (s *fileCheckPointStore) Delete(_ context.Context, id string) error {
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// resumeState is the runner's own sidecar, NOT conversation state.
//
// Eino's checkpoint holds the whole graph state (message history, pending tool
// call, interrupt state). The only thing Eino's checkpoint API does not carry
// across processes is the *operator decision*: which interrupt ids the second
// process must target, and with what answer. That is what this file records,
// and it is the same datum an approval UI would keep between two requests.
type resumeState struct {
	Task         string   `json:"task"`
	CheckpointID string   `json:"checkpoint_id"`
	InterruptIDs []string `json:"interrupt_ids"`
	Commands     []string `json:"commands,omitempty"`
	Framework    string   `json:"framework"`
}

const resumeStateFile = "eino-resume.json"

func writeResumeState(dir string, st resumeState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, resumeStateFile), b, 0o644)
}

func readResumeState(dir string) (resumeState, error) {
	b, err := os.ReadFile(filepath.Join(dir, resumeStateFile))
	if err != nil {
		return resumeState{}, err
	}
	var st resumeState
	if err := json.Unmarshal(b, &st); err != nil {
		return resumeState{}, fmt.Errorf("parsing %s: %w", resumeStateFile, err)
	}
	return st, nil
}

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/model"
)

// The console's attachment store, on disk.
//
// It is content-addressed and session-scoped. The bytes of one upload live under
// `attachments/objects/<sha256>` and are shared by every session that uploaded
// the same file, because they are the same file; the *descriptor* -- the id the
// console cites, the display name, the dimensions -- lives under
// `attachments/sessions/<session-scope>/<id>.json`, so a name one session gave a
// file is not visible to another. The id is derived from the digest, so reading
// an object can verify that what it read is what was stored, and a truncated or
// swapped file is reported rather than served with metadata that describes
// something else.
//
// Both upload routes end up here (the unary RPC and the raw-byte route); the
// store cannot tell them apart, which is the point.

// attachmentStoreDir is the directory under the host's checkpoint directory. It
// is the same state tree the runs and checkpoints live in, so `--checkpoint-dir`
// moves the attachments with the sessions that refer to them.
const attachmentStoreDir = "attachments"

// attachmentObjectsDirName and attachmentSessionsDirName split bytes from
// descriptors. The bytes are content-addressed and shared; the descriptors are
// per session, because a name and a session's access are not properties of the
// bytes.
const (
	attachmentObjectsDirName  = "objects"
	attachmentSessionsDirName = "sessions"
	attachmentPromptsDirName  = "prompts"
)

// promptCopyDirName is where a file a prompt carried is published for the agent
// to read, relative to the workspace root. The file tools are rooted in the
// workspace (workspace/local opens a root and refuses paths outside it), so a
// copy anywhere else would be a path the model is told to read and cannot.
const promptCopyDirName = ".zenforge/attachments"

// attachmentDiskStore is the file-backed [dshapi.AttachmentStore].
type attachmentDiskStore struct {
	root string
	// workspace is the root a prompt's file copies are published under. Empty
	// means this host has no tool world to publish into, which the model is told
	// rather than handed a path it cannot read.
	workspace string
}

// consoleAttachments builds the store under the host's checkpoint directory. A
// directory that cannot be created is nil, so the console's attachment family
// answers `unimplemented` with the dependency named instead of failing every
// upload with an internal error.
func consoleAttachments(checkpointDir, workspace string) dshapi.AttachmentStore {
	if strings.TrimSpace(checkpointDir) == "" {
		return nil
	}
	root := filepath.Join(checkpointDir, attachmentStoreDir)
	for _, directory := range []string{
		filepath.Join(root, attachmentObjectsDirName),
		filepath.Join(root, attachmentSessionsDirName),
		filepath.Join(root, attachmentPromptsDirName),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil
		}
	}
	return &attachmentDiskStore{root: root, workspace: strings.TrimSpace(workspace)}
}

// attachmentRecord is the sidecar: the descriptor plus the session that owns it
// and the object path its bytes live at, so a read does not have to re-derive
// storage layout decisions that a later version may change.
type attachmentRecord struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id"`
	Digest    string `json:"digest"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     int    `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// Save stores the exact bytes and returns their descriptor.
func (s *attachmentDiskStore) Save(_ context.Context, sessionID, name string, data []byte) (dshapi.AttachmentDescriptor, error) {
	if strings.TrimSpace(sessionID) == "" {
		return dshapi.AttachmentDescriptor{}, errors.New("attachment requires a session")
	}
	digest := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(digest[:])
	id := "att-" + hexDigest[:32]

	// The media type comes from the bytes, never from the name: a file called
	// `note.txt` that is a PNG is a PNG, and a file called `photo.png` that is
	// text is not an image. Dimensions are only read for an image, and an image
	// whose header this host cannot parse (WebP) keeps zero dimensions rather
	// than a guess -- the read method names that rather than describing it.
	mediaType := model.DetectImageMediaType(data)
	width, height := 0, 0
	if mediaType != "" {
		if w, h, err := model.ImageSize(mediaType, data); err == nil {
			width, height = w, h
		}
	}

	objectPath := filepath.Join(s.root, attachmentObjectsDirName, hexDigest)
	if err := writeAttachmentFile(objectPath, data); err != nil {
		return dshapi.AttachmentDescriptor{}, err
	}
	record := attachmentRecord{
		SessionID: sessionID,
		ID:        id,
		Digest:    hexDigest,
		Name:      name,
		MediaType: mediaType,
		Bytes:     len(data),
		Width:     width,
		Height:    height,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return dshapi.AttachmentDescriptor{}, fmt.Errorf("encode attachment record: %w", err)
	}
	recordPath, err := s.recordPath(sessionID, id)
	if err != nil {
		return dshapi.AttachmentDescriptor{}, err
	}
	if err := writeAttachmentFile(recordPath, encoded); err != nil {
		return dshapi.AttachmentDescriptor{}, err
	}
	return record.descriptor(), nil
}

// Read returns the descriptor and the exact bytes, and refuses an id that was
// never stored for this session.
func (s *attachmentDiskStore) Read(_ context.Context, sessionID, attachmentID string) (dshapi.AttachmentDescriptor, []byte, error) {
	recordPath, err := s.recordPath(sessionID, attachmentID)
	if err != nil {
		return dshapi.AttachmentDescriptor{}, nil, dshapi.ErrAttachmentNotFound
	}
	encoded, err := os.ReadFile(recordPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return dshapi.AttachmentDescriptor{}, nil, dshapi.ErrAttachmentNotFound
		}
		return dshapi.AttachmentDescriptor{}, nil, fmt.Errorf("read attachment record: %w", err)
	}
	var record attachmentRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return dshapi.AttachmentDescriptor{}, nil, fmt.Errorf("decode attachment record: %w", err)
	}
	// A record that names another session is not this session's attachment, even
	// if the path was guessed: the id is not a capability.
	if record.SessionID != sessionID {
		return dshapi.AttachmentDescriptor{}, nil, dshapi.ErrAttachmentNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.root, attachmentObjectsDirName, record.Digest))
	if err != nil {
		return dshapi.AttachmentDescriptor{}, nil, fmt.Errorf("read attachment bytes: %w", err)
	}
	// The id is the digest's prefix, so the bytes can be checked against the name
	// they are served under: a swapped or truncated object is an error, never a
	// descriptor that describes different bytes.
	digest := sha256.Sum256(data)
	if !strings.HasPrefix(hex.EncodeToString(digest[:]), strings.TrimPrefix(record.ID, "att-")) {
		return dshapi.AttachmentDescriptor{}, nil, fmt.Errorf("attachment %q does not match its stored bytes", attachmentID)
	}
	return record.descriptor(), data, nil
}

// descriptor is the record's public half.
func (r attachmentRecord) descriptor() dshapi.AttachmentDescriptor {
	return dshapi.AttachmentDescriptor{
		ID:        r.ID,
		Name:      r.Name,
		MediaType: r.MediaType,
		Bytes:     r.Bytes,
		Width:     r.Width,
		Height:    r.Height,
	}
}

// recordPath is the sidecar's path for one session and id. The session directory
// is a digest so a session id with a slash or a dot cannot escape the tree.
func (s *attachmentDiskStore) recordPath(sessionID, attachmentID string) (string, error) {
	scope := sha256.Sum256([]byte(sessionID))
	if !strings.HasPrefix(attachmentID, "att-") || strings.ContainsAny(attachmentID, `/\`) {
		return "", fmt.Errorf("invalid attachment id %q", attachmentID)
	}
	directory := filepath.Join(s.root, attachmentSessionsDirName, hex.EncodeToString(scope[:])[:16])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(directory, attachmentID+".json"), nil
}

// writeAttachmentFile writes bytes at path atomically: a reader either sees the
// previous content or the complete new one, never a half-written file.
func writeAttachmentFile(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		// Already stored: the bytes are content-addressed, so an existing object
		// is the same content, and rewriting it would only risk a torn read.
		return nil
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".staged-*")
	if err != nil {
		return fmt.Errorf("stage attachment: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write attachment: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close attachment: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("protect attachment: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish attachment: %w", err)
	}
	return nil
}

// promptRecordPath is the sidecar's path for one run's prompt. The run id is a
// session id or a continuation of one (`sess~2`), both filesystem-safe, and a
// digest keeps even an unexpected one from escaping the tree.
func (s *attachmentDiskStore) promptRecordPath(runID string) string {
	scope := sha256.Sum256([]byte(runID))
	return filepath.Join(s.root, attachmentPromptsDirName, hex.EncodeToString(scope[:])[:16]+".json")
}

// RecordPromptAttachments publishes what one turn's prompt carried, keyed by the
// run that turn is. It is written before the console can read the turn's
// projected message, so the transcript never has to guess.
func (s *attachmentDiskStore) RecordPromptAttachments(_ context.Context, sessionID, runID string, attachments []dshapi.PromptAttachment) error {
	if strings.TrimSpace(runID) == "" {
		return errors.New("a prompt record requires the run it belongs to")
	}
	stored := attachmentsPromptRecord{SessionID: sessionID, RunID: runID, Attachments: attachments}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode prompt attachments: %w", err)
	}
	path := s.promptRecordPath(runID)
	temp, err := os.CreateTemp(filepath.Dir(path), ".staged-*")
	if err != nil {
		return fmt.Errorf("stage prompt attachments: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write prompt attachments: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close prompt attachments: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("protect prompt attachments: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish prompt attachments: %w", err)
	}
	return nil
}

// promptAttachments is the projector's half: what one run's prompt carried, or
// nothing. A record that will not decode is not an attachment this host can
// describe, so it projects the turn as an unattached prompt rather than failing
// the console's stream.
func (s *attachmentDiskStore) PromptAttachments(_ context.Context, runID string) ([]dshapi.PromptAttachment, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, nil
	}
	encoded, err := os.ReadFile(s.promptRecordPath(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var stored attachmentsPromptRecord
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return nil, fmt.Errorf("decode prompt attachments: %w", err)
	}
	if stored.RunID != runID {
		return nil, fmt.Errorf("prompt attachments for %q name %q", runID, stored.RunID)
	}
	return stored.Attachments, nil
}

// attachmentsPromptRecord is one run's prompt attachments, on disk.
type attachmentsPromptRecord struct {
	SessionID   string                    `json:"sessionId"`
	RunID       string                    `json:"runId"`
	Attachments []dshapi.PromptAttachment `json:"attachments"`
}

// MaterializePromptFile publishes a stored file as a verbatim read-only copy in
// the workspace, and returns the path the run is told to read -- the same form
// the reference host hands a run (dsh-llm `fileHandleText`). An empty path means
// there is no tool world to publish into.
func (s *attachmentDiskStore) MaterializePromptFile(ctx context.Context, sessionID, attachmentID string) (string, error) {
	if s.workspace == "" {
		return "", nil
	}
	descriptor, data, err := s.Read(ctx, sessionID, attachmentID)
	if err != nil {
		return "", err
	}
	name := attachmentsCopyName(descriptor)
	directory := filepath.Join(s.workspace, filepath.FromSlash(promptCopyDirName))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create the file directory: %w", err)
	}
	path := filepath.Join(directory, name)
	if err := writeReadOnlyCopy(path, data); err != nil {
		return "", err
	}
	// The agent's file tools are rooted in the workspace, so the path it is given
	// is the one those tools accept: relative to that root.
	return pathpkg.Join(promptCopyDirName, name), nil
}

// attachmentsCopyName names the published copy after the digest and the display
// name, so two different files with the same name cannot overwrite each other and
// the name the operator chose is still visible.
func attachmentsCopyName(descriptor dshapi.AttachmentDescriptor) string {
	digest := strings.TrimPrefix(descriptor.ID, "att-")
	if len(digest) > 12 {
		digest = digest[:12]
	}
	name := sanitizeAttachmentName(descriptor.Name)
	if name == "" {
		name = "file"
	}
	return digest + "-" + name
}

// writeReadOnlyCopy publishes bytes at path with mode 0444, atomically and
// idempotently: the copy is verbatim and read-only, exactly as the run is told,
// and publishing it twice replaces it rather than failing.
func writeReadOnlyCopy(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".staged-*")
	if err != nil {
		return fmt.Errorf("stage the file copy: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write the file copy: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close the file copy: %w", err)
	}
	if err := os.Chmod(tempPath, 0o444); err != nil {
		return fmt.Errorf("protect the file copy: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish the file copy: %w", err)
	}
	return nil
}

// consolePromptAttachments is the provider the console's stream is given: what one
// run's prompt carried, by run id.
func consolePromptAttachments(store dshapi.AttachmentStore, ctx context.Context) func(runID string) []dshapi.PromptAttachment {
	return func(runID string) []dshapi.PromptAttachment {
		if store == nil {
			return nil
		}
		recorded, err := store.PromptAttachments(ctx, runID)
		if err != nil {
			return nil
		}
		return recorded
	}
}

// sanitizeAttachmentName reduces a display name to one path component: a name is
// the operator's text, and a copy published in the workspace must not be able to
// name a directory of its own.
func sanitizeAttachmentName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	cleaned := strings.Map(func(character rune) rune {
		switch character {
		case '/', '\\', 0:
			return -1
		}
		if character < 0x20 {
			return -1
		}
		return character
	}, name)
	return cleaned
}

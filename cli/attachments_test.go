package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshapi"
)

// The console's attachment store is on disk, content-addressed and
// session-scoped. These tests pin the three things that make it a store rather
// than a directory: the bytes that come back are the bytes that went in, a
// session cannot read another session's attachment by guessing its id, and the
// descriptor's dimensions are read from the image header rather than trusted from
// a name.
func TestAttachmentStoreRoundTripsBytesAndDimensions(t *testing.T) {
	store := consoleAttachments(t.TempDir(), t.TempDir())
	if store == nil {
		t.Fatal("consoleAttachments returned nil for a writable directory")
	}
	encoded := testPNG(t, 7, 4)

	descriptor, err := store.Save(context.Background(), "session-a", "pixels.png", encoded)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if descriptor.MediaType != "image/png" || descriptor.Width != 7 || descriptor.Height != 4 {
		t.Fatalf("descriptor = %+v, want a PNG with the header's dimensions", descriptor)
	}
	if descriptor.Bytes != len(encoded) || descriptor.Name != "pixels.png" {
		t.Fatalf("descriptor = %+v, want the stored size and name", descriptor)
	}

	read, data, err := store.Read(context.Background(), "session-a", descriptor.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(data, encoded) {
		t.Fatalf("read %d bytes, want the %d stored", len(data), len(encoded))
	}
	if read.ID != descriptor.ID || read.Width != 7 || read.Height != 4 {
		t.Fatalf("read descriptor = %+v, want the stored descriptor", read)
	}

	// The bytes are content-addressed: the same file uploaded twice is one object
	// and one id, so the console's receipt is stable.
	again, err := store.Save(context.Background(), "session-b", "copy.png", encoded)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if again.ID != descriptor.ID {
		t.Fatalf("ids = %q and %q, want one content-addressed id", descriptor.ID, again.ID)
	}
	// The descriptor, though, is per session: the name one session gave the file
	// is not visible to the other.
	if read, _, err := store.Read(context.Background(), "session-b", descriptor.ID); err != nil || read.Name != "copy.png" {
		t.Fatalf("session-b descriptor = %+v (%v), want its own name", read, err)
	}
}

func TestAttachmentStoreScopesReadsBySession(t *testing.T) {
	store := consoleAttachments(t.TempDir(), t.TempDir())
	encoded := testPNG(t, 2, 2)
	descriptor, err := store.Save(context.Background(), "session-a", "pixels.png", encoded)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, _, err := store.Read(context.Background(), "session-b", descriptor.ID); !errors.Is(err, dshapi.ErrAttachmentNotFound) {
		t.Fatalf("Read as another session = %v, want not-found", err)
	}
	if _, _, err := store.Read(context.Background(), "session-a", "att-0000"); !errors.Is(err, dshapi.ErrAttachmentNotFound) {
		t.Fatalf("Read an unknown id = %v, want not-found", err)
	}
	// A path-shaped id is refused rather than resolved: the id is not a path.
	if _, _, err := store.Read(context.Background(), "session-a", "../../etc/passwd"); !errors.Is(err, dshapi.ErrAttachmentNotFound) {
		t.Fatalf("Read a traversal id = %v, want not-found", err)
	}
}

// A WebP is measured like any other image now that its header reader exists
// (ADR 0139): the descriptor carries the canvas size the console renders with.
func TestAttachmentStoreMeasuresWebPImages(t *testing.T) {
	store := consoleAttachments(t.TempDir(), t.TempDir())
	descriptor, err := store.Save(context.Background(), "session-a", "picture.webp", testWebP())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if descriptor.MediaType != "image/webp" {
		t.Fatalf("descriptor = %+v, want the sniffer's type", descriptor)
	}
	if descriptor.Width != 4 || descriptor.Height != 3 {
		t.Fatalf("descriptor = %+v, want the lossless header's dimensions", descriptor)
	}
	_, data, err := store.Read(context.Background(), "session-a", descriptor.ID)
	if err != nil || !bytes.Equal(data, testWebP()) {
		t.Fatalf("read = %d bytes (%v), want the stored bytes", len(data), err)
	}
}

// A header this host cannot read is stored with no dimensions, which is what lets
// the read method name the missing reader instead of describing it with a guess.
func TestAttachmentStoreStoresAnUnreadableImageWithoutDimensions(t *testing.T) {
	store := consoleAttachments(t.TempDir(), t.TempDir())
	truncated := testWebP()
	descriptor, err := store.Save(context.Background(), "session-a", "broken.webp", truncated[:20])
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if descriptor.MediaType != "image/webp" || descriptor.Width != 0 || descriptor.Height != 0 {
		t.Fatalf("descriptor = %+v, want the type with no dimensions", descriptor)
	}
}

// A file that is not an image is stored the same way, with no media type and no
// dimensions: the store does not decide what may be uploaded, the read method
// decides what it can describe.
func TestAttachmentStoreStoresNonImages(t *testing.T) {
	store := consoleAttachments(t.TempDir(), t.TempDir())
	descriptor, err := store.Save(context.Background(), "session-a", "note.txt", []byte("just text"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if descriptor.MediaType != "" || descriptor.Width != 0 {
		t.Fatalf("descriptor = %+v, want no image metadata for text", descriptor)
	}
}

// The store's tree is the host's state tree, and an unwritable one is reported as
// no store at all: the family then answers unimplemented rather than failing every
// upload with an internal error.
func TestAttachmentStoreRefusesADirectoryItCannotUse(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if store := consoleAttachments(file, ""); store != nil {
		t.Fatal("consoleAttachments must return nil when the state path is a file")
	}
	if store := consoleAttachments("  ", ""); store != nil {
		t.Fatal("consoleAttachments must return nil without a checkpoint directory")
	}
}

// The store keeps the operator's bytes under the host's own state directory, and
// nothing it writes is world-readable.
func TestAttachmentStoreWritesPrivateFiles(t *testing.T) {
	root := t.TempDir()
	store := consoleAttachments(root, "")
	if store == nil {
		t.Fatal("consoleAttachments returned nil")
	}
	if _, err := store.Save(context.Background(), "session-a", "note.txt", []byte("hello")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var inspected int
	err := filepath.WalkDir(filepath.Join(root, attachmentStoreDir), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		inspected++
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s is mode %o, want no group or world access", path, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store: %v", err)
	}
	if inspected == 0 {
		t.Fatal("the store wrote no files at all")
	}
}

// A name with path separators must not decide where the record lands.
func TestAttachmentStoreSanitizesNames(t *testing.T) {
	root := t.TempDir()
	store := consoleAttachments(root, "")
	descriptor, err := store.Save(context.Background(), "../../escape", "x.txt", []byte("payload"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if strings.Contains(descriptor.ID, "/") {
		t.Fatalf("id = %q, want a path-free id", descriptor.ID)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read store root: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "escape" {
			t.Fatal("a session id escaped the store's own directory")
		}
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, width, height))
	pixels.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, pixels); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

// testWebP is a real lossless WebP of 4x3, produced by Pillow and embedded so the
// store's own test measures a real container rather than a hand-built header.
func testWebP() []byte {
	encoded, err := base64.StdEncoding.DecodeString(testWebPBase64)
	if err != nil {
		panic(err)
	}
	return encoded
}

const testWebPBase64 = "UklGRh4AAABXRUJQVlA4TBEAAAAvA4AAAAdQiirUo/+BiOh/AAA="

// A prompt's attachments are published under the run the turn is, so the stream
// that projects that turn's log can answer what its message carried. The record
// is keyed by run id and holds the transcript blocks verbatim.
func TestAttachmentStoreRecordsAndReadsPromptAttachments(t *testing.T) {
	store := consoleAttachments(t.TempDir(), t.TempDir())
	disk, ok := store.(*attachmentDiskStore)
	if !ok {
		t.Fatalf("store = %T, want the disk store", store)
	}
	attachments := []dshapi.PromptAttachment{
		{Kind: "image", AttachmentID: "att-image", Name: "pixels.png", MediaType: "image/png", Bytes: 73, Width: 4, Height: 3},
		{Kind: "file", AttachmentID: "att-file", Name: "notes.txt", Bytes: 16, Path: ".zenforge/attachments/abc-notes.txt"},
	}
	if err := store.RecordPromptAttachments(context.Background(), "session-a", "session-a~2", attachments); err != nil {
		t.Fatalf("RecordPromptAttachments: %v", err)
	}
	recorded, err := disk.PromptAttachments(context.Background(), "session-a~2")
	if err != nil {
		t.Fatalf("PromptAttachments: %v", err)
	}
	if len(recorded) != 2 || recorded[0] != attachments[0] || recorded[1] != attachments[1] {
		t.Fatalf("recorded = %+v, want %+v", recorded, attachments)
	}
	// The record belongs to one run: another run of the same session has none, and
	// that is an unattached prompt rather than the previous turn's attachments.
	if other, err := disk.PromptAttachments(context.Background(), "session-a~3"); err != nil || len(other) != 0 {
		t.Fatalf("another run's attachments = %+v, want none", other)
	}
	if none, err := disk.PromptAttachments(context.Background(), ""); err != nil || none != nil {
		t.Fatalf("empty run id = %+v, want nothing", none)
	}
	// The sidecar is private, like every other record in the store.
	entries, err := os.ReadDir(filepath.Join(disk.root, attachmentPromptsDirName))
	if err != nil || len(entries) != 1 {
		t.Fatalf("prompt records = %v (%v), want one", entries, err)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat prompt record: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt record mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(disk.root, attachmentPromptsDirName, "session-a~2.json")); err == nil {
		t.Fatal("the run id is the record's name, so a run id with a slash could escape")
	}
}

// A file a prompt carried is published as a verbatim read-only copy inside the
// workspace, because the agent's file tools are rooted there: a path the run is
// told to read has to be one those tools can.
func TestAttachmentStorePublishesPromptFilesReadOnly(t *testing.T) {
	workspace := t.TempDir()
	store := consoleAttachments(t.TempDir(), workspace)
	descriptor, err := store.Save(context.Background(), "session-a", "notes.txt", []byte("the file's words"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	path, err := store.MaterializePromptFile(context.Background(), "session-a", descriptor.ID)
	if err != nil {
		t.Fatalf("MaterializePromptFile: %v", err)
	}
	want := pathpkg.Join(promptCopyDirName, strings.TrimPrefix(descriptor.ID, "att-")[:12]+"-notes.txt")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	published, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read the published copy: %v", err)
	}
	if !bytes.Equal(published, []byte("the file's words")) {
		t.Fatalf("copy = %q, want the stored bytes verbatim", published)
	}
	info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("stat the published copy: %v", err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("copy mode = %v, want 0444", info.Mode().Perm())
	}
	// Publishing again replaces the copy rather than failing, so a prompt that
	// cites the same receipt twice is not a second error.
	if _, err := store.MaterializePromptFile(context.Background(), "session-a", descriptor.ID); err != nil {
		t.Fatalf("MaterializePromptFile twice: %v", err)
	}
	// A session that never staged the id cannot publish it, and a host with no
	// workspace publishes nothing rather than a path nobody can read.
	if _, err := store.MaterializePromptFile(context.Background(), "session-b", descriptor.ID); !errors.Is(err, dshapi.ErrAttachmentNotFound) {
		t.Fatalf("another session = %v, want ErrAttachmentNotFound", err)
	}
	headless := consoleAttachments(t.TempDir(), "")
	if path, err := headless.MaterializePromptFile(context.Background(), "session-a", descriptor.ID); err != nil || path != "" {
		t.Fatalf("no workspace = %q (%v), want no path", path, err)
	}
}

// A name is the operator's text, and the published copy's name is one path
// component: a name that tries to name a directory must not escape the store's
// own directory.
func TestAttachmentCopyNameCannotEscapeItsDirectory(t *testing.T) {
	descriptor := dshapi.AttachmentDescriptor{ID: "att-0123456789abcdef", Name: "../../escape"}
	copied := attachmentsCopyName(descriptor)
	if copied != "0123456789ab-escape" {
		t.Fatalf("copy name = %q, want one sanitized component", copied)
	}
	if empty := attachmentsCopyName(dshapi.AttachmentDescriptor{ID: "att-0123456789abcdef"}); empty != "0123456789ab-file" {
		t.Fatalf("nameless copy = %q, want a default name", empty)
	}
}

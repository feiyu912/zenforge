package cli

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
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
	store := consoleAttachments(t.TempDir())
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
	store := consoleAttachments(t.TempDir())
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

// A WebP is an image this host stores and cannot measure. The descriptor says so
// by leaving the dimensions zero, which is what lets the read method refuse by
// name instead of describing it with a guess.
func TestAttachmentStoreStoresUnmeasurableImagesWithoutDimensions(t *testing.T) {
	store := consoleAttachments(t.TempDir())
	descriptor, err := store.Save(context.Background(), "session-a", "picture.webp", testWebP())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if descriptor.MediaType != "image/webp" {
		t.Fatalf("descriptor = %+v, want the sniffer's type", descriptor)
	}
	if descriptor.Width != 0 || descriptor.Height != 0 {
		t.Fatalf("descriptor = %+v, want no dimensions for an unreadable header", descriptor)
	}
	_, data, err := store.Read(context.Background(), "session-a", descriptor.ID)
	if err != nil || !bytes.Equal(data, testWebP()) {
		t.Fatalf("read = %d bytes (%v), want the stored bytes", len(data), err)
	}
}

// A file that is not an image is stored the same way, with no media type and no
// dimensions: the store does not decide what may be uploaded, the read method
// decides what it can describe.
func TestAttachmentStoreStoresNonImages(t *testing.T) {
	store := consoleAttachments(t.TempDir())
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
	if store := consoleAttachments(file); store != nil {
		t.Fatal("consoleAttachments must return nil when the state path is a file")
	}
	if store := consoleAttachments("  "); store != nil {
		t.Fatal("consoleAttachments must return nil without a checkpoint directory")
	}
}

// The store keeps the operator's bytes under the host's own state directory, and
// nothing it writes is world-readable.
func TestAttachmentStoreWritesPrivateFiles(t *testing.T) {
	root := t.TempDir()
	store := consoleAttachments(root)
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
	store := consoleAttachments(root)
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

func testWebP() []byte {
	data := make([]byte, 25)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8L")
	data[20] = 0x2f
	return data
}

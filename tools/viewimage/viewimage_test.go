package viewimage

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/workspace/local"
)

func writePNG(t *testing.T, root, name string) int {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("png.Encode returned error: %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return buffer.Len()
}

func TestViewImageReturnsTheImageOnTheResult(t *testing.T) {
	root := t.TempDir()
	size := writePNG(t, root, "docs/diagram.png")
	ws, err := local.New(local.Config{Root: root, AllowBinaryRead: true, MaxReadBytes: 1 << 20})
	if err != nil {
		t.Fatalf("local.New returned error: %v", err)
	}
	viewer, err := New(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"path": "docs/diagram.png", "detail": "high"})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	result, err := viewer.Call(context.Background(), raw, tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	images, ok := result.Meta[ImageMetadataKey].([]model.Image)
	if !ok || len(images) != 1 {
		t.Fatalf("metadata = %#v", result.Meta)
	}
	image := images[0]
	if image.MediaType != "image/png" || image.Path != "docs/diagram.png" || image.Detail != "high" || len(image.Data) != size {
		t.Fatalf("image = %#v", image)
	}
	// The model-visible result is text: the bytes travel in the message, so
	// repeating them here would double the cost of every later request.
	encoded, err := json.Marshal(result.Structured)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if bytes.Contains(encoded, []byte("iVBOR")) {
		t.Fatalf("the tool result contained the image bytes: %s", encoded)
	}
	if !strings.Contains(string(encoded), "image/png") {
		t.Fatalf("the result did not describe the image: %s", encoded)
	}
}

func TestViewImageRefusesNonImagesAndEscapes(t *testing.T) {
	root := t.TempDir()
	writePNG(t, root, "ok.png")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	// A PNG renamed to .txt is still an image; a text file renamed to .png
	// is not, because the media type comes from the bytes.
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("not an image"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	ws, err := local.New(local.Config{Root: root, AllowBinaryRead: true, MaxReadBytes: 1 << 20})
	if err != nil {
		t.Fatalf("local.New returned error: %v", err)
	}
	viewer, err := New(Config{Workspace: ws})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	for _, path := range []string{"fake.png", "notes.txt", "../secret.png", "/etc/passwd", "missing.png", "docs"} {
		raw, err := json.Marshal(map[string]any{"path": path})
		if err != nil {
			t.Fatalf("Marshal returned error: %v", err)
		}
		result, err := viewer.Call(context.Background(), raw, tool.Context{})
		if err == nil {
			t.Fatalf("path %q was accepted: %#v", path, result.Structured)
		}
	}
	// No path at all is an argument error, not a crash.
	if _, err := viewer.Call(context.Background(), json.RawMessage(`{}`), tool.Context{}); err == nil {
		t.Fatal("an empty path was accepted")
	}
	// An oversized image is refused rather than sent.
	writePNG(t, root, "big.png")
	small, err := New(Config{Workspace: ws, MaxBytes: 10})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := small.Call(context.Background(), json.RawMessage(`{"path":"big.png"}`), tool.Context{}); err == nil {
		t.Fatal("an oversized image was accepted")
	}
	// A cancelled context stops before reading.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := viewer.Call(cancelled, json.RawMessage(`{"path":"ok.png"}`), tool.Context{}); err == nil {
		t.Fatal("a cancelled call was accepted")
	}
	// Without a workspace the tool cannot be built.
	if _, err := New(Config{}); err == nil {
		t.Fatal("a tool without a workspace was built")
	}
}

func TestDetectMediaTypeSniffsBytes(t *testing.T) {
	root := t.TempDir()
	writePNG(t, root, "x.png")
	raw, err := os.ReadFile(filepath.Join(root, "x.png"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if got := detectMediaType(raw); got != "image/png" {
		t.Fatalf("detectMediaType = %q", got)
	}
	if got := detectMediaType([]byte("RIFF____WEBPVP8 ")); got != "image/webp" {
		t.Fatalf("webp detection = %q", got)
	}
	if got := detectMediaType([]byte("plain text")); got != "" {
		t.Fatalf("text detection = %q", got)
	}
	if got := detectMediaType(nil); got != "" {
		t.Fatalf("empty detection = %q", got)
	}
}

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge/workspace"
)

func TestLocalWorkspaceReadListGrepWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\nTODO: test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	ws, err := New(Config{Root: root, CreateParentDir: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	data, err := ws.Read(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if string(data) != "hello\nTODO: test\n" {
		t.Fatalf("unexpected read: %q", data)
	}

	entries, err := ws.List(context.Background(), ".")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "README.md" {
		t.Fatalf("unexpected list: %#v", entries)
	}

	matches, err := ws.Grep(context.Background(), workspace.GrepQuery{Pattern: "TODO", Path: "."})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("unexpected matches: %#v", matches)
	}

	if err := ws.Write(context.Background(), "notes/out.txt", []byte("ok")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "out.txt")); err != nil {
		t.Fatalf("expected written file: %v", err)
	}
}

func TestLocalWorkspaceBlocksTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	ws, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if _, err := ws.Read(context.Background(), "../secret.txt"); !errors.Is(err, workspace.ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape for traversal, got %v", err)
	}
	if _, err := ws.Read(context.Background(), "link.txt"); !errors.Is(err, workspace.ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape for symlink, got %v", err)
	}
	if err := ws.Write(context.Background(), "link.txt", []byte("changed")); !errors.Is(err, workspace.ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape for symlink write, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("outside file was modified: %q", data)
	}
}

func TestLocalWorkspaceLimitsAndBinaryHandling(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	ws, err := New(Config{Root: root, MaxReadBytes: 3, MaxWriteBytes: 3})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := ws.Read(context.Background(), "big.txt"); !errors.Is(err, workspace.ErrReadTooLarge) {
		t.Fatalf("expected ErrReadTooLarge, got %v", err)
	}
	if err := ws.Write(context.Background(), "out.txt", []byte("abcd")); !errors.Is(err, workspace.ErrWriteTooLarge) {
		t.Fatalf("expected ErrWriteTooLarge, got %v", err)
	}

	ws, err = New(Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := ws.Read(context.Background(), "bin.dat"); !errors.Is(err, workspace.ErrBinaryFile) {
		t.Fatalf("expected ErrBinaryFile, got %v", err)
	}
}

func TestLocalWorkspaceBlocksKnownBinaryExtensionsWithoutNULBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.pdf"), []byte("plain-looking payload"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	ws, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := ws.Read(context.Background(), "report.pdf"); !errors.Is(err, workspace.ErrBinaryFile) {
		t.Fatalf("expected ErrBinaryFile, got %v", err)
	}

	ws, err = New(Config{Root: root, AllowBinaryRead: true})
	if err != nil {
		t.Fatalf("New with binary reads returned error: %v", err)
	}
	data, err := ws.Read(context.Background(), "report.pdf")
	if err != nil || string(data) != "plain-looking payload" {
		t.Fatalf("explicit binary read = %q, %v", data, err)
	}
}

func TestLocalWorkspaceGrepSkipsKnownBinaryExtensionsBeforeContentScan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "archive.zip"), []byte("TODO hidden in archive"), 0o644); err != nil {
		t.Fatalf("WriteFile archive returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("TODO visible"), 0o644); err != nil {
		t.Fatalf("WriteFile text returned error: %v", err)
	}
	ws, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	matches, err := ws.Grep(context.Background(), workspace.GrepQuery{Pattern: "TODO", Path: "."})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}
	if len(matches) != 1 || matches[0].Path != "notes.txt" {
		t.Fatalf("unexpected matches: %#v", matches)
	}
}

func TestLocalWorkspaceBlocksPlatformDeviceFiles(t *testing.T) {
	ws, err := New(Config{Root: string(os.PathSeparator), AllowBinaryRead: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := ws.Read(context.Background(), "dev/null"); !errors.Is(err, workspace.ErrUnsupportedFile) {
		t.Fatalf("expected ErrUnsupportedFile, got %v", err)
	}
}

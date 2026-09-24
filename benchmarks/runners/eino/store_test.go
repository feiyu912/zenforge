package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileCheckPointStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := newFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("newFileCheckPointStore: %v", err)
	}
	ctx := context.Background()

	if b, ok, err := store.Get(ctx, "absent"); err != nil || ok || b != nil {
		t.Fatalf("Get(absent) = (%v, %v, %v), want (nil, false, nil)", b, ok, err)
	}

	want := []byte("checkpoint payload \x00\x01\x02")
	if err := store.Set(ctx, "bench-task", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := store.Get(ctx, "bench-task")
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v, %v), want present", got, ok, err)
	}
	if string(got) != string(want) {
		t.Errorf("Get = %q, want %q", got, want)
	}

	// Overwriting must replace, not append.
	if err := store.Set(ctx, "bench-task", []byte("second")); err != nil {
		t.Fatalf("Set(overwrite): %v", err)
	}
	got, _, _ = store.Get(ctx, "bench-task")
	if string(got) != "second" {
		t.Errorf("after overwrite Get = %q, want %q", got, "second")
	}

	// Two keys must not collide.
	if err := store.Set(ctx, "other", []byte("other payload")); err != nil {
		t.Fatalf("Set(other): %v", err)
	}
	if got, _, _ := store.Get(ctx, "bench-task"); string(got) != "second" {
		t.Errorf("key collision: bench-task = %q", got)
	}

	if err := store.Delete(ctx, "bench-task"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := store.Get(ctx, "bench-task"); ok {
		t.Error("checkpoint still present after Delete")
	}
	if err := store.Delete(ctx, "bench-task"); err != nil {
		t.Errorf("Delete of a missing checkpoint should be a no-op, got %v", err)
	}
}

func TestFileCheckPointStoreDoesNotEscapeStateDir(t *testing.T) {
	dir := t.TempDir()
	store, err := newFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("newFileCheckPointStore: %v", err)
	}
	ctx := context.Background()

	hostile := "../../../../tmp/zenforge-eino-hostile"
	if err := store.Set(ctx, hostile, []byte("x")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("state dir has %d entries, want 1", len(entries))
	}
	if strings.ContainsAny(entries[0].Name(), `/\`) {
		t.Errorf("checkpoint file name %q is not flat", entries[0].Name())
	}
	if _, err := os.Stat("/tmp/zenforge-eino-hostile"); err == nil {
		t.Fatal("a hostile checkpoint id escaped the state directory")
	}
}

func TestResumeStateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if _, err := readResumeState(dir); err == nil {
		t.Error("readResumeState on an empty directory should fail")
	}

	want := resumeState{
		Task:         TaskDurableTask,
		CheckpointID: "zenforge-bench-durable-task",
		InterruptIDs: []string{"agent:zenforge-bench-eino;tool:run_shell#1"},
		Commands:     []string{"touch marker"},
		Framework:    "eino 0.9.21",
	}
	if err := writeResumeState(dir, want); err != nil {
		t.Fatalf("writeResumeState: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, resumeStateFile)); err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	got, err := readResumeState(dir)
	if err != nil {
		t.Fatalf("readResumeState: %v", err)
	}
	if got.Task != want.Task || got.CheckpointID != want.CheckpointID || len(got.InterruptIDs) != 1 ||
		got.InterruptIDs[0] != want.InterruptIDs[0] || got.Commands[0] != want.Commands[0] {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

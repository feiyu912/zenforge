package jsonl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestStoreAppendReadAndLatestSeq(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunDone, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_2", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	events, err := store.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("unexpected event count: got %d want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("unexpected seqs: got %d, %d", events[0].Seq, events[1].Seq)
	}

	latest, err := store.LatestSeq(ctx, "run_1")
	if err != nil {
		t.Fatalf("LatestSeq returned error: %v", err)
	}
	if latest != 2 {
		t.Fatalf("unexpected latest seq: got %d want 2", latest)
	}
}

func TestStoreReadSupportsAfterSeqAndLimit(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())

	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventModelDelta, "run_1", nil)); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	events, err := store.Read(ctx, "run_1", 1, 1)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unexpected event count: got %d want 1", len(events))
	}
	if events[0].Seq != 2 {
		t.Fatalf("unexpected seq: got %d want 2", events[0].Seq)
	}
}

func TestStoreRejectsOutOfOrderSeq(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())

	if err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil).WithSeq(2)); err == nil {
		t.Fatalf("expected out-of-order seq error")
	}
}

func TestStoresSharingRootSerializeConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stores := []*Store{New(root), New(root)}
	const count = 32

	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			errs <- store.Append(ctx, zenforge.NewEvent(zenforge.EventModelDelta, "run_1", nil))
		}(stores[i%len(stores)])
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append returned error: %v", err)
		}
	}

	events, err := stores[0].Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != count {
		t.Fatalf("event count = %d, want %d", len(events), count)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, event.Seq, i+1)
		}
	}
}

func TestStoreRejectsUnsafeRunIDs(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()
	for _, runID := range []string{".", "..", "nested/run", `nested\run`, filepath.Join(string(filepath.Separator), "tmp", "run")} {
		t.Run(fmt.Sprintf("%q", runID), func(t *testing.T) {
			event := zenforge.NewEvent(zenforge.EventModelDelta, runID, nil)
			if err := store.Append(ctx, event); err == nil {
				t.Fatalf("Append accepted unsafe runID %q", runID)
			}
			if _, err := store.Read(ctx, runID, 0, 0); err == nil {
				t.Fatalf("Read accepted unsafe runID %q", runID)
			}
			if _, err := store.LatestSeq(ctx, runID); err == nil {
				t.Fatalf("LatestSeq accepted unsafe runID %q", runID)
			}
		})
	}
}

func TestStoresAcrossProcessesSerializeAppends(t *testing.T) {
	root := t.TempDir()
	const processes = 4
	const perProcess = 12
	type childProcess struct {
		cmd    *exec.Cmd
		output bytes.Buffer
	}
	commands := make([]*childProcess, 0, processes)
	for i := 0; i < processes; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEventlogProcessHelper$")
		cmd.Env = append(os.Environ(),
			"ZENFORGE_EVENTLOG_HELPER=1",
			"ZENFORGE_JSONL_ROOT="+root,
			"ZENFORGE_JSONL_COUNT="+strconv.Itoa(perProcess),
		)
		child := &childProcess{cmd: cmd}
		cmd.Stdout = &child.output
		cmd.Stderr = &child.output
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child: %v", err)
		}
		commands = append(commands, child)
	}
	for _, child := range commands {
		if err := child.cmd.Wait(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, child.output.Bytes())
		}
	}

	want := processes * perProcess
	events, err := New(root).Read(context.Background(), "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != want {
		t.Fatalf("event count = %d, want %d", len(events), want)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, event.Seq, i+1)
		}
	}
}

func TestEventlogProcessHelper(t *testing.T) {
	if os.Getenv("ZENFORGE_EVENTLOG_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	count, err := strconv.Atoi(os.Getenv("ZENFORGE_JSONL_COUNT"))
	if err != nil {
		t.Fatal(err)
	}
	store := New(os.Getenv("ZENFORGE_JSONL_ROOT"))
	for i := 0; i < count; i++ {
		if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventModelDelta, "run_1", nil)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreReturnsErrorForCorruptLine(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	path := filepath.Join(runDir, eventsFileName)
	if err := os.WriteFile(path, []byte("{bad json}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := New(root).Read(ctx, "run_1", 0, 0)
	if err == nil {
		t.Fatalf("expected corrupt line error")
	}
	if !strings.Contains(err.Error(), "parse JSONL") {
		t.Fatalf("expected parse JSONL error, got %v", err)
	}
}

func TestStoreReturnsErrorForNonContiguousSequence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	body := "{\"seq\":1,\"type\":\"run.started\",\"runId\":\"run_1\",\"timestamp\":1}\n" +
		"{\"seq\":1,\"type\":\"run.done\",\"runId\":\"run_1\",\"timestamp\":2}\n"
	if err := os.WriteFile(filepath.Join(runDir, eventsFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := New(root).Read(ctx, "run_1", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "event seq must be 2, got 1") {
		t.Fatalf("expected sequence error, got %v", err)
	}
}

func TestStoreReadsPrettyPrintedEventsLikePlatform(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runDir := filepath.Join(root, "run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	path := filepath.Join(runDir, eventsFileName)
	body := `{
  "seq": 1,
  "type": "run.started",
  "runId": "run_1",
  "timestamp": 1
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	events, err := New(root).Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(events) != 1 || events[0].Type != zenforge.EventRunStarted {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := New(t.TempDir())
	err := store.Append(ctx, zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

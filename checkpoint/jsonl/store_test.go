package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
)

func TestStoreSaveLoadDelete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	cp := testCheckpoint("run_1", 1)

	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	loaded, err := store.Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.RunID != "run_1" || loaded.Seq != 1 {
		t.Fatalf("unexpected checkpoint: %#v", loaded)
	}
	if _, err := os.Stat(filepath.Join(root, "run_1", checkpointsFileName)); err != nil {
		t.Fatalf("expected checkpoints history file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "run_1", latestFileName)); err != nil {
		t.Fatalf("expected latest checkpoint file: %v", err)
	}

	if err := store.Delete(ctx, "run_1"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	_, err = store.Load(ctx, "run_1")
	if !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreLoadMissingReturnsErrNotFound(t *testing.T) {
	_, err := New(t.TempDir()).Load(context.Background(), "missing")
	if !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreLoadUsesLatestWhenHistoryHasCorruptLine(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	cp := testCheckpoint("run_1", 1)
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	history := filepath.Join(root, "run_1", checkpointsFileName)
	file, err := os.OpenFile(history, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile returned error: %v", err)
	}
	if _, err := file.WriteString("{bad json}\n"); err != nil {
		file.Close()
		t.Fatalf("WriteString returned error: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	loaded, err := store.Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Seq != 1 {
		t.Fatalf("unexpected checkpoint: %#v", loaded)
	}
}

func TestStoresSharingRootDoNotRaceLatestFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stores := []*Store{New(root), New(root)}
	const count = 16

	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			errs <- store.Save(ctx, testCheckpoint("run_1", int64(i+1)))
		}(i, stores[i%len(stores)])
	}
	close(start)
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
			continue
		}
		if !errors.Is(err, checkpoint.ErrStaleCheckpoint) {
			t.Fatalf("concurrent Save returned unexpected error: %v", err)
		}
	}

	loaded, err := stores[0].Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Seq != count {
		t.Fatalf("latest checkpoint seq = %d, want %d", loaded.Seq, count)
	}
	data, err := os.ReadFile(filepath.Join(root, "run_1", checkpointsFileName))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != accepted {
		t.Fatalf("history entry count = %d, want accepted count %d", got, accepted)
	}
}

func TestStoreRejectsNonIncreasingSeqWithoutChangingFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	if err := store.Save(ctx, testCheckpoint("run_1", 2)); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	historyPath := filepath.Join(root, "run_1", checkpointsFileName)
	latestPath := filepath.Join(root, "run_1", latestFileName)
	historyBefore, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("ReadFile history returned error: %v", err)
	}
	latestBefore, err := os.ReadFile(latestPath)
	if err != nil {
		t.Fatalf("ReadFile latest returned error: %v", err)
	}

	for _, seq := range []int64{2, 1} {
		if err := store.Save(ctx, testCheckpoint("run_1", seq)); !errors.Is(err, checkpoint.ErrStaleCheckpoint) {
			t.Fatalf("Save seq %d error = %v, want ErrStaleCheckpoint", seq, err)
		}
	}
	historyAfter, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("ReadFile history after rejects returned error: %v", err)
	}
	latestAfter, err := os.ReadFile(latestPath)
	if err != nil {
		t.Fatalf("ReadFile latest after rejects returned error: %v", err)
	}
	if string(historyAfter) != string(historyBefore) {
		t.Fatalf("history changed after rejected saves")
	}
	if string(latestAfter) != string(latestBefore) {
		t.Fatalf("latest changed after rejected saves")
	}
}

func TestStoreRetryAfterLatestFailureDoesNotDuplicateHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	var calls atomic.Int32
	store.writeLatest = func(runDir string, data []byte) error {
		if calls.Add(1) == 1 {
			return errors.New("injected latest failure")
		}
		return atomicWriteFile(runDir, latestFileName, data)
	}
	cp := testCheckpoint("run_1", 1)
	if err := store.Save(ctx, cp); err == nil || !strings.Contains(err.Error(), "injected latest failure") {
		t.Fatalf("first Save error = %v, want injected failure", err)
	}
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("retry Save returned error: %v", err)
	}
	loaded, err := store.Load(ctx, cp.RunID)
	if err != nil || loaded.Seq != cp.Seq {
		t.Fatalf("Load = %#v, %v", loaded, err)
	}
	data, err := os.ReadFile(filepath.Join(root, cp.RunID, checkpointsFileName))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 1 {
		t.Fatalf("history entry count = %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(root, cp.RunID, pendingFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending transaction remains: %v", err)
	}
}

func TestStoreLoadRecoversPendingCheckpointWithoutRetryingSave(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	if err := store.Save(ctx, testCheckpoint("run_1", 1)); err != nil {
		t.Fatalf("Save seq 1 returned error: %v", err)
	}
	store.writeLatest = func(string, []byte) error {
		return errors.New("injected latest failure")
	}
	if err := store.Save(ctx, testCheckpoint("run_1", 2)); err == nil || !strings.Contains(err.Error(), "injected latest failure") {
		t.Fatalf("Save seq 2 error = %v, want injected failure", err)
	}

	loaded, err := New(root).Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load from new Store returned error: %v", err)
	}
	if loaded.Seq != 2 {
		t.Fatalf("loaded seq = %d, want 2", loaded.Seq)
	}
	assertCheckpointHistorySeqs(t, root, "run_1", 1, 2)
	if _, err := os.Stat(filepath.Join(root, "run_1", pendingFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending transaction remains: %v", err)
	}
}

func TestStoreListRecoversPendingCheckpoint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	if err := store.Save(ctx, testCheckpoint("run_1", 1)); err != nil {
		t.Fatalf("Save seq 1 returned error: %v", err)
	}
	store.writeLatest = func(string, []byte) error {
		return errors.New("injected latest failure")
	}
	if err := store.Save(ctx, testCheckpoint("run_1", 2)); err == nil {
		t.Fatal("Save seq 2 unexpectedly succeeded")
	}

	summaries, err := New(root).List(ctx)
	if err != nil {
		t.Fatalf("List from new Store returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Seq != 2 {
		t.Fatalf("summaries = %#v, want recovered seq 2", summaries)
	}
	assertCheckpointHistorySeqs(t, root, "run_1", 1, 2)
}

func TestStoreRejectsUnsafeRunIDs(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()
	for _, runID := range []string{".", "..", "nested/run", `nested\run`, filepath.Join(string(filepath.Separator), "tmp", "run")} {
		t.Run(fmt.Sprintf("%q", runID), func(t *testing.T) {
			if err := store.Save(ctx, testCheckpoint(runID, 1)); err == nil {
				t.Fatalf("Save accepted unsafe runID %q", runID)
			}
			if _, err := store.Load(ctx, runID); err == nil {
				t.Fatalf("Load accepted unsafe runID %q", runID)
			}
			if err := store.Delete(ctx, runID); err == nil {
				t.Fatalf("Delete accepted unsafe runID %q", runID)
			}
		})
	}
}

func TestStoresAcrossProcessesSerializeCheckpointSequences(t *testing.T) {
	root := t.TempDir()
	const processes = 4
	const perProcess = 8
	type childProcess struct {
		cmd    *exec.Cmd
		output bytes.Buffer
	}
	commands := make([]*childProcess, 0, processes)
	for i := 0; i < processes; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointProcessHelper$")
		cmd.Env = append(os.Environ(),
			"ZENFORGE_CHECKPOINT_HELPER=1",
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
	loaded, err := New(root).Load(context.Background(), "run_1")
	if err != nil || loaded.Seq != int64(want) {
		t.Fatalf("latest = %#v, %v; want seq %d", loaded, err, want)
	}
	file, err := os.Open(filepath.Join(root, "run_1", checkpointsFileName))
	if err != nil {
		t.Fatalf("Open history: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var seq int64
	for scanner.Scan() {
		var cp checkpoint.Checkpoint
		if err := json.Unmarshal(scanner.Bytes(), &cp); err != nil {
			t.Fatalf("decode history: %v", err)
		}
		seq++
		if cp.Seq != seq {
			t.Fatalf("history seq = %d, want %d", cp.Seq, seq)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan history: %v", err)
	}
	if seq != int64(want) {
		t.Fatalf("history entries = %d, want %d", seq, want)
	}
}

func TestCheckpointProcessHelper(t *testing.T) {
	if os.Getenv("ZENFORGE_CHECKPOINT_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	count, err := strconv.Atoi(os.Getenv("ZENFORGE_JSONL_COUNT"))
	if err != nil {
		t.Fatal(err)
	}
	store := New(os.Getenv("ZENFORGE_JSONL_ROOT"))
	ctx := context.Background()
	for accepted := 0; accepted < count; {
		latest, err := store.Load(ctx, "run_1")
		next := int64(1)
		if err == nil {
			next = latest.Seq + 1
		} else if !errors.Is(err, checkpoint.ErrNotFound) {
			t.Fatal(err)
		}
		err = store.Save(ctx, testCheckpoint("run_1", next))
		if errors.Is(err, checkpoint.ErrStaleCheckpoint) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
}

func TestStoreListSummaries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := New(root)
	first := testCheckpoint("run_1", 1)
	first.State.Phase = harness.RunPhaseModel
	first.State.Control.Status = harness.RunStatusModelStreaming
	first.State.Step = 1
	first.SavedAt = time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	second := testCheckpoint("run_2", 3)
	second.State.Phase = harness.RunPhaseCompleted
	second.State.Control.Status = harness.RunStatusCompleted
	second.State.Step = 4
	second.SavedAt = time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC)

	if err := store.Save(ctx, first); err != nil {
		t.Fatalf("Save first returned error: %v", err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatalf("Save second returned error: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}

	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("summary count = %d, want 2: %#v", len(got), got)
	}
	if got[0].RunID != "run_2" || got[0].Seq != 3 || got[0].Phase != "completed" || got[0].Status != "COMPLETED" || got[0].Step != 4 {
		t.Fatalf("unexpected first summary: %#v", got[0])
	}
	if got[1].RunID != "run_1" {
		t.Fatalf("unexpected ordering: %#v", got)
	}
}

func TestStoreListMissingRootReturnsEmpty(t *testing.T) {
	got, err := New(filepath.Join(t.TempDir(), "missing")).List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("summary count = %d, want 0", len(got))
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New(t.TempDir()).Save(ctx, testCheckpoint("run_1", 1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func testCheckpoint(runID string, seq int64) checkpoint.Checkpoint {
	now := time.Now().UTC()
	return checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   runID,
		Seq:     seq,
		State: harness.RunState{
			Version:   harness.RunStateVersion,
			RunID:     runID,
			Input:     "hello",
			Phase:     harness.RunPhaseCreated,
			CreatedAt: now,
			UpdatedAt: now,
			Control:   harness.RunControlState{Status: harness.RunStatusIdle},
		},
		SavedAt: now,
	}
}

func assertCheckpointHistorySeqs(t *testing.T, root, runID string, want ...int64) {
	t.Helper()
	file, err := os.Open(filepath.Join(root, runID, checkpointsFileName))
	if err != nil {
		t.Fatalf("Open history: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var got []int64
	for scanner.Scan() {
		var cp checkpoint.Checkpoint
		if err := json.Unmarshal(scanner.Bytes(), &cp); err != nil {
			t.Fatalf("decode history: %v", err)
		}
		got = append(got, cp.Seq)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan history: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("history seqs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history seqs = %v, want %v", got, want)
		}
	}
}

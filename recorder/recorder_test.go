package recorder

import (
	"context"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/checkpoint"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	eventmemory "github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/harness"
)

func TestRecorderSavesCheckpointBeforeCheckpointEvent(t *testing.T) {
	ctx := context.Background()
	checkpoints := &recordingCheckpointStore{Store: checkpointmemory.New()}
	events := eventmemory.New()
	recorder := Recorder{Events: events, Checkpoints: checkpoints}

	if _, err := recorder.RecordCheckpoint(ctx, testRunState("run_1", harness.RunPhaseModel), 3); err != nil {
		t.Fatalf("RecordCheckpoint returned error: %v", err)
	}
	if checkpoints.saved != 1 {
		t.Fatalf("expected checkpoint save before event append")
	}
	recorded, err := events.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(recorded) != 1 || recorded[0].Type != zenforge.EventCheckpointCreated {
		t.Fatalf("unexpected events: %#v", recorded)
	}
}

func TestRecorderCompleteWritesTerminalEventAfterCheckpointEvent(t *testing.T) {
	ctx := context.Background()
	events := eventmemory.New()
	recorder := Recorder{Events: events, Checkpoints: checkpointmemory.New()}

	state := testRunState("run_1", harness.RunPhaseCompleted)
	terminal := zenforge.NewEvent(zenforge.EventRunDone, "run_1", map[string]any{"output": "ok"})
	if _, err := recorder.Complete(ctx, state, 5, terminal); err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	recorded, err := events.Read(ctx, "run_1", 0, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(recorded) != 2 {
		t.Fatalf("unexpected event count: got %d want 2", len(recorded))
	}
	if recorded[0].Type != zenforge.EventCheckpointCreated || recorded[1].Type != zenforge.EventRunDone {
		t.Fatalf("unexpected event order: %#v", recorded)
	}
}

type recordingCheckpointStore struct {
	*checkpointmemory.Store
	saved int
}

func (s *recordingCheckpointStore) Save(ctx context.Context, cp checkpoint.Checkpoint) error {
	s.saved++
	return s.Store.Save(ctx, cp)
}

func testRunState(runID string, phase harness.RunPhase) harness.RunState {
	now := time.Now().UTC()
	return harness.RunState{
		Version:   harness.RunStateVersion,
		RunID:     runID,
		Input:     "hello",
		Phase:     phase,
		CreatedAt: now,
		UpdatedAt: now,
		Control:   harness.RunControlState{Status: harness.RunStatusModelStreaming},
	}
}

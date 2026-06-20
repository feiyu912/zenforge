package recorder

import (
	"context"
	"encoding/json"
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
	if got := recorded[0].Payload["checkpointSeq"]; got != float64(3) && got != int64(3) {
		t.Fatalf("checkpointSeq = %#v, want 3", got)
	}
	encoded, err := json.Marshal(recorded[0])
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if got := persisted["checkpointSeq"]; got != float64(3) {
		t.Fatalf("persisted checkpointSeq = %#v, want 3", got)
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

func TestRecorderCompleteValidatesTerminalEventBeforeCheckpoint(t *testing.T) {
	ctx := context.Background()
	checkpoints := &recordingCheckpointStore{Store: checkpointmemory.New()}
	recorder := Recorder{Events: eventmemory.New(), Checkpoints: checkpoints}
	state := testRunState("run_1", harness.RunPhaseCompleted)

	tests := []struct {
		name  string
		event zenforge.Event
	}{
		{name: "non-terminal", event: zenforge.NewEvent(zenforge.EventModelDone, "run_1", nil)},
		{name: "wrong run", event: zenforge.NewEvent(zenforge.EventRunDone, "run_2", nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := recorder.Complete(ctx, state, 1, test.event); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
	if checkpoints.saved != 0 {
		t.Fatalf("saved checkpoints = %d, want 0", checkpoints.saved)
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

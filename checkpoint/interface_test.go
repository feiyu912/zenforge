package checkpoint

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/harness"
)

func TestCheckpointJSONRoundTripAndValidate(t *testing.T) {
	checkpoint := Checkpoint{
		Version: CheckpointVersion,
		RunID:   "run_1",
		Seq:     7,
		State: harness.RunState{
			Version:   harness.RunStateVersion,
			RunID:     "run_1",
			Input:     "hello",
			Phase:     harness.RunPhaseModel,
			Control:   harness.RunControlState{Status: harness.RunStatusModelStreaming},
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		},
		SavedAt: time.Now().UTC(),
	}
	if err := Validate(checkpoint); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	data, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var got Checkpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if err := Validate(got); err != nil {
		t.Fatalf("Validate after round trip returned error: %v", err)
	}
}

func TestValidateRejectsInvalidCheckpoint(t *testing.T) {
	if err := Validate(Checkpoint{}); err == nil {
		t.Fatalf("expected validation error")
	}
}

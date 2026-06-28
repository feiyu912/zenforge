package checkpoint

import (
	"encoding/json"
	"strings"
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

func TestValidateRejectsUnsupportedRunStateDispatchFields(t *testing.T) {
	base := validCheckpoint()
	tests := []struct {
		name   string
		mutate func(*Checkpoint)
		want   string
	}{
		{name: "version", mutate: func(cp *Checkpoint) { cp.State.Version = "zenforge.run_state.v2" }, want: "unsupported run state version"},
		{name: "phase", mutate: func(cp *Checkpoint) { cp.State.Phase = "future" }, want: "unsupported run phase"},
		{name: "mode", mutate: func(cp *Checkpoint) { cp.State.Mode = "future" }, want: "unsupported run mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cp := base
			test.mutate(&cp)
			err := ValidateForLoad(cp)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateAcceptsLegacyRunStateVersionAndMode(t *testing.T) {
	cp := validCheckpoint()
	cp.State.Version = ""
	cp.State.Mode = ""
	if err := ValidateForLoad(cp); err != nil {
		t.Fatalf("ValidateForLoad(legacy checkpoint) returned error: %v", err)
	}
}

func validCheckpoint() Checkpoint {
	now := time.Now().UTC()
	return Checkpoint{
		Version: CheckpointVersion,
		RunID:   "run_1",
		Seq:     1,
		State: harness.RunState{
			Version: harness.RunStateVersion,
			RunID:   "run_1",
			Phase:   harness.RunPhaseCreated,
		},
		SavedAt: now,
	}
}

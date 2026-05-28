package zenforge

import (
	"encoding/json"
	"testing"
)

func TestEventValidateRequiresCoreFields(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{
			name:  "missing run id",
			event: Event{Type: EventRunStarted, Timestamp: 1},
		},
		{
			name:  "missing type",
			event: Event{Payload: EventData{"runId": "run_1"}, Timestamp: 1},
		},
		{
			name:  "missing timestamp",
			event: Event{Payload: EventData{"runId": "run_1"}, Type: EventRunStarted},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.event.Validate(); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestEventValidateAcceptsNewEvent(t *testing.T) {
	event := NewEvent(EventRunStarted, "run_1", map[string]any{"input": "hello"})
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if event.Seq != 0 {
		t.Fatalf("new transient event should not have seq: got %d", event.Seq)
	}
}

func TestEventJSONMatchesPlatformFlattenedShape(t *testing.T) {
	event := NewEvent(EventRunStarted, "run_1", map[string]any{"input": "hello"}).WithSeq(7)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if raw["runId"] != "run_1" || raw["input"] != "hello" || raw["type"] != string(EventRunStarted) {
		t.Fatalf("unexpected flattened event: %s", data)
	}
	if _, ok := raw["payload"]; ok {
		t.Fatalf("event should not contain payload wrapper: %s", data)
	}
	if _, ok := raw["data"]; ok {
		t.Fatalf("event should not contain data wrapper: %s", data)
	}
}

func TestPersistedEventRequiresSeq(t *testing.T) {
	event := NewEvent(EventRunStarted, "run_1", nil)
	if err := event.ValidatePersisted(); err == nil {
		t.Fatalf("expected missing seq validation error")
	}

	persisted := event.WithSeq(NextEventSeq(41))
	if err := persisted.ValidatePersisted(); err != nil {
		t.Fatalf("ValidatePersisted returned error: %v", err)
	}
	if persisted.Seq != 42 {
		t.Fatalf("unexpected seq: got %d want 42", persisted.Seq)
	}
}

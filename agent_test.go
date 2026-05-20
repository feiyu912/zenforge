package zenforge

import (
	"context"
	"testing"
)

func TestAgentStreamEmitsLifecycleEvents(t *testing.T) {
	agent := New(Config{})
	events, err := agent.Stream(context.Background(), Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
		if event.RunID == "" {
			t.Fatalf("expected run id on event %#v", event)
		}
	}
	want := []EventType{EventRunStarted, EventRunDone}
	if len(types) != len(want) {
		t.Fatalf("unexpected event count: got %v want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("unexpected event types: got %v want %v", types, want)
		}
	}
}


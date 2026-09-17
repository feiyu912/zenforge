package zenforge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/feiyu912/zenforge/checkpoint"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

// interruptedCheckpointStore reports a failure for one save that did land,
// which is what a store does when a deadline expires inside the window where
// the checkpoint has a durable intent but the save has not reported success.
// The wrapper exists because the real window is a timing race; the shape it
// produces is deterministic.
type interruptedCheckpointStore struct {
	inner    checkpoint.Store
	injected error

	mu       sync.Mutex
	reported bool
}

func (s *interruptedCheckpointStore) Save(ctx context.Context, cp checkpoint.Checkpoint) error {
	if err := s.inner.Save(ctx, cp); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reported {
		return nil
	}
	s.reported = true
	return s.injected
}

func (s *interruptedCheckpointStore) Load(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	return s.inner.Load(ctx, runID)
}

func (s *interruptedCheckpointStore) Delete(ctx context.Context, runID string) error {
	return s.inner.Delete(ctx, runID)
}

// TestAgentCheckpointSaveFailureIsNotMaskedByASequenceCollision pins the
// contract between the loop's in-memory checkpoint counter and a store whose
// save can fail after it landed: the run must report the failure it actually
// met, and the terminal checkpoint must still be written. Trusting the stale
// counter instead makes the terminal save collide with the checkpoint that is
// already there, and the collision error replaces the real reason, which is
// how a cancelled run ends up reporting a checkpoint conflict.
func TestAgentCheckpointSaveFailureIsNotMaskedByASequenceCollision(t *testing.T) {
	cases := []struct {
		name          string
		injected      error
		wantCancelled bool
	}{
		{
			name:          "a context error is reported as the cancellation it means",
			injected:      context.DeadlineExceeded,
			wantCancelled: true,
		},
		{
			name:     "a store error is reported with its own message",
			injected: errors.New("injected checkpoint failure"),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			const runID = "run_interrupted_checkpoint_save"
			inner := checkpointmemory.New()
			store := &interruptedCheckpointStore{inner: inner, injected: testCase.injected}
			agent := New(Config{
				Model:       answerModel{},
				Events:      &testEventStore{},
				Checkpoints: store,
			})

			stream, err := agent.Stream(context.Background(), Task{RunID: runID, Input: "do the work"})
			if err != nil {
				t.Fatalf("Stream returned error: %v", err)
			}
			var types []EventType
			var failure string
			for event := range stream {
				types = append(types, event.Type)
				if event.Type == EventRunError {
					failure, _ = event.Payload["error"].(string)
				}
			}
			if strings.Contains(failure, "checkpoint sequence must increase") {
				t.Fatalf("the real failure was masked by a sequence collision: %q (%v)", failure, types)
			}
			if testCase.wantCancelled {
				assertContainsEvent(t, types, EventRunCancelled)
				for _, eventType := range types {
					if eventType == EventRunError {
						t.Fatalf("an interrupted save reported run.error: %v (%s)", types, failure)
					}
				}
			} else {
				assertContainsEvent(t, types, EventRunError)
				if !strings.Contains(failure, testCase.injected.Error()) {
					t.Fatalf("failure = %q, want it to contain %q", failure, testCase.injected.Error())
				}
			}
			// The terminal checkpoint is durable and unique: the counter
			// caught up with the save that landed before it.
			latest, loadErr := inner.Load(context.Background(), runID)
			if loadErr != nil {
				t.Fatalf("Load returned error: %v", loadErr)
			}
			if latest.Seq != 2 {
				t.Fatalf("latest checkpoint seq = %d, want 2", latest.Seq)
			}
			if testCase.wantCancelled {
				if latest.State.Phase != harness.RunPhaseCancelled {
					t.Fatalf("terminal checkpoint phase = %q", latest.State.Phase)
				}
			} else if latest.State.Phase != harness.RunPhaseFailed {
				t.Fatalf("terminal checkpoint phase = %q", latest.State.Phase)
			}
		})
	}
}

// answerModel is a model that answers one turn with fixed text.
type answerModel struct{}

func (answerModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	return &model.Response{Message: model.Message{Role: "assistant", Content: "done"}}, ctx.Err()
}

func (answerModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	events := make(chan model.Event, 2)
	go func() {
		defer close(events)
		if err := ctx.Err(); err != nil {
			events <- model.Event{Type: model.EventError, Error: err}
			return
		}
		events <- model.Event{Type: model.EventDelta, Delta: "done"}
		events <- model.Event{Type: model.EventDone, Message: &model.Message{Role: "assistant", Content: "done"}}
	}()
	return events, nil
}

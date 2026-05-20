package zenforge

import (
	"context"
	"fmt"
	"time"
)

// Agent is the high-level batteries-included runtime entrypoint.
type Agent struct {
	config Config
}

// New creates an Agent with the provided runtime configuration.
func New(config Config) *Agent {
	return &Agent{config: config}
}

// Run executes a task and returns the final result.
func (a *Agent) Run(ctx context.Context, task Task) (*Result, error) {
	events, err := a.Stream(ctx, task)
	if err != nil {
		return nil, err
	}
	var result Result
	for event := range events {
		if event.Type == EventRunDone {
			result.RunID = event.RunID
			result.Output = stringValue(event.Data["output"])
		}
		if event.Type == EventRunError {
			result.RunID = event.RunID
			return &result, fmt.Errorf("%s", stringValue(event.Data["error"]))
		}
	}
	return &result, nil
}

// Stream executes a task and returns a stream of runtime events.
//
// S0 intentionally keeps this as a minimal no-op runtime. S1 will replace the
// body with the real harness loop extracted from agent-platform.
func (a *Agent) Stream(ctx context.Context, task Task) (<-chan Event, error) {
	runID := task.RunID
	if runID == "" {
		runID = newRunID()
	}
	events := make(chan Event, 2)
	go func() {
		defer close(events)
		select {
		case <-ctx.Done():
			events <- NewEvent(EventRunError, runID, map[string]any{"error": ctx.Err().Error()})
			return
		default:
		}
		events <- NewEvent(EventRunStarted, runID, map[string]any{"input": task.Input})
		events <- NewEvent(EventRunDone, runID, map[string]any{"output": ""})
	}()
	return events, nil
}

// Resume resumes a run from the configured checkpoint store.
func (a *Agent) Resume(ctx context.Context, runID string) (<-chan Event, error) {
	if a.config.Checkpoints == nil {
		return nil, fmt.Errorf("checkpoint store is not configured")
	}
	checkpoint, err := a.config.Checkpoints.Load(ctx, runID)
	if err != nil {
		return nil, err
	}
	return a.Stream(ctx, Task{RunID: checkpoint.RunID, Input: checkpoint.Input})
}

func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}


package harness

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

// stopHookRunner builds a runner whose model always answers with text, so
// every turn asks the Stop hook whether the run may finish.
func stopHookRunner(t *testing.T, calls *[]string, hook func(context.Context, *RunState, string) (StopDecision, error)) (Runner, *[]RuntimeEvent) {
	t.Helper()
	events := &[]RuntimeEvent{}
	return Runner{
		MaxSteps: 4,
		Emit: func(eventType RuntimeEvent, _ map[string]any) error {
			*events = append(*events, eventType)
			return nil
		},
		Checkpoint: func(context.Context, RunState) error { return nil },
		CallModel: func(_ context.Context, _ RunState, _ model.ToolChoice) (MessageState, model.Usage, error) {
			*calls = append(*calls, "model")
			return MessageState{Role: "assistant", Content: "answer"}, model.Usage{}, nil
		},
		StopHook: hook,
	}, events
}

func TestStopHookRefusalSendsTheAgentBackToWork(t *testing.T) {
	var calls []string
	refusals := 0
	runner, events := stopHookRunner(t, &calls, func(_ context.Context, state *RunState, output string) (StopDecision, error) {
		if output != "answer" {
			t.Fatalf("hook saw output %q", output)
		}
		refusals++
		if refusals == 1 {
			return StopDecision{AllowStop: false, Reason: "tests have not run"}, nil
		}
		return StopDecision{AllowStop: true}, nil
	})
	var observed []RunState
	runner.Checkpoint = func(_ context.Context, state RunState) error {
		observed = append(observed, state)
		return nil
	}
	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunDone || terminal.Data["output"] != "answer" {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(calls))
	}
	// The hook's reason became the newest user instruction and the run
	// checkpointed it, so a resume sees the same instruction.
	last := observed[len(observed)-1]
	found := false
	for _, message := range last.Messages {
		if message.Role == "user" && message.Content == "tests have not run" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the hook reason was not recorded: %#v", last.Messages)
	}
	if !containsEvent(*events, RuntimeStopBlocked) {
		t.Fatalf("events = %#v", *events)
	}
}

func TestStopHookRefusalsAreBounded(t *testing.T) {
	var calls []string
	runner, events := stopHookRunner(t, &calls, func(context.Context, *RunState, string) (StopDecision, error) {
		return StopDecision{AllowStop: false, Reason: "never good enough"}, nil
	})
	runner.MaxStopHookRetries = 2
	terminal := runner.Run(context.Background(), testRunState(), false)
	// The run still finishes: a hook cannot hold the agent hostage.
	if terminal.Type != RuntimeRunDone {
		t.Fatalf("terminal = %#v", terminal)
	}
	blocked := 0
	for _, eventType := range *events {
		if eventType == RuntimeStopBlocked {
			blocked++
		}
	}
	if blocked != 2 {
		t.Fatalf("stop.blocked events = %d, want 2", blocked)
	}
	if len(calls) != 3 {
		t.Fatalf("model calls = %d, want 3 (initial plus two retries)", len(calls))
	}
}

func TestStopHookErrorsFailTheRun(t *testing.T) {
	var calls []string
	runner, _ := stopHookRunner(t, &calls, func(context.Context, *RunState, string) (StopDecision, error) {
		return StopDecision{}, stopHookError("stop hook boom")
	})
	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunError {
		t.Fatalf("terminal = %#v", terminal)
	}
	if message, _ := terminal.Data["error"].(string); !strings.Contains(message, "boom") {
		t.Fatalf("terminal = %#v", terminal)
	}
}

func TestStopHookReasonDefaultsWhenTheHookGivesNone(t *testing.T) {
	var calls []string
	var seen []RunState
	refusals := 0
	runner, _ := stopHookRunner(t, &calls, func(context.Context, *RunState, string) (StopDecision, error) {
		refusals++
		if refusals == 1 {
			return StopDecision{AllowStop: false}, nil
		}
		return StopDecision{AllowStop: true}, nil
	})
	runner.Checkpoint = func(_ context.Context, state RunState) error {
		seen = append(seen, state)
		return nil
	}
	runner.MaxStopHookRetries = 1
	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunDone {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(calls) != 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	last := seen[len(seen)-1]
	found := false
	for _, message := range last.Messages {
		if strings.Contains(message.Content, "gave no reason") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a refusal without a reason was not explained: %#v", last.Messages)
	}
}

func TestStopHookRefusalAtTheToolLimit(t *testing.T) {
	// With MaxSteps 1 the run falls out of the loop into the final-answer
	// path; the Stop hook must be consulted there too.
	var calls []string
	refusals := 0
	runner := Runner{
		MaxSteps: 1,
		Emit:     func(RuntimeEvent, map[string]any) error { return nil },
		Checkpoint: func(context.Context, RunState) error {
			return nil
		},
		CallModel: func(_ context.Context, _ RunState, choice model.ToolChoice) (MessageState, model.Usage, error) {
			calls = append(calls, string(choice))
			if choice == model.ToolChoiceAuto {
				// A tool call every turn means the step limit, not a text
				// answer, ends the loop.
				return MessageState{
					Role:      "assistant",
					ToolCalls: []ToolCallSpec{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
				}, model.Usage{}, nil
			}
			return MessageState{Role: "assistant", Content: "final"}, model.Usage{}, nil
		},
		RunPendingTools: func(_ context.Context, state *RunState) error {
			state.Messages = append(state.Messages, MessageState{
				Role: "tool", ToolCallID: state.Tool.Pending[0].ID, Content: "found",
			})
			state.Tool.Pending = nil
			return nil
		},
		StopHook: func(context.Context, *RunState, string) (StopDecision, error) {
			refusals++
			// The hook first runs at the tool-use limit; refuse that answer
			// once and allow the one after it.
			if refusals == 1 {
				return StopDecision{AllowStop: false, Reason: "still not done"}, nil
			}
			return StopDecision{AllowStop: true}, nil
		},
	}
	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunDone {
		t.Fatalf("terminal = %#v", terminal)
	}
	// One auto turn (Step 1), the tool-limit final turn, then one more
	// final turn after the refusal.
	want := []string{"auto", "none", "none"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("choices = %#v, want %#v", calls, want)
	}
}

func containsEvent(events []RuntimeEvent, wanted RuntimeEvent) bool {
	for _, eventType := range events {
		if eventType == wanted {
			return true
		}
	}
	return false
}

type stopHookError string

func (e stopHookError) Error() string { return string(e) }

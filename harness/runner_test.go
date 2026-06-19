package harness

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

func TestRunnerCompletesTextOnlyRun(t *testing.T) {
	var events []RuntimeEvent
	var checkpoints []RunState
	runner := Runner{
		MaxSteps: 3,
		Emit: func(eventType RuntimeEvent, _ map[string]any) error {
			events = append(events, eventType)
			return nil
		},
		Checkpoint: func(_ context.Context, state RunState) error {
			checkpoints = append(checkpoints, state)
			return nil
		},
		CallModel: func(_ context.Context, _ RunState, choice model.ToolChoice) (MessageState, model.Usage, error) {
			if choice != model.ToolChoiceAuto {
				t.Fatalf("choice = %q, want auto", choice)
			}
			return MessageState{Role: "assistant", Content: "done"}, model.Usage{
				PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5,
			}, nil
		},
	}

	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunDone || terminal.Data["output"] != "done" || terminal.Err != nil {
		t.Fatalf("unexpected terminal: %#v", terminal)
	}
	wantEvents := []RuntimeEvent{
		RuntimeRunStarted,
		RuntimeStepStarted,
		RuntimeModelStarted,
		RuntimeModelDone,
		RuntimeRunDone,
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	if len(checkpoints) != 3 {
		t.Fatalf("checkpoint count = %d, want 3", len(checkpoints))
	}
	last := checkpoints[len(checkpoints)-1]
	if last.Phase != RunPhaseCompleted || last.Control.Status != RunStatusCompleted {
		t.Fatalf("unexpected terminal checkpoint: %#v", last)
	}
	if last.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v, want 5 total tokens", last.Usage)
	}
}

func TestRunnerExecutesPendingToolsBeforeNextModelTurn(t *testing.T) {
	modelCalls := 0
	toolCalls := 0
	runner := Runner{
		MaxSteps: 3,
		CallModel: func(_ context.Context, state RunState, _ model.ToolChoice) (MessageState, model.Usage, error) {
			modelCalls++
			if modelCalls == 1 {
				return MessageState{
					Role: "assistant",
					ToolCalls: []ToolCallSpec{{
						ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"zen"}`),
					}},
				}, model.Usage{}, nil
			}
			if len(state.Messages) != 3 || state.Messages[2].Role != "tool" {
				t.Fatalf("second model call did not receive tool result: %#v", state.Messages)
			}
			return MessageState{Role: "assistant", Content: "result used"}, model.Usage{}, nil
		},
		RunPendingTools: func(_ context.Context, state *RunState) error {
			toolCalls++
			if len(state.Tool.Pending) != 1 || state.Tool.Pending[0].Name != "lookup" {
				t.Fatalf("unexpected pending tools: %#v", state.Tool.Pending)
			}
			state.Messages = append(state.Messages, MessageState{
				Role: "tool", ToolCallID: state.Tool.Pending[0].ID, Content: "found",
			})
			state.Tool.Pending = nil
			return nil
		},
	}

	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunDone || terminal.Data["output"] != "result used" {
		t.Fatalf("unexpected terminal: %#v", terminal)
	}
	if modelCalls != 2 || toolCalls != 1 {
		t.Fatalf("model calls = %d, tool calls = %d; want 2 and 1", modelCalls, toolCalls)
	}
}

func TestRunnerOneshotCapsAutoTurnsAndUsesFinalNoToolTurn(t *testing.T) {
	var choices []model.ToolChoice
	runner := Runner{
		MaxSteps: 10,
		Mode:     "oneshot",
		CallModel: func(_ context.Context, _ RunState, choice model.ToolChoice) (MessageState, model.Usage, error) {
			choices = append(choices, choice)
			if choice == model.ToolChoiceNone {
				return MessageState{Role: "assistant", Content: "final"}, model.Usage{}, nil
			}
			return MessageState{
				Role:      "assistant",
				ToolCalls: []ToolCallSpec{{ID: "call", Name: "work"}},
			}, model.Usage{}, nil
		},
		RunPendingTools: func(_ context.Context, state *RunState) error {
			state.Tool.Pending = nil
			return nil
		},
	}

	terminal := runner.Run(context.Background(), testRunState(), false)
	if terminal.Type != RuntimeRunDone || terminal.Data["output"] != "final" {
		t.Fatalf("unexpected terminal: %#v", terminal)
	}
	want := []model.ToolChoice{model.ToolChoiceAuto, model.ToolChoiceAuto, model.ToolChoiceNone}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("tool choices = %#v, want %#v", choices, want)
	}
}

func testRunState() RunState {
	return RunState{
		Version:  RunStateVersion,
		RunID:    "run_test",
		Input:    "test",
		Phase:    RunPhaseCreated,
		Messages: []MessageState{{Role: "user", Content: "test"}},
		Control:  RunControlState{Status: RunStatusRunning},
	}
}

package harness

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

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

func TestRunnerResumeFinalizingReplacesAttemptWithNoToolChoice(t *testing.T) {
	now := time.Now().UTC()
	state := testRunState()
	state.Step = 1
	state.Phase = RunPhaseFinalizing
	state.Control.Status = RunStatusModelStreaming
	state.Messages = append(state.Messages, MessageState{
		Role:    "user",
		Content: "You have reached the tool-use limit. Provide the best final answer using the available context.",
	})
	state.Model.Active = &ModelAttempt{
		ID: "attempt_old", LogicalStep: 1, Status: ModelAttemptStreaming,
		TextDraft: "partial", StartedAt: now,
	}
	var choices []model.ToolChoice
	var finalMessageCount int
	runner := Runner{
		MaxSteps: 1,
		Checkpoint: func(_ context.Context, _ RunState) error {
			return nil
		},
		DurableCallModel: func(_ context.Context, current *RunState, choice model.ToolChoice) (MessageState, model.Usage, error) {
			choices = append(choices, choice)
			for _, message := range current.Messages {
				if message.Content == "You have reached the tool-use limit. Provide the best final answer using the available context." {
					finalMessageCount++
				}
			}
			if current.Model.Active == nil || current.Model.Active.Status != ModelAttemptSuperseded {
				t.Fatalf("active attempt was not superseded: %#v", current.Model.Active)
			}
			current.Model.Attempts = append(current.Model.Attempts, *current.Model.Active)
			current.Model.Active = &ModelAttempt{
				ID: "attempt_new", LogicalStep: current.Step, Status: ModelAttemptStarted,
				StartedAt: now.Add(time.Second), ReplacesID: "attempt_old",
			}
			return MessageState{Role: "assistant", Content: "final answer"}, model.Usage{}, nil
		},
	}

	terminal := runner.Run(context.Background(), state, true)
	if terminal.Type != RuntimeRunDone || terminal.Data["output"] != "final answer" {
		t.Fatalf("unexpected terminal: %#v", terminal)
	}
	if len(choices) != 1 || choices[0] != model.ToolChoiceNone {
		t.Fatalf("model choices = %v, want one no-tool call", choices)
	}
	if finalMessageCount != 1 {
		t.Fatalf("final instruction count = %d, want 1", finalMessageCount)
	}
}

func TestRunnerResumeInterruptedAttemptDoesNotSpendAnotherStep(t *testing.T) {
	now := time.Now().UTC()
	state := testRunState()
	state.Step = 1
	state.Phase = RunPhaseModel
	state.Control.Status = RunStatusModelStreaming
	state.Model.Active = &ModelAttempt{
		ID: "attempt_old", LogicalStep: 1, Status: ModelAttemptInterrupted,
		StartedAt: now, CompletedAt: &now,
	}
	var calledStep int
	runner := Runner{
		MaxSteps: 1,
		Checkpoint: func(_ context.Context, _ RunState) error {
			return nil
		},
		DurableCallModel: func(_ context.Context, current *RunState, choice model.ToolChoice) (MessageState, model.Usage, error) {
			calledStep = current.Step
			if choice != model.ToolChoiceAuto || current.Model.Active.Status != ModelAttemptSuperseded {
				t.Fatalf("unexpected replacement call: choice=%q active=%#v", choice, current.Model.Active)
			}
			current.Model.Attempts = append(current.Model.Attempts, *current.Model.Active)
			current.Model.Active = &ModelAttempt{
				ID: "attempt_new", LogicalStep: current.Step, Status: ModelAttemptStarted,
				StartedAt: now.Add(time.Second), ReplacesID: "attempt_old",
			}
			return MessageState{Role: "assistant", Content: "replacement"}, model.Usage{}, nil
		},
	}

	terminal := runner.Run(context.Background(), state, true)
	if terminal.Type != RuntimeRunDone || calledStep != 1 {
		t.Fatalf("replacement spent another step: terminal=%#v calledStep=%d", terminal, calledStep)
	}
}

func TestRunnerResumeCompletesCommittedTextBoundaryWithoutModelCall(t *testing.T) {
	state := testRunState()
	state.Step = 1
	state.Phase = RunPhaseModel
	state.Control.Status = RunStatusRunning
	state.Messages = append(state.Messages, MessageState{Role: "assistant", Content: "already durable"})
	modelCalls := 0
	var checkpoint RunState
	runner := Runner{
		Checkpoint: func(_ context.Context, current RunState) error {
			checkpoint = current
			return nil
		},
		DurableCallModel: func(_ context.Context, _ *RunState, _ model.ToolChoice) (MessageState, model.Usage, error) {
			modelCalls++
			return MessageState{}, model.Usage{}, nil
		},
	}

	terminal := runner.Run(context.Background(), state, true)
	if terminal.Type != RuntimeRunDone || terminal.Data["output"] != "already durable" {
		t.Fatalf("unexpected terminal: %#v", terminal)
	}
	if modelCalls != 0 {
		t.Fatalf("model calls = %d, want 0", modelCalls)
	}
	if checkpoint.Phase != RunPhaseCompleted || checkpoint.Control.Status != RunStatusCompleted {
		t.Fatalf("unexpected completion checkpoint: %#v", checkpoint)
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

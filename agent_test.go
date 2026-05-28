package zenforge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
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
		if event.RunID() == "" {
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

func TestAgentRunReturnsModelText(t *testing.T) {
	agent := New(Config{
		Model: &scriptedModel{turns: []scriptedTurn{
			{events: []model.Event{{Delta: "hello "}, {Delta: "world"}}},
		}},
		Checkpoints: checkpointmemory.New(),
	})

	result, err := agent.Run(context.Background(), Task{Input: "say hi"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output != "hello world" {
		t.Fatalf("unexpected output: got %q", result.Output)
	}
}

func TestAgentStreamRunsToolAndContinuesModelLoop(t *testing.T) {
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "call_1",
						Name:      "echo",
						Arguments: json.RawMessage(`{"text":"from tool"}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "final answer"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{echoTool{}},
		Checkpoints: checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_tool", Input: "use echo"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
	}
	assertContainsEvent(t, types, EventToolCall)
	assertContainsEvent(t, types, EventToolResult)
	if types[len(types)-1] != EventRunDone {
		t.Fatalf("last event = %s, want %s; all events: %v", types[len(types)-1], EventRunDone, types)
	}
	if len(fakeModel.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(fakeModel.requests))
	}
	second := fakeModel.requests[1]
	if second.Messages[len(second.Messages)-1].Role != "tool" || second.Messages[len(second.Messages)-1].Content != "from tool" {
		t.Fatalf("tool result was not fed back to model: %#v", second.Messages)
	}

	cp, err := checkpoints.Load(context.Background(), "run_tool")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if cp.State.Phase != "completed" {
		t.Fatalf("checkpoint phase = %s, want completed", cp.State.Phase)
	}
}

func TestAgentPlanningAddsTodoToolsAndCheckpointsTodos(t *testing.T) {
	checkpoints := checkpointmemory.New()
	model := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "call_1",
						Name:      "todo_write",
						Arguments: json.RawMessage(`{"todos":[{"content":"Inspect repo"}]}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "planned"}}},
	}}
	agent := New(Config{
		Model:       model,
		Planning:    PlanningEnabled,
		Checkpoints: checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_plan", Input: "make a plan"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
	}
	assertContainsEvent(t, types, EventTodoUpdated)
	assertContainsEvent(t, types, EventRunDone)

	cp, err := checkpoints.Load(context.Background(), "run_plan")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if len(cp.State.Todos) != 1 || cp.State.Todos[0].Content != "Inspect repo" {
		t.Fatalf("expected checkpointed todos, got %#v", cp.State.Todos)
	}
}

func TestAgentRecordsApprovalRequiredToolResult(t *testing.T) {
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "call_approval",
						Name:      "needs_approval",
						Arguments: json.RawMessage(`{}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "final after approval request"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{approvalTool{}},
		Checkpoints: checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_approval", Input: "try risky thing"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
	}
	assertContainsEvent(t, types, EventApprovalRequested)
	assertContainsEvent(t, types, EventToolError)

	cp, err := checkpoints.Load(context.Background(), "run_approval")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if cp.State.Approval.Waiting == nil {
		t.Fatalf("expected checkpointed waiting approval, got %#v", cp.State.Approval)
	}
	if cp.State.Approval.Waiting.ToolCallID != "call_approval" {
		t.Fatalf("approval tool call id = %q", cp.State.Approval.Waiting.ToolCallID)
	}
}

func TestAgentApprovalBrokerApprovesAndRetriesTool(t *testing.T) {
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "call_approval",
						Name:      "needs_approval",
						Arguments: json.RawMessage(`{}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{approvalTool{}},
		Approval:    approval.AlwaysAllow(),
		Checkpoints: checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_approval_approved", Input: "try approved thing"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
	}
	assertContainsEvent(t, types, EventApprovalRequested)
	assertContainsEvent(t, types, EventApprovalResolved)
	assertContainsEvent(t, types, EventToolResult)

	cp, err := checkpoints.Load(context.Background(), "run_approval_approved")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if cp.State.Approval.Waiting != nil || len(cp.State.Approval.Resolved) != 1 {
		t.Fatalf("expected resolved approval, got %#v", cp.State.Approval)
	}
	if got := fakeModel.requests[1].Messages[len(fakeModel.requests[1].Messages)-1].Content; got != "approved result" {
		t.Fatalf("tool content = %q", got)
	}
}

func TestAgentApprovalTimeoutEmitsExpired(t *testing.T) {
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "call_approval",
						Name:      "needs_approval",
						Arguments: json.RawMessage(`{}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "done after timeout"}}},
	}}
	blocking := approval.BrokerFunc(func(ctx context.Context, req approval.Request) (approval.Decision, error) {
		<-ctx.Done()
		return approval.Decision{}, ctx.Err()
	})
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{approvalTool{}},
		Approval:    approval.WithTimeout(blocking, time.Millisecond),
		Checkpoints: checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_approval_expired", Input: "try timed approval"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
	}
	assertContainsEvent(t, types, EventApprovalRequested)
	assertContainsEvent(t, types, EventApprovalExpired)
	assertContainsEvent(t, types, EventToolError)

	cp, err := checkpoints.Load(context.Background(), "run_approval_expired")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if len(cp.State.Approval.Resolved) != 1 || cp.State.Approval.Resolved[0].Reason != approval.ErrorExpired {
		t.Fatalf("expected expired approval decision, got %#v", cp.State.Approval)
	}
}

func TestAgentRunsSubAgentTaskTool(t *testing.T) {
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:   "call_task",
						Name: "task",
						Arguments: json.RawMessage(`{"tasks":[` +
							`{"agent":"researcher","name":"Read docs","input":"summarize docs"},` +
							`{"agent":"reviewer","name":"Review risk","input":"find bugs"}` +
							`]}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "parent final"}}},
	}}
	registry := subagent.MustRegistry(subagent.SubAgentSpec{Name: "researcher"}, subagent.SubAgentSpec{Name: "reviewer"})
	agent := New(Config{
		Model:            fakeModel,
		SubAgents:        SubAgentsEnabled,
		SubAgentRegistry: registry,
		SubAgentRunner: subagent.RunnerFunc(func(ctx context.Context, spec subagent.SubAgentSpec, task subagent.TaskSpec, req subagent.Request) (subagent.TaskResult, error) {
			return subagent.TaskResult{Output: spec.Name + " handled " + task.Input}, nil
		}),
		Checkpoints: checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_subagent", Input: "delegate"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	for event := range events {
		types = append(types, event.Type)
	}
	assertContainsEvent(t, types, EventSubtaskStarted)
	assertContainsEvent(t, types, EventSubtaskDone)
	assertContainsEvent(t, types, EventToolResult)

	cp, err := checkpoints.Load(context.Background(), "run_subagent")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if len(cp.State.Subtasks) != 2 || cp.State.Subtasks[0].Status != "completed" {
		t.Fatalf("expected completed subtasks in checkpoint, got %#v", cp.State.Subtasks)
	}
	second := fakeModel.requests[1]
	toolMessage := second.Messages[len(second.Messages)-1]
	if toolMessage.Role != "tool" || toolMessage.ToolCallID != "call_task" {
		t.Fatalf("missing task tool message: %#v", toolMessage)
	}
	if !contains(toolMessage.Content, "researcher handled summarize docs") || !contains(toolMessage.Content, "reviewer handled find bugs") {
		t.Fatalf("unexpected aggregate content: %q", toolMessage.Content)
	}
}

func TestAgentPlanExecutePresetPlansExecutesAndSummarizes(t *testing.T) {
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "plan_call",
						Name:      "todo_write",
						Arguments: json.RawMessage(`{"todos":[{"id":"task_1","content":"Inspect repo"}]}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "plan created"}}},
		{
			events: []model.Event{{
				Message: &model.Message{
					ToolCalls: []model.ToolCallSpec{{
						ID:        "done_call",
						Name:      "todo_update",
						Arguments: json.RawMessage(`{"id":"task_1","status":"done","notes":"finished"}`),
					}},
				},
			}},
		},
		{events: []model.Event{{Delta: "task done"}}},
		{events: []model.Event{{Delta: "summary done"}}},
	}}
	agent := New(Config{
		Model:    fakeModel,
		Planning: PlanningPlanExecute,
	})

	result, err := agent.Run(context.Background(), Task{RunID: "run_plan_execute", Input: "do the work"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output != "summary done" {
		t.Fatalf("unexpected summary output: %q", result.Output)
	}
	if len(fakeModel.requests) != 5 {
		t.Fatalf("model calls = %d, want 5", len(fakeModel.requests))
	}
	if !hasTool(fakeModel.requests[0].Tools, "todo_write") {
		t.Fatalf("plan request missing todo_write tool: %#v", fakeModel.requests[0].Tools)
	}
	if fakeModel.requests[4].ToolChoice != model.ToolChoiceNone {
		t.Fatalf("summary request tool choice = %q, want none", fakeModel.requests[4].ToolChoice)
	}
}

func assertContainsEvent(t *testing.T, events []EventType, want EventType) {
	t.Helper()
	for _, event := range events {
		if event == want {
			return
		}
	}
	t.Fatalf("events %v did not contain %s", events, want)
}

func hasTool(tools []model.ToolSpec, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func contains(text, sub string) bool {
	return strings.Contains(text, sub)
}

type scriptedTurn struct {
	events []model.Event
}

type scriptedModel struct {
	turns    []scriptedTurn
	requests []model.Request
}

func (m *scriptedModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	var response model.Response
	for event := range stream {
		if event.Message != nil {
			response.Message = *event.Message
		}
		if event.Delta != "" {
			response.Message.Content += event.Delta
		}
		response.Usage = event.Usage
	}
	return &response, nil
}

func (m *scriptedModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	m.requests = append(m.requests, req)
	turn := scriptedTurn{}
	if len(m.turns) > 0 {
		turn = m.turns[0]
		m.turns = m.turns[1:]
	}
	out := make(chan model.Event, len(turn.events))
	go func() {
		defer close(out)
		for _, event := range turn.events {
			select {
			case out <- event:
			case <-ctx.Done():
				out <- model.Event{Error: ctx.Err()}
				return
			}
		}
	}()
	return out, nil
}

type echoTool struct{}

func (echoTool) Name() string { return "echo" }

func (echoTool) Description() string { return "Echo text" }

func (echoTool) Schema() map[string]any { return nil }

func (echoTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, nil
	}
	return tool.Result{Output: args.Text}, nil
}

type approvalTool struct{}

func (approvalTool) Name() string { return "needs_approval" }

func (approvalTool) Description() string { return "Requires approval" }

func (approvalTool) Schema() map[string]any { return nil }

func (approvalTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	if approval.IsApprovedAction(call.Metadata[approval.MetadataDecisionAction]) {
		return tool.Result{Output: "approved result"}, nil
	}
	req := approval.Request{
		ID:         "approval_test",
		RunID:      call.RunID,
		ToolCallID: call.ToolCallID,
		ToolName:   "needs_approval",
		Operation:  "test.approval",
		Title:      "Approve test tool",
		Risk:       approval.RiskMedium,
		Options:    approval.DefaultOptions(),
		CreatedAt:  time.Now().UTC(),
	}
	return approval.RequiredResult(req), approval.ErrRequired
}

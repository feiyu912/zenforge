package zenforge

import (
	"context"
	"encoding/json"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
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
	model := &scriptedModel{turns: []scriptedTurn{
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
		Model:       model,
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
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	second := model.requests[1]
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

func assertContainsEvent(t *testing.T, events []EventType, want EventType) {
	t.Helper()
	for _, event := range events {
		if event == want {
			return
		}
	}
	t.Fatalf("events %v did not contain %s", events, want)
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

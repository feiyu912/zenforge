package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/feiyu912/zenforge"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tools"
	"github.com/feiyu912/zenforge/trace"
)

type summarizeInput struct {
	Topic string `json:"topic" jsonschema:"required,description=Topic to summarize"`
}

func main() {
	summarize := tools.Must("summarize_topic", "Return one local fact about a topic.", func(ctx context.Context, in summarizeInput) (string, error) {
		return "ZenForge gives Go services a durable agent harness for tools, approvals, traces, and checkpoints.", nil
	})

	events := eventlogmemory.New()
	checkpoints := checkpointmemory.New()
	traces := trace.NewMemorySink()
	agent := zenforge.New(zenforge.Config{
		Model:        &scriptedModel{},
		Instructions: "Use tools when useful and answer briefly.",
		Tools:        []zenforge.Tool{summarize},
		Events:       events,
		Checkpoints:  checkpoints,
		Trace:        trace.Redact(traces),
		MaxSteps:     4,
	})

	result, err := agent.Run(context.Background(), zenforge.Task{
		RunID: "run_sdk_example",
		Input: "Summarize ZenForge.",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Output)
}

type scriptedModel struct {
	turn int
}

func (m *scriptedModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	response := model.Response{}
	for event := range stream {
		if event.Error != nil {
			return nil, event.Error
		}
		if event.Message != nil {
			response.Message = *event.Message
		}
		if event.Delta != "" {
			response.Message.Content += event.Delta
		}
	}
	return &response, nil
}

func (m *scriptedModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	m.turn++
	out := make(chan model.Event, 1)
	go func() {
		defer close(out)
		if m.turn == 1 {
			out <- model.Event{Message: &model.Message{
				ToolCalls: []model.ToolCallSpec{{
					ID:        "call_summarize",
					Name:      "summarize_topic",
					Arguments: json.RawMessage(`{"topic":"ZenForge"}`),
				}},
			}}
			return
		}
		out <- model.Event{Delta: "ZenForge gives Go services a durable agent harness for tools, approvals, traces, and checkpoints."}
	}()
	return out, nil
}

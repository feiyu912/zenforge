package zenforge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/compaction"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tools/contextinfo"
)

func TestAgentPersistsRateLimitsAndFeedsContextRemaining(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	contextTool, err := contextinfo.New()
	if err != nil {
		t.Fatalf("contextinfo.New returned error: %v", err)
	}
	limits := &model.RateLimit{
		RequestsLimit:     500,
		RequestsRemaining: 499,
		RequestsReset:     1500 * time.Millisecond,
		TokensLimit:       150000,
		TokensRemaining:   149000,
		TokensReset:       30 * time.Second,
	}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{
			{Type: model.EventUsage, Usage: model.Usage{
				PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105, RateLimits: limits,
			}},
			{ToolCalls: []model.ToolCallSpec{{
				ID: "call_ctx", Name: contextinfo.Name, Arguments: json.RawMessage(`{}`),
			}}},
		}},
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{contextTool},
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    4,
		Compaction: &compaction.Config{
			Policy:     compaction.Policy{ContextWindow: 1000, RetainTokens: 400},
			Summarizer: &fakeCompactionSummarizer{},
		},
	})

	stream, err := agent.Stream(ctx, Task{Input: "check the budget"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}

	// The normalized snapshot rides a dedicated model.ratelimits event.
	rateEvents := eventsByType(collected, EventModelRateLimits)
	if len(rateEvents) != 1 {
		t.Fatalf("model.ratelimits events = %d, want 1", len(rateEvents))
	}
	payload := rateEvents[0].Payload
	if payload["requestsLimit"] != 500 || payload["requestsRemaining"] != 499 {
		t.Fatalf("request payload = %v", payload)
	}
	if payload["tokensLimit"] != 150000 || payload["tokensRemaining"] != 149000 {
		t.Fatalf("token payload = %v", payload)
	}
	if payload["requestsResetMs"] != int64(1500) || payload["tokensResetMs"] != int64(30000) {
		t.Fatalf("reset payload = %v", payload)
	}

	// The tool call observed the live budget: window 1000 minus the
	// provider-measured prompt tokens 100.
	toolOutput := lastToolOutput(t, collected, contextinfo.Name)
	if !strings.Contains(toolOutput, `"tokens_left":900`) {
		t.Fatalf("get_context_remaining output = %q, want tokens_left 900", toolOutput)
	}

	// Durable state keeps cumulative usage and the latest snapshot.
	cp, err := checkpoints.Load(ctx, collected[0].RunID())
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	if cp.State.Usage.InputTokens != 100 || cp.State.Usage.TotalTokens != 105 {
		t.Fatalf("persisted usage = %+v", cp.State.Usage)
	}
	persisted := cp.State.Usage.RateLimits
	if persisted == nil {
		t.Fatal("persisted usage has no rate-limit snapshot")
	}
	if persisted.TokensRemaining != 149000 || persisted.RequestsResetMs != 1500 || persisted.TokensResetMs != 30000 {
		t.Fatalf("persisted rate limits = %+v", persisted)
	}
	if back := persisted.Model(); back.TokensReset != 30*time.Second || back.RequestsRemaining != 499 {
		t.Fatalf("snapshot round-trip = %+v", back)
	}
}

func TestContextRemainingReportsNullWithoutWindow(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	contextTool, err := contextinfo.New()
	if err != nil {
		t.Fatalf("contextinfo.New returned error: %v", err)
	}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurn("call_ctx", contextinfo.Name),
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{contextTool},
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    4,
	})

	stream, err := agent.Stream(ctx, Task{Input: "check the budget"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	toolOutput := lastToolOutput(t, collected, contextinfo.Name)
	if !strings.Contains(toolOutput, `"tokens_left":null`) {
		t.Fatalf("output = %q, want null tokens_left without a configured window", toolOutput)
	}
	if len(eventsByType(collected, EventModelRateLimits)) != 0 {
		t.Fatal("model.ratelimits emitted without any provider snapshot")
	}
}

func lastToolOutput(t *testing.T, collected []Event, toolName string) string {
	t.Helper()
	output := ""
	for _, event := range eventsByType(collected, EventToolResult) {
		if event.Payload["toolName"] == toolName {
			value, _ := event.Payload["output"].(string)
			output = value
		}
	}
	if output == "" {
		t.Fatalf("no tool.result output found for %s", toolName)
	}
	return output
}

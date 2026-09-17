package zenforge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/compaction"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
)

type fakeCompactionSummarizer struct {
	calls    int
	summary  string
	err      error
	requests []compaction.SummarizeRequest
}

func (f *fakeCompactionSummarizer) Summarize(_ context.Context, req compaction.SummarizeRequest) (compaction.Summary, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.err != nil {
		return compaction.Summary{}, f.err
	}
	summary := f.summary
	if summary == "" {
		summary = "SUMMARY-OF-SHADOWED-WORK"
	}
	return compaction.Summary{Text: summary, Model: "fake-summarizer"}, nil
}

type bigOutputTool struct {
	output string
	calls  int
}

func (t *bigOutputTool) Name() string        { return "record" }
func (t *bigOutputTool) Description() string { return "Record calls with large output" }
func (t *bigOutputTool) Schema() map[string]any {
	return nil
}

func (t *bigOutputTool) Call(_ context.Context, _ json.RawMessage, _ tool.Context) (tool.Result, error) {
	t.calls++
	return tool.Result{Output: t.output}, nil
}

func collectRunEvents(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var collected []Event
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func eventsByType(events []Event, eventType EventType) []Event {
	var out []Event
	for _, event := range events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

func toolCallTurn(id, name string) scriptedTurn {
	return scriptedTurn{events: []model.Event{{
		ToolCalls: []model.ToolCallSpec{{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}},
	}}}
}

func TestAgentAutoCompactsAtStepBoundaryUnderPressure(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	summarizer := &fakeCompactionSummarizer{}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurn("call_1", "record"),
		toolCallTurn("call_2", "record"),
		toolCallTurn("call_3", "record"),
		{events: []model.Event{{Delta: "done"}}},
	}}
	big := strings.Repeat("x", 2400)
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{&bigOutputTool{output: big}},
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    6,
		Compaction: &compaction.Config{
			Policy: compaction.Policy{
				ContextWindow: 2000,
				RetainTokens:  700,
			},
			Summarizer: summarizer,
		},
	})

	stream, err := agent.Stream(ctx, Task{Input: "run the tool"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	started := eventsByType(collected, EventCompactionStarted)
	if len(started) != 1 {
		t.Fatalf("compaction.started events = %d, want 1", len(started))
	}
	if got := started[0].Payload["reason"]; got != string(compaction.ReasonPressure) {
		t.Fatalf("compaction reason = %v, want pressure", got)
	}
	if len(eventsByType(collected, EventCompactionSummary)) != 1 {
		t.Fatalf("compaction.summary event missing")
	}
	done := eventsByType(collected, EventCompactionDone)
	if len(done) != 1 || done[0].Payload["outcome"] != "summarized" {
		t.Fatalf("compaction.done event = %+v, want outcome summarized", done)
	}
	if summarizer.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", summarizer.calls)
	}

	// The post-compaction model request carries the replacement message and
	// retains the well-formed recent tool turn.
	if len(fakeModel.requests) < 2 {
		t.Fatalf("model requests = %d, want at least 2", len(fakeModel.requests))
	}
	last := fakeModel.requests[len(fakeModel.requests)-1]
	if !strings.HasPrefix(last.Messages[0].Content, compaction.SummaryPrefix) {
		t.Fatalf("post-compaction request does not start with the summary replacement: %q", last.Messages[0].Content[:60])
	}
	if !strings.Contains(last.Messages[0].Content, "SUMMARY-OF-SHADOWED-WORK") {
		t.Fatalf("replacement message lacks the summary text")
	}
	sawToolResult := false
	for _, message := range last.Messages[1:] {
		if message.Role == "tool" && message.ToolCallID == "call_3" {
			sawToolResult = true
		}
		if message.Role == "tool" && message.ToolCallID == "" {
			t.Fatalf("retained dangling tool result: %+v", message)
		}
	}
	if !sawToolResult {
		t.Fatalf("recent tool result was not retained: %+v", last.Messages)
	}

	// Durable provenance: the terminal checkpoint carries the record and the
	// compacted history.
	runID := collected[0].RunID()
	cp, err := checkpoints.Load(ctx, runID)
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	if len(cp.State.Compactions) != 1 {
		t.Fatalf("compaction records = %d, want 1", len(cp.State.Compactions))
	}
	record := cp.State.Compactions[0]
	if record.Reason != string(compaction.ReasonPressure) || record.ShadowedCount == 0 {
		t.Fatalf("unexpected compaction record: %+v", record)
	}
	if record.TokensAfter >= record.TokensBefore {
		t.Fatalf("compaction record did not reduce tokens: %+v", record)
	}
	if err := harness.ValidateRunState(cp.State); err != nil {
		t.Fatalf("compacted run state failed validation: %v", err)
	}
}

func TestAgentRecoversFromContextOverflowWithCompaction(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	summarizer := &fakeCompactionSummarizer{summary: "overflow recovery summary"}
	overflow := model.NewHTTPStatusError("openai", "chat", "https://api.example/v1/chat", 400, "Bad Request",
		`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 200 tokens"}}`)
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: overflow}}},
		{events: []model.Event{{Delta: "recovered"}}},
	}}
	history := []model.Message{
		{Role: "user", Content: "earlier context " + strings.Repeat("a", 800)},
		{Role: "assistant", Content: "earlier answer " + strings.Repeat("b", 800)},
	}
	agent := New(Config{
		Model:       fakeModel,
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    2,
		Compaction: &compaction.Config{
			Policy:     compaction.Policy{ContextWindow: 4000, RetainTokens: 100},
			Summarizer: summarizer,
		},
	})

	stream, err := agent.Stream(ctx, Task{Input: "continue the work", InitialMessages: history})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	dones := eventsByType(collected, EventRunDone)
	if len(dones) != 1 || dones[0].Payload["output"] != "recovered" {
		t.Fatalf("run did not recover: %+v", dones)
	}
	if len(eventsByType(collected, EventModelSuperseded)) != 1 {
		t.Fatalf("failed attempt was not superseded")
	}
	started := eventsByType(collected, EventCompactionStarted)
	if len(started) != 1 || started[0].Payload["reason"] != string(compaction.ReasonOverflow) {
		t.Fatalf("overflow compaction events = %+v", started)
	}
	retries := eventsByType(collected, EventModelRetry)
	if len(retries) != 1 || retries[0].Payload["reason"] != "context_overflow" {
		t.Fatalf("model.retry event = %+v, want context_overflow retry", retries)
	}
	if summarizer.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", summarizer.calls)
	}

	runID := collected[0].RunID()
	cp, err := checkpoints.Load(ctx, runID)
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	if len(cp.State.Compactions) != 1 || cp.State.Compactions[0].Reason != string(compaction.ReasonOverflow) {
		t.Fatalf("overflow compaction record missing: %+v", cp.State.Compactions)
	}
	if len(cp.State.Model.Attempts) != 2 {
		t.Fatalf("attempt history = %d, want superseded + committed", len(cp.State.Model.Attempts))
	}
	if cp.State.Model.Attempts[0].Status != harness.ModelAttemptSuperseded ||
		cp.State.Model.Attempts[1].Status != harness.ModelAttemptCommitted {
		t.Fatalf("unexpected attempt statuses: %+v", cp.State.Model.Attempts)
	}
	if cp.State.Model.Attempts[1].ReplacesID != cp.State.Model.Attempts[0].ID {
		t.Fatalf("replacement attempt is not linked to the superseded one: %+v", cp.State.Model.Attempts)
	}
	if err := harness.ValidateRunState(cp.State); err != nil {
		t.Fatalf("recovered run state failed validation: %v", err)
	}
}

func TestAgentCompactionPruneOnlySkipsSummarization(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	events := &testEventStore{}
	summarizer := &fakeCompactionSummarizer{}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurn("call_1", "record"),
		{events: []model.Event{{Delta: "done"}}},
	}}
	// 12000 chars ~ 3000 tokens: above the 800-token threshold, but pruning
	// to head+tail (~1200 chars ~ 300 tokens) clears the pressure alone.
	big := strings.Repeat("y", 12000)
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{&bigOutputTool{output: big}},
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    4,
		Compaction: &compaction.Config{
			Policy: compaction.Policy{
				ContextWindow: 1000,
				RetainTokens:  400,
				Prune: compaction.PrunePolicy{
					Enabled:        true,
					ThresholdChars: 4000,
					HeadChars:      900,
					TailChars:      300,
				},
			},
			Summarizer: summarizer,
		},
	})

	stream, err := agent.Stream(ctx, Task{Input: "run the tool"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete")
	}
	pruned := eventsByType(collected, EventCompactionPruned)
	if len(pruned) != 1 {
		t.Fatalf("compaction.pruned events = %d, want 1", len(pruned))
	}
	done := eventsByType(collected, EventCompactionDone)
	if len(done) != 1 || done[0].Payload["outcome"] != "pruned" {
		t.Fatalf("compaction.done = %+v, want outcome pruned", done)
	}
	if len(eventsByType(collected, EventCompactionSummary)) != 0 {
		t.Fatalf("prune-only pass summarized anyway")
	}
	if summarizer.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0", summarizer.calls)
	}

	last := fakeModel.requests[len(fakeModel.requests)-1]
	found := false
	for _, message := range last.Messages {
		if message.Role == "tool" && compaction.HasPruneMarker(message.Content) {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-compaction request lacks the pruned tool result")
	}
	runID := collected[0].RunID()
	cp, err := checkpoints.Load(ctx, runID)
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	if len(cp.State.Compactions) != 0 {
		t.Fatalf("prune-only pass wrote a summary record: %+v", cp.State.Compactions)
	}
}

func TestAgentCompactionPressureFailureFailsSoft(t *testing.T) {
	ctx := context.Background()
	events := &testEventStore{}
	summarizer := &fakeCompactionSummarizer{err: errors.New("summarizer unavailable")}
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurn("call_1", "record"),
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{&bigOutputTool{output: strings.Repeat("x", 2400)}},
		Events:      events,
		Checkpoints: checkpointmemory.New(),
		MaxSteps:    4,
		Compaction: &compaction.Config{
			Policy:     compaction.Policy{ContextWindow: 200, RetainTokens: 60},
			Summarizer: summarizer,
		},
	})

	stream, err := agent.Stream(ctx, Task{Input: "run the tool"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("pressure compaction failure killed the run: %+v", collected)
	}
	failures := eventsByType(collected, EventCompactionError)
	if len(failures) != 1 {
		t.Fatalf("compaction.error events = %d, want 1", len(failures))
	}
	if !strings.Contains(failures[0].Payload["error"].(string), "summarizer unavailable") {
		t.Fatalf("compaction.error payload = %+v", failures[0].Payload)
	}
}

func TestAgentCompactionConfigErrorFailsRun(t *testing.T) {
	ctx := context.Background()
	agent := New(Config{
		Model: &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "done"}}}}},
		Compaction: &compaction.Config{
			Policy:     compaction.Policy{ContextWindow: -5},
			Summarizer: &fakeCompactionSummarizer{},
		},
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err == nil || !strings.Contains(err.Error(), "configure compaction") {
		t.Fatalf("Stream error = %v, want compaction configuration failure", err)
	}
	if stream != nil {
		t.Fatalf("Stream returned an event stream for invalid compaction config")
	}
}

func TestAgentWithoutCompactionEmitsNoCompactionEvents(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		toolCallTurn("call_1", "record"),
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Tools:       []Tool{&bigOutputTool{output: strings.Repeat("x", 2400)}},
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		MaxSteps:    4,
	})
	stream, err := agent.Stream(ctx, Task{Input: "run the tool"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete")
	}
	for _, event := range collected {
		if strings.HasPrefix(string(event.Type), "compaction.") || event.Type == EventModelRetry {
			t.Fatalf("unexpected compaction event without configuration: %v", event.Type)
		}
	}
}

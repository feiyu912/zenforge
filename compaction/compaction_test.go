package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

func timeNow() time.Time { return time.Now().UTC() }

type fakeSummarizer struct {
	requests []SummarizeRequest
	summary  Summary
	err      error
}

func (f *fakeSummarizer) Summarize(_ context.Context, req SummarizeRequest) (Summary, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return Summary{}, f.err
	}
	summary := f.summary
	if summary.Text == "" {
		summary.Text = "summary of shadowed work"
	}
	return summary, nil
}

type fakeModel struct {
	requests []model.Request
	response *model.Response
	err      error
}

func (m *fakeModel) Generate(_ context.Context, req model.Request) (*model.Response, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *fakeModel) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	return nil, errors.New("stream is not used")
}

func message(role, content string) harness.MessageState {
	return harness.MessageState{Role: role, Content: content}
}

func toolResult(id, name, content string) harness.MessageState {
	return harness.MessageState{Role: "tool", Name: name, ToolCallID: id, Content: content}
}

func assistantWithCall(id, name, content string) harness.MessageState {
	return harness.MessageState{
		Role:    "assistant",
		Content: content,
		ToolCalls: []harness.ToolCallSpec{
			{ID: id, Name: name, Arguments: []byte(`{}`)},
		},
	}
}

func TestPolicyWithDefaultsFillsReferenceDefaults(t *testing.T) {
	policy, err := Policy{ContextWindow: 1000}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	if policy.ThresholdRatio != DefaultThresholdRatio {
		t.Fatalf("threshold ratio = %v, want %v", policy.ThresholdRatio, DefaultThresholdRatio)
	}
	if policy.RetainRatio != DefaultRetainRatio {
		t.Fatalf("retain ratio = %v, want %v", policy.RetainRatio, DefaultRetainRatio)
	}
	if policy.MaxSummaryTokens != DefaultMaxSummaryTokens {
		t.Fatalf("max summary tokens = %d, want %d", policy.MaxSummaryTokens, DefaultMaxSummaryTokens)
	}
	if policy.CompactionRetries != DefaultCompactionRetries {
		t.Fatalf("compaction retries = %d, want %d", policy.CompactionRetries, DefaultCompactionRetries)
	}
	if policy.MaxOverflowRetries != DefaultMaxOverflowRetries {
		t.Fatalf("overflow retries = %d, want %d", policy.MaxOverflowRetries, DefaultMaxOverflowRetries)
	}
	if got := policy.retainBudget(); got != 160 {
		t.Fatalf("retain budget = %d, want 160", got)
	}
	if got := policy.thresholdTokens(); got != 800 {
		t.Fatalf("threshold tokens = %d, want 800", got)
	}
}

func TestPolicyWithDefaultsRejectsInvalidCombination(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
	}{
		{"negative window", Policy{ContextWindow: -1}},
		{"threshold above one", Policy{ContextWindow: 100, ThresholdRatio: 1.5}},
		{"negative threshold", Policy{ContextWindow: 100, ThresholdRatio: -0.2}},
		{"both retain forms", Policy{ContextWindow: 100, RetainRatio: 0.2, RetainTokens: 10}},
		{"retain ratio one", Policy{ContextWindow: 100, RetainRatio: 1}},
		{"negative retain tokens", Policy{ContextWindow: 100, RetainTokens: -5}},
		{"negative summary tokens", Policy{ContextWindow: 100, MaxSummaryTokens: -1}},
		{"negative retries", Policy{ContextWindow: 100, CompactionRetries: -1}},
		{"negative overflow retries", Policy{ContextWindow: 100, MaxOverflowRetries: -1}},
		{"prune head tail above threshold", Policy{
			ContextWindow: 100,
			Prune:         PrunePolicy{Enabled: true, ThresholdChars: 100, HeadChars: 60, TailChars: 60},
		}},
		{"negative prune value", Policy{
			ContextWindow: 100,
			Prune:         PrunePolicy{Enabled: true, ThresholdChars: -1},
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := testCase.policy.WithDefaults(); err == nil {
				t.Fatalf("WithDefaults accepted invalid policy %+v", testCase.policy)
			}
		})
	}
}

func TestPolicyWithDefaultsAllowsExplicitRetainForms(t *testing.T) {
	byTokens, err := Policy{ContextWindow: 1000, RetainTokens: 500, ThresholdRatio: 0.9}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	if got := byTokens.retainBudget(); got != 500 {
		t.Fatalf("retain budget = %d, want 500", got)
	}
	if byTokens.CompactionRetries != DefaultCompactionRetries {
		t.Fatalf("zero compactionRetries did not take the default")
	}
	zeroRetries, err := Policy{ContextWindow: 1000, CompactionRetries: -1}.WithDefaults()
	if err == nil {
		t.Fatalf("negative compactionRetries accepted: %+v", zeroRetries)
	}
}

func TestHeuristicEstimatorIsDeterministicAndRuneBased(t *testing.T) {
	estimator := NewHeuristicEstimator()
	text := strings.Repeat("abcd", 10) // 40 runes -> 10 tokens
	if got := estimator.EstimateText(text); got != 10 {
		t.Fatalf("EstimateText = %d, want 10", got)
	}
	if got := estimator.EstimateText(text); got != 10 {
		t.Fatalf("EstimateText is not deterministic: %d", got)
	}
	if got := estimator.EstimateText(""); got != 0 {
		t.Fatalf("EstimateText empty = %d, want 0", got)
	}
	messages := []harness.MessageState{message("user", text)}
	first := estimator.EstimateMessages(messages)
	if first != 10+messageOverheadTokens {
		t.Fatalf("EstimateMessages = %d, want %d", first, 10+messageOverheadTokens)
	}
	tools := []model.ToolSpec{{Name: "tool", Description: strings.Repeat("x", 40), Schema: map[string]any{"type": "object"}}}
	if got := estimator.EstimateTools(tools); got <= 10 {
		t.Fatalf("EstimateTools = %d, want > 10", got)
	}
	// Partial tokens round up.
	if got := estimator.EstimateText("abcde"); got != 2 {
		t.Fatalf("EstimateText partial = %d, want 2", got)
	}
}

func TestPruneToolResultsRewritesOnlyOversizedToolMessages(t *testing.T) {
	policy := PrunePolicy{Enabled: true, ThresholdChars: 100, HeadChars: 20, TailChars: 10}
	big := strings.Repeat("x", 200)
	messages := []harness.MessageState{
		message("user", "keep me"),
		toolResult("call_1", "shell", big),
		toolResult("call_2", "shell", "small output"),
		message("assistant", "keep me too"),
	}
	pruned, stats := PruneToolResults(messages, policy)
	if stats.Pruned != 1 {
		t.Fatalf("pruned = %d, want 1", stats.Pruned)
	}
	if stats.CharsRemoved <= 0 {
		t.Fatalf("charsRemoved = %d, want positive", stats.CharsRemoved)
	}
	if pruned[0].Content != "keep me" || pruned[2].Content != "small output" || pruned[3].Content != "keep me too" {
		t.Fatalf("non-target messages changed: %+v", pruned)
	}
	content := pruned[1].Content
	if !strings.HasPrefix(content, strings.Repeat("x", 20)) {
		t.Fatalf("head budget not retained: %q", content[:40])
	}
	if !strings.HasSuffix(content, strings.Repeat("x", 10)) {
		t.Fatalf("tail budget not retained")
	}
	if !HasPruneMarker(content) {
		t.Fatalf("prune marker missing: %q", content)
	}
	if _, ok := pruned[1].Meta["compaction.pruned"].(map[string]any); !ok {
		t.Fatalf("prune provenance missing: %+v", pruned[1].Meta)
	}
	// Inputs are copied, never mutated.
	if messages[1].Content != big {
		t.Fatalf("PruneToolResults mutated its input")
	}
}

func TestPruneToolResultsDisabledPassesThrough(t *testing.T) {
	messages := []harness.MessageState{toolResult("call_1", "shell", strings.Repeat("x", 500))}
	pruned, stats := PruneToolResults(messages, PrunePolicy{})
	if stats.Pruned != 0 || pruned[0].Content != messages[0].Content {
		t.Fatalf("disabled pruning changed messages: %+v stats %+v", pruned, stats)
	}
}

func TestEvaluatePressureAtThreshold(t *testing.T) {
	policy, err := Policy{ContextWindow: 100}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	estimator := NewHeuristicEstimator()
	small := []harness.MessageState{message("user", strings.Repeat("a", 40))}
	pressure := Evaluate(policy, estimator, 0, nil, small)
	if pressure.ShouldCompact {
		t.Fatalf("small history triggered compaction: %+v", pressure)
	}
	if pressure.WindowTokens != 100 || pressure.MessagesTokens == 0 {
		t.Fatalf("unexpected pressure: %+v", pressure)
	}
	big := []harness.MessageState{message("user", strings.Repeat("a", 4000))}
	pressure = Evaluate(policy, estimator, 10, nil, big)
	if !pressure.ShouldCompact {
		t.Fatalf("large history did not trigger compaction: %+v", pressure)
	}
	if pressure.Ratio <= 0 || pressure.Threshold != DefaultThresholdRatio {
		t.Fatalf("unexpected ratio reporting: %+v", pressure)
	}
	// No window means no automatic compaction.
	noWindow := Policy{}
	pressure = Evaluate(noWindow, estimator, 0, nil, big)
	if pressure.ShouldCompact || pressure.Ratio != 0 {
		t.Fatalf("windowless policy triggered compaction: %+v", pressure)
	}
}

func TestRetainBoundaryKeepsToolPairsTogether(t *testing.T) {
	compactor, err := New(Config{
		Policy:     Policy{ContextWindow: 1000, RetainTokens: 60},
		Summarizer: &fakeSummarizer{},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	messages := []harness.MessageState{
		message("user", strings.Repeat("a", 400)), // ~100 tokens, shadowed
		assistantWithCall("call_1", "shell", "working"),
		toolResult("call_1", "shell", strings.Repeat("b", 80)), // ~20 tokens
		message("assistant", strings.Repeat("c", 80)),          // ~20 tokens
	}
	boundary := compactor.RetainBoundary(messages)
	if boundary != 1 {
		t.Fatalf("boundary = %d, want 1 (assistant tool-call turn retained with its result)", boundary)
	}
	// A budget that lands directly on the tool result backs off over it.
	compactor, err = New(Config{
		Policy:     Policy{ContextWindow: 1000, RetainTokens: 50},
		Summarizer: &fakeSummarizer{},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	boundary = compactor.RetainBoundary(messages)
	if boundary != 1 {
		t.Fatalf("boundary = %d, want 1 after tool back-off", boundary)
	}
	if got := compactor.RetainBoundary(nil); got != 0 {
		t.Fatalf("empty history boundary = %d, want 0", got)
	}
}

func TestCompactProducesReplacementAndRecord(t *testing.T) {
	summarizer := &fakeSummarizer{summary: Summary{
		Text:  "did step one; next is step two",
		Model: "summarizer-model",
		Usage: model.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}}
	compactor, err := New(Config{
		Policy: Policy{
			ContextWindow: 1000,
			RetainTokens:  40,
			Prune:         PrunePolicy{Enabled: true, ThresholdChars: 100, HeadChars: 20, TailChars: 10},
		},
		Summarizer: summarizer,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	messages := []harness.MessageState{
		message("user", "original task"),
		assistantWithCall("call_1", "shell", "ran a command"),
		toolResult("call_1", "shell", strings.Repeat("x", 400)),
		message("assistant", "recent answer "+strings.Repeat("y", 40)),
	}
	result, err := compactor.Compact(context.Background(), Request{
		RunID:    "run_1",
		RunInput: "original task",
		Step:     3,
		Reason:   ReasonPressure,
		Messages: messages,
	})
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if len(result.Messages) == 0 || result.Messages[0].Role != "user" {
		t.Fatalf("replacement message missing: %+v", result.Messages)
	}
	if !strings.HasPrefix(result.Messages[0].Content, SummaryPrefix) {
		t.Fatalf("replacement lacks summary prefix: %q", result.Messages[0].Content[:80])
	}
	if !strings.Contains(result.Messages[0].Content, "did step one") {
		t.Fatalf("replacement lacks summary text")
	}
	if result.Messages[len(result.Messages)-1].Content != messages[len(messages)-1].Content {
		t.Fatalf("recent context not retained")
	}
	if result.Record.ID == "" || result.Record.Step != 3 || result.Record.Reason != string(ReasonPressure) {
		t.Fatalf("unexpected record: %+v", result.Record)
	}
	if result.Record.ShadowedCount != 3 {
		t.Fatalf("shadowed = %d, want 3", result.Record.ShadowedCount)
	}
	if result.Record.SummarizerModel != "summarizer-model" || result.Record.SummaryUsage.TotalTokens != 18 {
		t.Fatalf("summarizer provenance missing: %+v", result.Record)
	}
	if result.Prune.Pruned != 1 {
		t.Fatalf("prune stats = %+v, want one pruned result", result.Prune)
	}
	if result.TokensAfter >= result.TokensBefore {
		t.Fatalf("compaction did not reduce tokens: before %d after %d", result.TokensBefore, result.TokensAfter)
	}
	if len(summarizer.requests) != 1 || summarizer.requests[0].RunInput != "original task" {
		t.Fatalf("summarizer request = %+v", summarizer.requests)
	}
	// The summarizer saw the pruned shadow range.
	shadowedTool := summarizer.requests[0].Messages[2]
	if !HasPruneMarker(shadowedTool.Content) {
		t.Fatalf("summarizer input was not pruned: %q", shadowedTool.Content)
	}
	// Compaction is well-formed for provider replay: no dangling tool result.
	for i, item := range result.Messages {
		if item.Role == "tool" {
			if i == 0 {
				t.Fatalf("retained history starts with a dangling tool result")
			}
		}
	}
}

func TestCompactRejectsInvalidRequests(t *testing.T) {
	compactor, err := New(Config{
		Policy:     Policy{ContextWindow: 1000},
		Summarizer: &fakeSummarizer{},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := compactor.Compact(context.Background(), Request{Reason: Reason("bogus"), Messages: []harness.MessageState{message("user", "x")}}); err == nil {
		t.Fatalf("invalid reason accepted")
	}
	if _, err := compactor.Compact(context.Background(), Request{Reason: ReasonManual}); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("empty history error = %v, want ErrNothingToCompact", err)
	}
}

func TestCompactPropagatesSummarizerFailure(t *testing.T) {
	failure := errors.New("summarizer unavailable")
	compactor, err := New(Config{
		Policy:     Policy{ContextWindow: 1000, RetainTokens: 10},
		Summarizer: &fakeSummarizer{err: failure},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	_, err = compactor.Compact(context.Background(), Request{
		Reason:   ReasonPressure,
		Messages: []harness.MessageState{message("user", strings.Repeat("a", 4000)), message("assistant", "ok")},
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Compact error = %v, want wrapped failure", err)
	}
}

func TestNewRequiresSummarizer(t *testing.T) {
	if _, err := New(Config{Policy: Policy{ContextWindow: 100}}); err == nil {
		t.Fatalf("New accepted a missing summarizer")
	}
	if _, err := New(Config{Policy: Policy{ContextWindow: -1}, Summarizer: &fakeSummarizer{}}); err == nil {
		t.Fatalf("New accepted an invalid policy")
	}
}

func TestModelSummarizerUsesGenerateWithHandoffPrompt(t *testing.T) {
	fake := &fakeModel{response: &model.Response{
		Message: model.Message{Role: "assistant", Content: " handoff summary "},
		Usage:   model.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}}
	summarizer := ModelSummarizer{Model: fake, Name: "cheap-model"}
	summary, err := summarizer.Summarize(context.Background(), SummarizeRequest{
		RunInput:         "the task",
		Messages:         []harness.MessageState{message("assistant", "did work")},
		MaxSummaryTokens: 1234,
	})
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if summary.Text != "handoff summary" || summary.Model != "cheap-model" || summary.MaxTokens != 1234 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Usage.TotalTokens != 7 {
		t.Fatalf("usage not propagated: %+v", summary.Usage)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("generate calls = %d, want 1", len(fake.requests))
	}
	request := fake.requests[0]
	if request.ToolChoice != model.ToolChoiceNone {
		t.Fatalf("summarize request allows tools: %v", request.ToolChoice)
	}
	if !strings.Contains(request.Messages[0].Content, "CONTEXT CHECKPOINT COMPACTION") {
		t.Fatalf("summarize system prompt missing handoff instructions")
	}
	if !strings.Contains(request.Messages[1].Content, "the task") {
		t.Fatalf("transcript lacks the original task")
	}
}

func TestModelSummarizerRejectsEmptySummary(t *testing.T) {
	fake := &fakeModel{response: &model.Response{Message: model.Message{Role: "assistant", Content: "   "}}}
	summarizer := ModelSummarizer{Model: fake}
	if _, err := summarizer.Summarize(context.Background(), SummarizeRequest{Messages: nil}); err == nil {
		t.Fatalf("empty summary accepted")
	}
	missing := ModelSummarizer{}
	if _, err := missing.Summarize(context.Background(), SummarizeRequest{}); err == nil {
		t.Fatalf("missing model accepted")
	}
}

func TestFormatTranscriptBoundsMessageAndTotalSize(t *testing.T) {
	messages := make([]harness.MessageState, 0, 60)
	for i := 0; i < 60; i++ {
		messages = append(messages, message("assistant", strings.Repeat("z", 4000)))
	}
	transcript := FormatTranscript(SummarizeRequest{RunInput: "task", Messages: messages}, 8000, 500)
	if len(transcript) > 12000 {
		t.Fatalf("transcript not bounded: %d chars", len(transcript))
	}
	if !strings.Contains(transcript, "task") {
		t.Fatalf("transcript lost the original task")
	}
	if !strings.Contains(transcript, "omitted") {
		t.Fatalf("transcript lacks an elision marker")
	}
	last := messages[len(messages)-1]
	if !strings.Contains(transcript, strings.Repeat("z", 100)) {
		t.Fatalf("transcript lost recent content: %q", last.Content[:20])
	}
}

func TestIsOverflowErrorClassification(t *testing.T) {
	openaiOverflow := model.NewHTTPStatusError("openai", "chat", "https://api.example/v1/chat", 400, "Bad Request",
		`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 8192 tokens"}}`)
	if !IsOverflowError(openaiOverflow) {
		t.Fatalf("openai overflow not detected")
	}
	anthropicOverflow := model.NewHTTPStatusError("anthropic", "messages", "https://api.example/v1/messages", 400, "Bad Request",
		"prompt is too long: 250000 tokens > 200000 maximum")
	if !IsOverflowError(anthropicOverflow) {
		t.Fatalf("anthropic overflow not detected")
	}
	rateLimited := model.NewHTTPStatusError("openai", "chat", "https://api.example", 429, "Too Many Requests", "context_length_exceeded lookalike")
	if IsOverflowError(rateLimited) {
		t.Fatalf("429 misclassified as overflow")
	}
	badRequest := model.NewHTTPStatusError("openai", "chat", "https://api.example", 400, "Bad Request", "invalid tool schema")
	if IsOverflowError(badRequest) {
		t.Fatalf("unrelated 400 misclassified as overflow")
	}
	streamError := errors.New("openai provider error (invalid_request_error): Please reduce the length of the messages or completion")
	if !IsOverflowError(streamError) {
		t.Fatalf("in-stream overflow not detected")
	}
	if IsOverflowError(errors.New("connection refused")) || IsOverflowError(nil) {
		t.Fatalf("non-overflow errors misclassified")
	}
}

func TestCompactionRecordValidation(t *testing.T) {
	base := harness.RunState{
		Version: harness.RunStateVersion,
		Phase:   harness.RunPhaseModel,
		Step:    2,
	}
	valid := base
	valid.AppendCompaction(harness.CompactionRecord{ID: "cmpct_1", Step: 1, Reason: "pressure", TokensBefore: 100, CreatedAt: timeNow()})
	if err := harness.ValidateRunState(valid); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	missingID := base
	missingID.AppendCompaction(harness.CompactionRecord{Step: 1, CreatedAt: timeNow()})
	if err := harness.ValidateRunState(missingID); err == nil {
		t.Fatalf("record without id accepted")
	}
	futureStep := base
	futureStep.AppendCompaction(harness.CompactionRecord{ID: "cmpct_1", Step: 9, CreatedAt: timeNow()})
	if err := harness.ValidateRunState(futureStep); err == nil {
		t.Fatalf("record ahead of run step accepted")
	}
	duplicate := base
	duplicate.AppendCompaction(harness.CompactionRecord{ID: "cmpct_1", Step: 1, CreatedAt: timeNow()})
	duplicate.AppendCompaction(harness.CompactionRecord{ID: "cmpct_1", Step: 2, CreatedAt: timeNow()})
	if err := harness.ValidateRunState(duplicate); err == nil {
		t.Fatalf("duplicate record id accepted")
	}
}

func TestAppendCompactionBoundsHistory(t *testing.T) {
	state := harness.RunState{}
	for i := 0; i < harness.CompactionHistoryLimit+10; i++ {
		state.AppendCompaction(harness.CompactionRecord{ID: NewCompactionID(), CreatedAt: timeNow()})
	}
	if len(state.Compactions) != harness.CompactionHistoryLimit {
		t.Fatalf("history = %d, want %d", len(state.Compactions), harness.CompactionHistoryLimit)
	}
}

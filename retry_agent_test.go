package zenforge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/modelretry"
)

func retryStatusError(status int, body string) *model.HTTPStatusError {
	return model.NewHTTPStatusError("openai", "chat completion", "https://api.example/v1/chat",
		status, http.StatusText(status), body)
}

func fastRetryConfig(maxRetries int) *modelretry.Config {
	return &modelretry.Config{
		MaxRetries:   maxRetries,
		InitialDelay: time.Millisecond,
		MaxDelay:     2 * time.Millisecond,
	}
}

// stallThenSucceedModel stalls the first stream until the run context ends
// (exercising the idle watchdog) and answers the second call normally.
type stallThenSucceedModel struct {
	calls int
}

func (m *stallThenSucceedModel) Generate(context.Context, model.Request) (*model.Response, error) {
	return nil, errors.New("Generate is not used")
}

func (m *stallThenSucceedModel) Stream(ctx context.Context, _ model.Request) (<-chan model.Event, error) {
	m.calls++
	out := make(chan model.Event)
	if m.calls == 1 {
		go func() {
			defer close(out)
			<-ctx.Done()
		}()
		return out, nil
	}
	go func() {
		defer close(out)
		out <- model.Event{Delta: "recovered"}
	}()
	return out, nil
}

func TestAgentRetriesServerErrorThenSucceeds(t *testing.T) {
	ctx := context.Background()
	checkpoints := checkpointmemory.New()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: retryStatusError(503, "service unavailable")}}},
		{events: []model.Event{{Delta: "recovered"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpoints,
		Retry:       fastRetryConfig(3),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	dones := eventsByType(collected, EventRunDone)
	if len(dones) != 1 || dones[0].Payload["output"] != "recovered" {
		t.Fatalf("run did not recover: %+v", dones)
	}
	retries := eventsByType(collected, EventModelRetry)
	if len(retries) != 1 {
		t.Fatalf("model.retry events = %d, want 1", len(retries))
	}
	if got := retries[0].Payload["reason"]; got != string(modelretry.CodeServer) {
		t.Fatalf("retry reason = %v, want server_error", got)
	}
	if got := retries[0].Payload["retry"]; got != 1 {
		t.Fatalf("retry counter = %v, want 1", got)
	}
	runID := collected[0].RunID()
	cp, err := checkpoints.Load(ctx, runID)
	if err != nil {
		t.Fatalf("checkpoint load returned error: %v", err)
	}
	if len(cp.State.Model.Attempts) != 2 ||
		cp.State.Model.Attempts[0].Status != harness.ModelAttemptSuperseded ||
		cp.State.Model.Attempts[1].Status != harness.ModelAttemptCommitted {
		t.Fatalf("unexpected attempt history: %+v", cp.State.Model.Attempts)
	}
	if err := harness.ValidateRunState(cp.State); err != nil {
		t.Fatalf("recovered run state failed validation: %v", err)
	}
}

func TestAgentDoesNotRetryAuthFailure(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: retryStatusError(401, "invalid api key")}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		Retry:       fastRetryConfig(3),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunError)) != 1 {
		t.Fatalf("run.error missing: %+v", collected)
	}
	if len(eventsByType(collected, EventModelRetry)) != 0 {
		t.Fatalf("auth failure was retried")
	}
	if len(fakeModel.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(fakeModel.requests))
	}
}

func TestAgentRetryExhaustionFailsRun(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: retryStatusError(500, "boom")}}},
		{events: []model.Event{{Error: retryStatusError(500, "boom")}}},
		{events: []model.Event{{Error: retryStatusError(500, "boom")}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		Retry:       fastRetryConfig(2),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunError)) != 1 {
		t.Fatalf("run.error missing after exhaustion")
	}
	retries := eventsByType(collected, EventModelRetry)
	if len(retries) != 2 {
		t.Fatalf("model.retry events = %d, want 2", len(retries))
	}
	if len(fakeModel.requests) != 3 {
		t.Fatalf("model calls = %d, want 3 (initial + 2 retries)", len(fakeModel.requests))
	}
}

func TestAgentRetriesEmptyResponse(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{},
		{events: []model.Event{{Delta: "content"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		Retry:       fastRetryConfig(2),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	dones := eventsByType(collected, EventRunDone)
	if len(dones) != 1 || dones[0].Payload["output"] != "content" {
		t.Fatalf("run did not recover from empty response: %+v", dones)
	}
	retries := eventsByType(collected, EventModelRetry)
	if len(retries) != 1 || retries[0].Payload["reason"] != string(modelretry.CodeEmptyResponse) {
		t.Fatalf("model.retry = %+v, want empty_response", retries)
	}
}

func TestAgentHonorsRetryAfterDelay(t *testing.T) {
	ctx := context.Background()
	rateLimited := retryStatusError(429, "slow down")
	rateLimited.RetryAfter = 30 * time.Millisecond
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: rateLimited}}},
		{events: []model.Event{{Delta: "ok"}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		Retry:       fastRetryConfig(1),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete")
	}
	retries := eventsByType(collected, EventModelRetry)
	if len(retries) != 1 {
		t.Fatalf("model.retry events = %d, want 1", len(retries))
	}
	delayMs, ok := retries[0].Payload["delayMs"].(int64)
	if !ok || delayMs < 30 {
		t.Fatalf("retry delayMs = %v, want at least the 30ms Retry-After advice", retries[0].Payload["delayMs"])
	}
}

func TestAgentStreamIdleTimeoutRetriesAsTimeout(t *testing.T) {
	ctx := context.Background()
	fakeModel := &stallThenSucceedModel{}
	agent := New(Config{
		Model:             fakeModel,
		Events:            &testEventStore{},
		Checkpoints:       checkpointmemory.New(),
		Retry:             fastRetryConfig(2),
		StreamIdleTimeout: 25 * time.Millisecond,
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	dones := eventsByType(collected, EventRunDone)
	if len(dones) != 1 || dones[0].Payload["output"] != "recovered" {
		t.Fatalf("run did not recover from idle stream: %+v", dones)
	}
	retries := eventsByType(collected, EventModelRetry)
	if len(retries) != 1 || retries[0].Payload["reason"] != string(modelretry.CodeTimeout) {
		t.Fatalf("model.retry = %+v, want timeout", retries)
	}
	message, _ := retries[0].Payload["error"].(string)
	if !strings.Contains(message, "idle timeout") {
		t.Fatalf("retry error = %q, want idle timeout", message)
	}
}

func TestAgentCancelDuringRetryBackoffCancelsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: retryStatusError(503, "unavailable")}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		Retry: &modelretry.Config{
			MaxRetries:   3,
			InitialDelay: 5 * time.Second,
			MaxDelay:     5 * time.Second,
		},
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	time.AfterFunc(50*time.Millisecond, cancel)
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunCancelled)) != 1 {
		t.Fatalf("run.cancelled missing: %+v", collected)
	}
	for _, event := range collected {
		if event.Type == EventRunError {
			t.Fatalf("cancellation during backoff emitted run.error: %+v", event.Payload)
		}
	}
	if len(fakeModel.requests) != 1 {
		t.Fatalf("model calls = %d, want 1 (no attempt after cancel)", len(fakeModel.requests))
	}
}

func TestAgentRetryConfigErrorFailsStream(t *testing.T) {
	ctx := context.Background()
	agent := New(Config{
		Model: &scriptedModel{},
		Retry: &modelretry.Config{Jitter: 5},
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err == nil || !strings.Contains(err.Error(), "configure model retry") {
		t.Fatalf("Stream error = %v, want retry configuration failure", err)
	}
	if stream != nil {
		t.Fatalf("Stream returned an event stream for invalid retry config")
	}
}

func TestAgentWithoutRetryPolicyDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	fakeModel := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Error: retryStatusError(503, "unavailable")}}},
	}}
	agent := New(Config{
		Model:       fakeModel,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
	})
	stream, err := agent.Stream(ctx, Task{Input: "hello"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunError)) != 1 {
		t.Fatalf("run.error missing")
	}
	if len(eventsByType(collected, EventModelRetry)) != 0 {
		t.Fatalf("retry emitted without a retry policy")
	}
	if len(fakeModel.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(fakeModel.requests))
	}
}

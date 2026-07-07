package harnesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalmemory "github.com/feiyu912/zenforge/approval/memory"
	"github.com/feiyu912/zenforge/eventlog"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/server/sse"
	"github.com/feiyu912/zenforge/tool"
)

func TestNewRuntimeWiresSharedComponentsAndOptions(t *testing.T) {
	durable := eventlogmemory.New()
	access := AccessFunc(func(context.Context, *http.Request, Operation) (AccessDecision, error) {
		return AccessDecision{}, nil
	})
	newRunID := func() string { return "configured-run" }
	opts := RuntimeOptions{
		Access:         access,
		SSE:            sse.Options{RetryMillis: 731},
		Manager:        RunManagerOptions{MaxActive: 3, TerminalRetention: -1, NewRunID: newRunID},
		ApprovalBuffer: 7,
		LiveBuffer:     19,
	}

	runtime, err := NewRuntime(zenforge.Config{Model: runtimeTestModel{}}, durable, opts)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if runtime.Agent == nil || runtime.Manager == nil || runtime.Handler == nil {
		t.Fatal("runtime did not expose agent, manager, and handler")
	}
	if runtime.Events.Store != durable || runtime.Events.Bus != runtime.Bus {
		t.Fatal("fanout store does not use the supplied durable store and shared bus")
	}
	if runtime.Handler.Agent != runtime.Agent ||
		runtime.Handler.Manager != runtime.Manager ||
		runtime.Handler.Events != runtime.Events ||
		runtime.Handler.Bus != runtime.Bus ||
		runtime.Handler.ApprovalInbox != runtime.ApprovalInbox ||
		runtime.Handler.Approvals != runtime.Approvals {
		t.Fatal("handler does not use the runtime's shared components")
	}
	if runtime.Manager.agent != runtime.Agent ||
		runtime.Manager.events != runtime.Events ||
		runtime.Manager.bus != runtime.Bus {
		t.Fatal("manager does not use the runtime's shared components")
	}
	if runtime.Handler.Access == nil || runtime.Handler.SSE.RetryMillis != 731 || runtime.Handler.LiveBuffer != 19 {
		t.Fatal("handler options were not applied")
	}
	if runtime.Manager.opts.MaxActive != 3 ||
		runtime.Manager.opts.TerminalRetention != -1 ||
		runtime.Manager.opts.NewRunID() != "configured-run" {
		t.Fatal("manager options were not applied")
	}

	result, err := runtime.Agent.Run(context.Background(), zenforge.Task{RunID: "wired", Input: "hello"})
	if err != nil {
		t.Fatalf("run assembled agent: %v", err)
	}
	if result.Output != "configured-model" {
		t.Fatalf("output = %q, want configured-model", result.Output)
	}
	events, err := durable.Read(context.Background(), "wired", 0, 0)
	if err != nil {
		t.Fatalf("read durable events: %v", err)
	}
	if len(events) == 0 || !runtime.Bus.RunClosed("wired") {
		t.Fatal("agent events did not pass through the shared fanout store and bus")
	}
}

func TestNewRuntimeUsesApplicationDurableApprovalInbox(t *testing.T) {
	store := approvalmemory.NewStore()
	inbox, err := approval.NewStoreBroker(store, approval.StoreBrokerOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(
		zenforge.Config{Model: runtimeTestModel{}},
		eventlogmemory.New(),
		RuntimeOptions{ApprovalInbox: inbox},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close(context.Background()) }()
	if runtime.ApprovalInbox != inbox || runtime.Handler.ApprovalInbox != inbox {
		t.Fatal("custom approval inbox was not shared with the handler")
	}
	if runtime.Approvals != nil || runtime.Handler.Approvals != nil {
		t.Fatal("custom durable inbox was misreported as a PendingBroker")
	}

	var typedNil *approval.StoreBroker
	if _, err := NewRuntime(
		zenforge.Config{},
		eventlogmemory.New(),
		RuntimeOptions{ApprovalInbox: typedNil},
	); err == nil {
		t.Fatal("typed-nil approval inbox accepted")
	}
}

func TestNewRuntimeRejectsInvalidInputs(t *testing.T) {
	var typedNil *runtimeTestStore
	var typedNilRegistry *MemoryRunRegistry
	tests := []struct {
		name    string
		store   eventlog.Store
		options RuntimeOptions
	}{
		{name: "nil durable store"},
		{name: "typed nil durable store", store: typedNil},
		{name: "negative approval buffer", store: eventlogmemory.New(), options: RuntimeOptions{ApprovalBuffer: -1}},
		{name: "negative live buffer", store: eventlogmemory.New(), options: RuntimeOptions{LiveBuffer: -1}},
		{name: "typed nil run registry", store: eventlogmemory.New(), options: RuntimeOptions{Manager: RunManagerOptions{Registry: typedNilRegistry}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRuntime(zenforge.Config{}, test.store, test.options); err == nil {
				t.Fatal("NewRuntime returned nil error")
			}
		})
	}
}

func TestRuntimeCloseStopsManagerWithoutClosingDurableStore(t *testing.T) {
	durable := &runtimeTestStore{Store: eventlogmemory.New()}
	runtime, err := NewRuntime(
		zenforge.Config{Model: blockingRuntimeTestModel{}},
		durable,
		RuntimeOptions{Manager: RunManagerOptions{TerminalRetention: -1}},
	)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if _, err := runtime.Manager.Start(context.Background(), zenforge.Task{RunID: "active", Input: "wait"}); err != nil {
		t.Fatalf("start detached run: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := runtime.Manager.Start(context.Background(), zenforge.Task{Input: "closed"}); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start after close = %v, want ErrManagerClosed", err)
	}
	if durable.closeCalls.Load() != 0 {
		t.Fatalf("durable close calls = %d, want 0", durable.closeCalls.Load())
	}
	if err := durable.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunDone, "caller-owned", nil)); err != nil {
		t.Fatalf("durable store unusable after runtime close: %v", err)
	}
}

func TestRuntimeDetachedHITLSurvivesDisconnectAndReplays(t *testing.T) {
	runtime, err := NewRuntime(
		zenforge.Config{
			Model: &runtimeHITLModel{},
			Tools: []zenforge.Tool{runtimeApprovalTool{}},
		},
		eventlogmemory.New(),
		RuntimeOptions{Manager: RunManagerOptions{TerminalRetention: -1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close(context.Background()) }()

	start := httptest.NewRecorder()
	runtime.Handler.ServeDetachedStart(start, httptest.NewRequest(
		http.MethodPost,
		"/runs",
		strings.NewReader(`{"runId":"hitl","input":"run the approved tool"}`),
	))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d; body=%s", start.Code, start.Body.String())
	}
	waitPendingApproval(t, runtime.Approvals, "runtime_approval")
	waitStatus(t, runtime.Manager, "hitl", RunWaitingApproval)

	attachCtx, disconnect := context.WithCancel(context.Background())
	events, errs, err := runtime.Manager.Attach(attachCtx, "hitl", 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("detached attachment did not replay")
	}
	disconnect()
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatalf("disconnect returned follow error: %v", err)
	}
	if _, ok := runtime.Approvals.Pending("runtime_approval"); !ok {
		t.Fatal("client disconnect removed pending approval")
	}

	approve := httptest.NewRecorder()
	runtime.Handler.ServeApproval(approve, httptest.NewRequest(
		http.MethodPost,
		"/approval",
		strings.NewReader(`{"requestId":"runtime_approval","action":"approve","scope":"once"}`),
	))
	if approve.Code != http.StatusOK {
		t.Fatalf("approval status = %d; body=%s", approve.Code, approve.Body.String())
	}
	waitStatus(t, runtime.Manager, "hitl", RunCompleted)

	replayed, replayErrs, err := runtime.Manager.Attach(context.Background(), "hitl", 0)
	if err != nil {
		t.Fatal(err)
	}
	var lastSeq int64
	var sawRequested, sawResolved, sawToolResult, sawDone bool
	for event := range replayed {
		if event.Seq != lastSeq+1 {
			t.Fatalf("event sequence jumped from %d to %d", lastSeq, event.Seq)
		}
		lastSeq = event.Seq
		switch event.Type {
		case zenforge.EventApprovalRequested:
			sawRequested = true
		case zenforge.EventApprovalResolved:
			sawResolved = true
		case zenforge.EventToolResult:
			sawToolResult = true
		case zenforge.EventRunDone:
			sawDone = true
		}
	}
	if err := <-replayErrs; err != nil {
		t.Fatal(err)
	}
	if !sawRequested || !sawResolved || !sawToolResult || !sawDone {
		t.Fatalf("replay lifecycle requested=%t resolved=%t tool=%t done=%t", sawRequested, sawResolved, sawToolResult, sawDone)
	}
}

type runtimeTestModel struct{}

func (runtimeTestModel) Generate(context.Context, model.Request) (*model.Response, error) {
	return &model.Response{Message: model.Message{Role: "assistant", Content: "configured-model"}}, nil
}

func (runtimeTestModel) Stream(ctx context.Context, _ model.Request) (<-chan model.Event, error) {
	events := make(chan model.Event, 2)
	events <- model.Event{Type: model.EventDelta, Delta: "configured-model"}
	events <- model.Event{Type: model.EventDone, Message: &model.Message{Role: "assistant", Content: "configured-model"}}
	close(events)
	return events, ctx.Err()
}

type blockingRuntimeTestModel struct{}

func (blockingRuntimeTestModel) Generate(ctx context.Context, _ model.Request) (*model.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingRuntimeTestModel) Stream(ctx context.Context, _ model.Request) (<-chan model.Event, error) {
	events := make(chan model.Event)
	go func() {
		defer close(events)
		<-ctx.Done()
	}()
	return events, nil
}

type runtimeHITLModel struct {
	calls atomic.Int32
}

func (m *runtimeHITLModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	response := &model.Response{}
	for event := range stream {
		if event.Message != nil {
			response.Message = *event.Message
		}
		response.Message.Content += event.Delta
	}
	return response, nil
}

func (m *runtimeHITLModel) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	out := make(chan model.Event, 1)
	if m.calls.Add(1) == 1 {
		out <- model.Event{Message: &model.Message{
			Role: "assistant",
			ToolCalls: []model.ToolCallSpec{{
				ID:        "runtime_call",
				Name:      "runtime_approval_tool",
				Arguments: json.RawMessage(`{}`),
			}},
		}}
	} else {
		out <- model.Event{Delta: "approved and complete"}
	}
	close(out)
	return out, nil
}

type runtimeApprovalTool struct{}

func (runtimeApprovalTool) Name() string { return "runtime_approval_tool" }

func (runtimeApprovalTool) Description() string { return "requires an operator decision" }

func (runtimeApprovalTool) Schema() map[string]any { return nil }

func (runtimeApprovalTool) Call(_ context.Context, _ json.RawMessage, call tool.Context) (tool.Result, error) {
	if approval.IsApprovedAction(call.Metadata[approval.MetadataDecisionAction]) {
		return tool.Result{Output: "approved"}, nil
	}
	request := approval.Request{
		ID:         "runtime_approval",
		RunID:      call.RunID,
		ToolCallID: call.ToolCallID,
		ToolName:   "runtime_approval_tool",
		Operation:  "runtime.test",
		Title:      "Approve runtime test",
		Risk:       approval.RiskMedium,
		Options:    approval.DefaultOptions(),
		CreatedAt:  time.Now().UTC(),
	}
	return approval.RequiredResult(request), approval.ErrRequired
}

type runtimeTestStore struct {
	eventlog.Store
	closeCalls atomic.Int32
}

func (s *runtimeTestStore) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func waitPendingApproval(t *testing.T, broker *approval.PendingBroker, requestID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := broker.Pending(requestID); ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("approval %q did not become pending", requestID)
}

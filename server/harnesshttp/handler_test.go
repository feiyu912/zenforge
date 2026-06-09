package harnesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/server/sse"
)

func TestServeRunStreamsEvents(t *testing.T) {
	agent := &fakeAgent{}
	handler := New(agent, sse.Options{RetryMillis: 500})
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"runId":"run_http","input":" hello ","meta":{"source":"test"}}`))
	rec := httptest.NewRecorder()

	handler.ServeRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if agent.streamTask.RunID != "run_http" || agent.streamTask.Input != "hello" || agent.streamTask.Meta["source"] != "test" {
		t.Fatalf("unexpected task passed to Stream: %#v", agent.streamTask)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "retry: 500\n\n") || !strings.Contains(body, "event: run.done\n") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
}

func TestServeRunAuthorizesAndInjectsTrustedMeta(t *testing.T) {
	agent := &fakeAgent{}
	handler := New(agent, sse.Options{})
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "run" || operation.RunID != "run_http" {
			return AccessDecision{}, fmt.Errorf("unexpected operation: %#v", operation)
		}
		if r.Header.Get("Authorization") != "Bearer ok" {
			return AccessDecision{}, ErrUnauthorized
		}
		return AccessDecision{Meta: map[string]any{"tenantId": "tenant_1", "source": "trusted"}}, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"runId":"run_http","input":"hello","meta":{"source":"client"}}`))
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()

	handler.ServeRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if agent.streamTask.Meta["tenantId"] != "tenant_1" || agent.streamTask.Meta["source"] != "trusted" {
		t.Fatalf("trusted meta was not injected with precedence: %#v", agent.streamTask.Meta)
	}
}

func TestServeRunRejectsUnauthorized(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		return AccessDecision{}, ErrUnauthorized
	})
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"input":"hello"}`))
	rec := httptest.NewRecorder()

	handler.ServeRun(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestServeRunRejectsInvalidRequest(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"input":"   "}`))
	rec := httptest.NewRecorder()

	handler.ServeRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "input_required") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestServeRunRequiresPost(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	req := httptest.NewRequest(http.MethodGet, "/run", nil)
	rec := httptest.NewRecorder()

	handler.ServeRun(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandlersRejectUnsupportedMethods(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		serve  func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{name: "run", method: http.MethodGet, path: "/run", serve: (*Handler).ServeRun},
		{name: "resume", method: http.MethodDelete, path: "/resume", serve: (*Handler).ServeResume},
		{name: "events", method: http.MethodPost, path: "/events", serve: (*Handler).ServeEvents},
		{name: "live", method: http.MethodPost, path: "/live", serve: (*Handler).ServeLiveEvents},
		{name: "approval", method: http.MethodGet, path: "/approval", serve: (*Handler).ServeApproval},
		{name: "approvals", method: http.MethodPost, path: "/approvals", serve: (*Handler).ServeApprovals},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(nil, sse.Options{})
			rec := httptest.NewRecorder()

			tc.serve(handler, rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "method_not_allowed") {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
		})
	}
}

func TestServeResumeStreamsGETAndPOST(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{name: "GET", req: httptest.NewRequest(http.MethodGet, "/resume?runId=run_http", nil)},
		{name: "POST", req: httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{"runId":"run_http"}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := &fakeAgent{}
			handler := New(agent, sse.Options{})
			rec := httptest.NewRecorder()

			handler.ServeResume(rec, tc.req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if agent.resumeRunID != "run_http" {
				t.Fatalf("resume runID = %q", agent.resumeRunID)
			}
			if !strings.Contains(rec.Body.String(), "event: run.resumed\n") {
				t.Fatalf("unexpected SSE body: %q", rec.Body.String())
			}
		})
	}
}

func TestServeResumeAuthorizesRunID(t *testing.T) {
	agent := &fakeAgent{}
	handler := New(agent, sse.Options{})
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name == "resume" && operation.RunID == "run_http" {
			return AccessDecision{}, nil
		}
		return AccessDecision{}, ErrForbidden
	})
	rec := httptest.NewRecorder()

	handler.ServeResume(rec, httptest.NewRequest(http.MethodGet, "/resume?runId=run_http", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if agent.resumeRunID != "run_http" {
		t.Fatalf("resume runID = %q", agent.resumeRunID)
	}
}

func TestServeResumeRejectsMissingRunID(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	req := httptest.NewRequest(http.MethodGet, "/resume", nil)
	rec := httptest.NewRecorder()

	handler.ServeResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "run_id_required") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestServeResumeRejectsInvalidPostJSON(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	req := httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{`))
	rec := httptest.NewRecorder()

	handler.ServeResume(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_json") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestServeEventsReplaysStoredEvents(t *testing.T) {
	store := &fakeEventStore{
		events: []zenforge.Event{
			zenforge.NewEvent(zenforge.EventRunStarted, "run_http", map[string]any{"input": "hi"}).WithSeq(1),
			zenforge.NewEvent(zenforge.EventRunDone, "run_http", map[string]any{"output": "done"}).WithSeq(2),
		},
	}
	handler := New(&fakeAgent{}, sse.Options{RetryMillis: 250})
	handler.Events = store
	req := httptest.NewRequest(http.MethodGet, "/events?runId=run_http&afterSeq=1&limit=1", nil)
	rec := httptest.NewRecorder()

	handler.ServeEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.runID != "run_http" || store.afterSeq != 1 || store.limit != 1 {
		t.Fatalf("unexpected Read args: runID=%q afterSeq=%d limit=%d", store.runID, store.afterSeq, store.limit)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "retry: 250\n\n") || !strings.Contains(body, "event: run.done\n") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
	if strings.Contains(body, "event: run.started\n") {
		t.Fatalf("afterSeq filter was not applied: %q", body)
	}
}

func TestServeEventsRejectsForbidden(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Events = &fakeEventStore{}
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "events" || operation.RunID != "run_http" {
			t.Fatalf("unexpected operation: %#v", operation)
		}
		return AccessDecision{}, ErrForbidden
	})
	rec := httptest.NewRecorder()

	handler.ServeEvents(rec, httptest.NewRequest(http.MethodGet, "/events?runId=run_http", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestServeEventsRejectsInvalidQuery(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "negative_after_seq", url: "/events?runId=run_http&afterSeq=-1", want: "invalid_after_seq"},
		{name: "invalid_after_seq", url: "/events?runId=run_http&afterSeq=soon", want: "invalid_after_seq"},
		{name: "negative_limit", url: "/events?runId=run_http&limit=-1", want: "invalid_limit"},
		{name: "invalid_limit", url: "/events?runId=run_http&limit=many", want: "invalid_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(&fakeAgent{}, sse.Options{})
			handler.Events = &fakeEventStore{}
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()

			handler.ServeEvents(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("unexpected error body: %s", rec.Body.String())
			}
		})
	}
}

func TestServeEventsRequiresStoreAndRunID(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	rec := httptest.NewRecorder()

	handler.ServeEvents(rec, httptest.NewRequest(http.MethodGet, "/events?runId=run_http", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	handler.Events = &fakeEventStore{}
	rec = httptest.NewRecorder()
	handler.ServeEvents(rec, httptest.NewRequest(http.MethodGet, "/events", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeLiveEventsStreamsBusEvents(t *testing.T) {
	bus := eventlog.NewBus()
	handler := New(&fakeAgent{}, sse.Options{RetryMillis: 750})
	handler.Bus = bus
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "liveEvents" || operation.RunID != "run_http" {
			return AccessDecision{}, fmt.Errorf("unexpected operation: %#v", operation)
		}
		return AccessDecision{}, nil
	})
	req := httptest.NewRequest(http.MethodGet, "/live?runId=run_http", nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeLiveEvents(rec, req)
		close(done)
	}()

	event := zenforge.NewEvent(zenforge.EventRunDone, "run_http", map[string]any{"output": "done"}).WithSeq(3)
	publishErrs := make(chan error, 1)
	go func() {
		time.Sleep(5 * time.Millisecond)
		for i := 0; i < 100; i++ {
			if err := bus.Publish(context.Background(), event); err != nil {
				publishErrs <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
		bus.CloseRun("run_http")
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for live event stream")
	}
	select {
	case err := <-publishErrs:
		t.Fatalf("Publish returned error: %v", err)
	default:
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "retry: 750\n\n") || !strings.Contains(body, "id: 3\n") || !strings.Contains(body, "event: run.done\n") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
}

func TestServeLiveEventsRejectsForbiddenRun(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Bus = eventlog.NewBus()
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "liveEvents" || operation.RunID != "run_http" {
			t.Fatalf("unexpected operation: %#v", operation)
		}
		return AccessDecision{}, ErrForbidden
	})
	rec := httptest.NewRecorder()

	handler.ServeLiveEvents(rec, httptest.NewRequest(http.MethodGet, "/live?runId=run_http", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestServeLiveEventsRejectsInvalidBuffer(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Bus = eventlog.NewBus()
	handler.LiveBuffer = -1
	rec := httptest.NewRecorder()

	handler.ServeLiveEvents(rec, httptest.NewRequest(http.MethodGet, "/live?runId=run_http", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_live_buffer") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestServeLiveEventsRequiresBusAndRunID(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	rec := httptest.NewRecorder()

	handler.ServeLiveEvents(rec, httptest.NewRequest(http.MethodGet, "/live?runId=run_http", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	handler.Bus = eventlog.NewBus()
	rec = httptest.NewRecorder()
	handler.ServeLiveEvents(rec, httptest.NewRequest(http.MethodGet, "/live", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeApprovalSubmitsPendingDecision(t *testing.T) {
	broker := approval.NewPendingBroker(1)
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Approvals = broker
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "approval" || operation.RunID != "run_http" {
			return AccessDecision{}, fmt.Errorf("unexpected operation: %#v", operation)
		}
		return AccessDecision{}, nil
	})
	result := make(chan approval.Decision, 1)
	errs := make(chan error, 1)
	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		decision, err := broker.Request(waitCtx, approval.Request{
			ID:        "approval_http",
			RunID:     "run_http",
			Operation: "shell.command",
			Title:     "Approve command",
			Risk:      approval.RiskHigh,
			Options:   approval.DefaultOptions(),
		})
		if err != nil {
			errs <- err
			return
		}
		result <- decision
	}()
	<-broker.Requests()

	rec := httptest.NewRecorder()
	handler.ServeApproval(rec, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(`{"requestId":"approval_http","action":"approve"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	select {
	case err := <-errs:
		t.Fatalf("broker returned error: %v", err)
	case decision := <-result:
		if decision.RequestID != "approval_http" || decision.Action != approval.DecisionApprove {
			t.Fatalf("unexpected decision: %#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for submitted decision")
	}
	if !strings.Contains(rec.Body.String(), `"requestId":"approval_http"`) {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
}

func TestServeApprovalRejectsUnknownRequest(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Approvals = approval.NewPendingBroker(0)
	rec := httptest.NewRecorder()

	handler.ServeApproval(rec, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(`{"requestId":"missing","action":"approve"}`)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "approval_not_found") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestServeApprovalRejectsInvalidJSONAndDecision(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "invalid_json", body: `{`, want: "invalid_json"},
		{name: "invalid_approval", body: `{"action":"approve"}`, want: "invalid_approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(&fakeAgent{}, sse.Options{})
			handler.Approvals = approval.NewPendingBroker(0)
			rec := httptest.NewRecorder()

			handler.ServeApproval(rec, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
		})
	}
}

func TestServeApprovalRejectsUnconfiguredBroker(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	rec := httptest.NewRecorder()

	handler.ServeApproval(rec, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(`{"requestId":"approval_http","action":"approve"}`)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "approval_broker_not_configured") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestServeApprovalAuthorizesPendingRun(t *testing.T) {
	broker := approval.NewPendingBroker(1)
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Approvals = broker
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.RunID != "run_http" {
			t.Fatalf("operation runID = %q", operation.RunID)
		}
		return AccessDecision{}, ErrForbidden
	})
	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = broker.Request(waitCtx, approval.Request{
			ID:        "approval_http",
			RunID:     "run_http",
			Operation: "shell.command",
			Title:     "Approve command",
			Risk:      approval.RiskHigh,
			Options:   approval.DefaultOptions(),
		})
	}()
	<-broker.Requests()
	rec := httptest.NewRecorder()

	handler.ServeApproval(rec, httptest.NewRequest(http.MethodPost, "/approval", strings.NewReader(`{"requestId":"approval_http","action":"approve"}`)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if _, ok := broker.Pending("approval_http"); !ok {
		t.Fatalf("pending request should remain after forbidden submit")
	}
}

func TestServeApprovalsListsPendingRequestsForRun(t *testing.T) {
	broker := approval.NewPendingBroker(2)
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Approvals = broker
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "approvals" || operation.RunID != "run_http" {
			return AccessDecision{}, fmt.Errorf("unexpected operation: %#v", operation)
		}
		return AccessDecision{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPendingApproval(t, ctx, broker, "approval_http", "run_http")
	startPendingApproval(t, ctx, broker, "approval_other", "run_other")
	rec := httptest.NewRecorder()

	handler.ServeApprovals(rec, httptest.NewRequest(http.MethodGet, "/approvals?runId=run_http", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"approval_http"`) || strings.Contains(body, `"id":"approval_other"`) {
		t.Fatalf("unexpected approvals body: %s", body)
	}
}

func TestServeApprovalsRejectsForbiddenRun(t *testing.T) {
	broker := approval.NewPendingBroker(1)
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Approvals = broker
	handler.Access = AccessFunc(func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
		if operation.Name != "approvals" || operation.RunID != "run_http" {
			t.Fatalf("unexpected operation: %#v", operation)
		}
		return AccessDecision{}, ErrForbidden
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPendingApproval(t, ctx, broker, "approval_http", "run_http")
	rec := httptest.NewRecorder()

	handler.ServeApprovals(rec, httptest.NewRequest(http.MethodGet, "/approvals?runId=run_http", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestServeApprovalsRequiresBrokerAndRunID(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	rec := httptest.NewRecorder()

	handler.ServeApprovals(rec, httptest.NewRequest(http.MethodGet, "/approvals?runId=run_http", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	handler.Approvals = approval.NewPendingBroker(0)
	rec = httptest.NewRecorder()
	handler.ServeApprovals(rec, httptest.NewRequest(http.MethodGet, "/approvals", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeRunReportsAgentError(t *testing.T) {
	handler := New(&fakeAgent{streamErr: errors.New("boom")}, sse.Options{})
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"input":"hello"}`))
	rec := httptest.NewRecorder()

	handler.ServeRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "run_failed") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func startPendingApproval(t *testing.T, ctx context.Context, broker *approval.PendingBroker, requestID, runID string) {
	t.Helper()
	go func() {
		_, _ = broker.Request(ctx, approval.Request{
			ID:        requestID,
			RunID:     runID,
			Operation: "shell.command",
			Title:     "Approve command",
			Risk:      approval.RiskHigh,
			Options:   approval.DefaultOptions(),
		})
	}()
	select {
	case <-broker.Requests():
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for pending approval %s", requestID)
	}
}

type fakeEventStore struct {
	events   []zenforge.Event
	runID    string
	afterSeq int64
	limit    int
}

func (s *fakeEventStore) Append(ctx context.Context, event zenforge.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *fakeEventStore) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error) {
	s.runID = runID
	s.afterSeq = afterSeq
	s.limit = limit
	var out []zenforge.Event
	for _, event := range s.events {
		if event.RunID() != runID || event.Seq <= afterSeq {
			continue
		}
		out = append(out, event)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *fakeEventStore) LatestSeq(ctx context.Context, runID string) (int64, error) {
	var latest int64
	for _, event := range s.events {
		if event.RunID() == runID && event.Seq > latest {
			latest = event.Seq
		}
	}
	return latest, nil
}

type fakeAgent struct {
	streamTask  zenforge.Task
	streamErr   error
	resumeRunID string
	resumeErr   error
}

func (a *fakeAgent) Stream(ctx context.Context, task zenforge.Task) (<-chan zenforge.Event, error) {
	a.streamTask = task
	if a.streamErr != nil {
		return nil, a.streamErr
	}
	events := make(chan zenforge.Event, 1)
	events <- zenforge.NewEvent(zenforge.EventRunDone, task.RunID, map[string]any{"output": "done"}).WithSeq(1)
	close(events)
	return events, nil
}

func (a *fakeAgent) Resume(ctx context.Context, runID string) (<-chan zenforge.Event, error) {
	a.resumeRunID = runID
	if a.resumeErr != nil {
		return nil, a.resumeErr
	}
	events := make(chan zenforge.Event, 1)
	events <- zenforge.NewEvent(zenforge.EventRunResumed, runID, map[string]any{"input": "resume"}).WithSeq(1)
	close(events)
	return events, nil
}

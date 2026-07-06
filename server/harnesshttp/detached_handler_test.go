package harnesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/server/sse"
)

func TestDetachedHandlersRejectUnsupportedMethods(t *testing.T) {
	handler := &Handler{}
	tests := []struct {
		name   string
		method string
		serve  http.HandlerFunc
	}{
		{"start", http.MethodGet, handler.ServeDetachedStart},
		{"resume", http.MethodPut, handler.ServeDetachedResume},
		{"status", http.MethodPost, handler.ServeDetachedStatus},
		{"attach", http.MethodPost, handler.ServeDetachedAttach},
		{"cancel", http.MethodGet, handler.ServeDetachedCancel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			test.serve(rec, httptest.NewRequest(test.method, "/detached", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDetachedStartAuthorizationMetaAndStatus(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	var operation Operation
	handler := &Handler{
		Manager: manager,
		Access: AccessFunc(func(_ context.Context, _ *http.Request, op Operation) (AccessDecision, error) {
			operation = op
			return AccessDecision{Meta: map[string]any{"tenant": "trusted"}}, nil
		}),
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/detached/start", strings.NewReader(
		`{"runId":"run_http","input":" hello ","meta":{"tenant":"client"}}`,
	))
	handler.ServeDetachedStart(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var info RunInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.RunID != "run_http" || info.Status != RunStarting {
		t.Fatalf("info = %+v", info)
	}
	if operation != (Operation{Name: "detachedStart", RunID: "run_http"}) {
		t.Fatalf("operation = %+v", operation)
	}

	statusRec := httptest.NewRecorder()
	handler.ServeDetachedStatus(statusRec, httptest.NewRequest(http.MethodGet, "/detached/status?runId=run_http", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status response = %d; body=%s", statusRec.Code, statusRec.Body.String())
	}
	agent.finish("run_http", zenforge.EventRunDone)
	waitStatus(t, manager, "run_http", RunCompleted)
}

func TestDetachedAuthorizationFailureDoesNotStart(t *testing.T) {
	manager, _, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	handler := &Handler{
		Manager: manager,
		Access: AccessFunc(func(context.Context, *http.Request, Operation) (AccessDecision, error) {
			return AccessDecision{}, ErrForbidden
		}),
	}
	rec := httptest.NewRecorder()
	handler.ServeDetachedStart(rec, httptest.NewRequest(http.MethodPost, "/detached/start",
		strings.NewReader(`{"runId":"denied","input":"hello"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if _, err := manager.Get("denied"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Get denied run = %v", err)
	}
}

func TestDetachedValidationAndErrorMappings(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{
		MaxActive:         1,
		TerminalRetention: -1,
	})
	defer closeManager(t, manager)
	handler := &Handler{Manager: manager}

	assertHTTPError(t, handler.ServeDetachedStart, http.MethodPost, "/detached/start", `{`, http.StatusBadRequest)
	assertHTTPError(t, handler.ServeDetachedStart, http.MethodPost, "/detached/start", `{"input":" "}`, http.StatusBadRequest)
	assertHTTPError(t, handler.ServeDetachedStatus, http.MethodGet, "/detached/status", "", http.StatusBadRequest)
	assertHTTPError(t, handler.ServeDetachedStatus, http.MethodGet, "/detached/status?runId=missing", "", http.StatusNotFound)
	assertHTTPError(t, handler.ServeDetachedResume, http.MethodGet, "/detached/resume?runId=missing", "", http.StatusNotFound)

	startDetached(t, handler, "active")
	assertHTTPError(t, handler.ServeDetachedStart, http.MethodPost, "/detached/start",
		`{"runId":"active","input":"hello"}`, http.StatusConflict)
	assertHTTPError(t, handler.ServeDetachedStart, http.MethodPost, "/detached/start",
		`{"runId":"limited","input":"hello"}`, http.StatusTooManyRequests)

	cancelRec := httptest.NewRecorder()
	handler.ServeDetachedCancel(cancelRec, httptest.NewRequest(http.MethodDelete, "/detached/cancel?runId=active", nil))
	if cancelRec.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d; body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	waitStatus(t, manager, "active", RunCancelled)
	if !agent.cancelled("active") {
		t.Fatal("cancel did not reach the detached run context")
	}
	startDetached(t, handler, "post_cancel")
	postCancelRec := httptest.NewRecorder()
	handler.ServeDetachedCancel(postCancelRec, httptest.NewRequest(http.MethodPost, "/detached/cancel",
		strings.NewReader(`{"runId":"post_cancel"}`)))
	if postCancelRec.Code != http.StatusAccepted {
		t.Fatalf("POST cancel status = %d; body=%s", postCancelRec.Code, postCancelRec.Body.String())
	}
	waitStatus(t, manager, "post_cancel", RunCancelled)
	assertHTTPError(t, handler.ServeDetachedCancel, http.MethodPost, "/detached/cancel",
		`{"runId":"missing"}`, http.StatusNotFound)
}

func TestDetachedManagerUnavailableMappings(t *testing.T) {
	handler := &Handler{}
	assertHTTPError(t, handler.ServeDetachedStatus, http.MethodGet, "/detached/status?runId=x", "", http.StatusServiceUnavailable)

	manager, _, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler.Manager = manager
	assertHTTPError(t, handler.ServeDetachedStart, http.MethodPost, "/detached/start",
		`{"runId":"closed","input":"hello"}`, http.StatusServiceUnavailable)
}

func TestDetachedResumeAndAttachContinuity(t *testing.T) {
	manager, agent, store := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	handler := &Handler{Manager: manager, SSE: sse.Options{}}

	for _, event := range []zenforge.Event{
		zenforge.NewEvent(zenforge.EventRunStarted, "resume_http", nil),
		zenforge.NewEvent(zenforge.EventModelDelta, "resume_http", map[string]any{"textDelta": "kept"}),
	} {
		if err := store.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	resumeRec := httptest.NewRecorder()
	handler.ServeDetachedResume(resumeRec, httptest.NewRequest(http.MethodGet,
		"/detached/resume?runId=resume_http", nil))
	if resumeRec.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d; body=%s", resumeRec.Code, resumeRec.Body.String())
	}
	agent.finish("resume_http", zenforge.EventRunDone)
	waitStatus(t, manager, "resume_http", RunCompleted)

	if err := store.Append(context.Background(),
		zenforge.NewEvent(zenforge.EventRunStarted, "resume_post", nil)); err != nil {
		t.Fatal(err)
	}
	postResumeRec := httptest.NewRecorder()
	handler.ServeDetachedResume(postResumeRec, httptest.NewRequest(http.MethodPost, "/detached/resume",
		strings.NewReader(`{"runId":"resume_post"}`)))
	if postResumeRec.Code != http.StatusAccepted {
		t.Fatalf("POST resume status = %d; body=%s", postResumeRec.Code, postResumeRec.Body.String())
	}
	agent.finish("resume_post", zenforge.EventRunDone)
	waitStatus(t, manager, "resume_post", RunCompleted)

	attachRec := httptest.NewRecorder()
	attachReq := httptest.NewRequest(http.MethodGet, "/detached/attach?runId=resume_http", nil)
	attachReq.Header.Set("Last-Event-ID", "1")
	handler.ServeDetachedAttach(attachRec, attachReq)
	body := attachRec.Body.String()
	if strings.Contains(body, "id: 1\n") || !strings.Contains(body, "id: 2\n") || !strings.Contains(body, "id: 3\n") {
		t.Fatalf("Last-Event-ID continuity mismatch: %q", body)
	}

	overrideRec := httptest.NewRecorder()
	overrideReq := httptest.NewRequest(http.MethodGet,
		"/detached/attach?runId=resume_http&afterSeq=0", nil)
	overrideReq.Header.Set("Last-Event-ID", "3")
	handler.ServeDetachedAttach(overrideRec, overrideReq)
	if !strings.Contains(overrideRec.Body.String(), "id: 1\n") {
		t.Fatalf("afterSeq did not override Last-Event-ID: %q", overrideRec.Body.String())
	}

	assertHTTPError(t, handler.ServeDetachedAttach, http.MethodGet,
		"/detached/attach?runId=resume_http&afterSeq=-1", "", http.StatusBadRequest)
	badHeader := httptest.NewRequest(http.MethodGet, "/detached/attach?runId=resume_http", nil)
	badHeader.Header.Set("Last-Event-ID", "bad")
	badRec := httptest.NewRecorder()
	handler.ServeDetachedAttach(badRec, badHeader)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid Last-Event-ID status = %d", badRec.Code)
	}
}

func TestDetachedAttachWriterFailureStopsFollowerNotRun(t *testing.T) {
	manager, agent, store := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	handler := &Handler{Manager: manager}
	startDetached(t, handler, "disconnect")
	agent.send("disconnect", zenforge.NewEvent(zenforge.EventRunStarted, "disconnect", nil))
	waitLatest(t, store, "disconnect", 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeDetachedAttach(&failingResponseWriter{header: make(http.Header)},
			httptest.NewRequest(http.MethodGet, "/detached/attach?runId=disconnect", nil))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attach handler blocked after its writer failed")
	}
	if agent.cancelled("disconnect") {
		t.Fatal("attachment failure cancelled the managed run")
	}
	info, err := manager.Get("disconnect")
	if err != nil || (info.Status != RunStarting && info.Status != RunRunning) {
		t.Fatalf("run after attachment failure = (%+v, %v)", info, err)
	}
	agent.finish("disconnect", zenforge.EventRunDone)
	waitStatus(t, manager, "disconnect", RunCompleted)
}

type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (*failingResponseWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func (*failingResponseWriter) WriteHeader(int) {}

func startDetached(t *testing.T, handler *Handler, runID string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeDetachedStart(rec, httptest.NewRequest(http.MethodPost, "/detached/start",
		strings.NewReader(`{"runId":"`+runID+`","input":"hello"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start %s status = %d; body=%s", runID, rec.Code, rec.Body.String())
	}
}

func assertHTTPError(
	t *testing.T,
	serve http.HandlerFunc,
	method string,
	target string,
	body string,
	want int,
) {
	t.Helper()
	rec := httptest.NewRecorder()
	serve(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	if rec.Code != want {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, target, rec.Code, want, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
}

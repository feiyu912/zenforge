package harnesshttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
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

func TestServeEventsRejectsInvalidQuery(t *testing.T) {
	handler := New(&fakeAgent{}, sse.Options{})
	handler.Events = &fakeEventStore{}
	req := httptest.NewRequest(http.MethodGet, "/events?runId=run_http&afterSeq=-1", nil)
	rec := httptest.NewRecorder()

	handler.ServeEvents(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_after_seq") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
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

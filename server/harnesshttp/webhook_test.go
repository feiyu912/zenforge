package harnesshttp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
)

const (
	webhookTestSecret = "webhook-test-secret"
	webhookTestRunID  = "webhook_run"
)

// webhookTestNow is fixed so the replay-window checks are deterministic and no
// test has to sleep on the wall clock.
var webhookTestNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestWebhookRunStartsSignedRun(t *testing.T) {
	handler, agent := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	body := []byte(`{"prompt":" deploy the release ","metadata":{"source":"ci"}}`)
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, signedWebhookRequest(t, webhookTestSecret, webhookTestNow, body))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var response WebhookRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.RunID != webhookTestRunID || response.Status != "queued" {
		t.Fatalf("response = %+v", response)
	}
	info, err := handler.Manager.Get(webhookTestRunID)
	if err != nil || info.Status != RunStarting {
		t.Fatalf("started run = (%+v, %v)", info, err)
	}
	agent.finish(webhookTestRunID, zenforge.EventRunDone)
	waitStatus(t, handler.Manager, webhookTestRunID, RunCompleted)
}

func TestWebhookRunRouteRegisteredWithSecret(t *testing.T) {
	handler, agent := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	mux := http.NewServeMux()
	handler.RegisterWebhookRun(mux)
	body := []byte(`{"prompt":"triggered through the mux"}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, signedWebhookRequest(t, webhookTestSecret, webhookTestNow, body))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	agent.finish(webhookTestRunID, zenforge.EventRunDone)
	waitStatus(t, handler.Manager, webhookTestRunID, RunCompleted)
}

func TestWebhookRunRejectsWrongSignatureAndStartsNoRun(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	body := []byte(`{"prompt":"deploy"}`)
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, signedWebhookRequest(t, "not-the-secret", webhookTestNow, body))

	assertWebhookUnauthorized(t, rec)
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRejectsMissingSignature(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	body := []byte(`{"prompt":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, WebhookRunPath, bytes.NewReader(body))
	req.Header.Set(WebhookTimestampHeader, webhookTimestamp(webhookTestNow))
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, req)

	assertWebhookUnauthorized(t, rec)
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRejectsMissingOrUnparsableTimestamp(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	body := []byte(`{"prompt":"deploy"}`)
	tests := []struct {
		name      string
		timestamp string
	}{
		{name: "missing"},
		{name: "unparsable", timestamp: "not-a-timestamp"},
		{name: "fractional seconds", timestamp: "1767323045.5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, WebhookRunPath, bytes.NewReader(body))
			if test.timestamp != "" {
				req.Header.Set(WebhookTimestampHeader, test.timestamp)
			}
			req.Header.Set(WebhookSignatureHeader, "sha256="+webhookSignature(webhookTestSecret, test.timestamp, body))
			rec := httptest.NewRecorder()
			handler.ServeWebhookRun(rec, req)
			assertWebhookUnauthorized(t, rec)
		})
	}
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRejectsStaleTimestampEvenWithValidSignature(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	// Ten minutes old: outside the five-minute window, but correctly signed for
	// that timestamp and body. The prefix is what makes this combination fail.
	stale := webhookTestNow.Add(-10 * time.Minute)
	body := []byte(`{"prompt":"deploy"}`)
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, signedWebhookRequest(t, webhookTestSecret, stale, body))

	assertWebhookUnauthorized(t, rec)
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRejectsFutureTimestampEvenWithValidSignature(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	future := webhookTestNow.Add(2 * time.Minute)
	body := []byte(`{"prompt":"deploy"}`)
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, signedWebhookRequest(t, webhookTestSecret, future, body))

	assertWebhookUnauthorized(t, rec)
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRejectsSignatureWithoutTimestampPrefix(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	body := []byte(`{"prompt":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, WebhookRunPath, bytes.NewReader(body))
	req.Header.Set(WebhookTimestampHeader, webhookTimestamp(webhookTestNow))
	// A valid body signature that ignores the timestamp: accepting it would let
	// a captured signature be replayed with any fresh timestamp.
	mac := hmac.New(sha256.New, []byte(webhookTestSecret))
	mac.Write(body)
	req.Header.Set(WebhookSignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, req)

	assertWebhookUnauthorized(t, rec)
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunAcceptsBareHexSignature(t *testing.T) {
	handler, agent := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	body := []byte(`{"prompt":"deploy"}`)
	timestamp := webhookTimestamp(webhookTestNow)
	req := httptest.NewRequest(http.MethodPost, WebhookRunPath, bytes.NewReader(body))
	req.Header.Set(WebhookTimestampHeader, timestamp)
	req.Header.Set(WebhookSignatureHeader, webhookSignature(webhookTestSecret, timestamp, body))
	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	agent.finish(webhookTestRunID, zenforge.EventRunDone)
	waitStatus(t, handler.Manager, webhookTestRunID, RunCompleted)
}

func TestWebhookRunRejectsMalformedJSONAndEmptyPrompt(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	tests := []struct {
		name string
		body string
	}{
		{name: "malformed json", body: `{`},
		{name: "empty prompt", body: `{"prompt":"   "}`},
		{name: "missing prompt", body: `{"metadata":{"source":"ci"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeWebhookRun(rec, signedWebhookRequest(t, webhookTestSecret, webhookTestNow, []byte(test.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
			}
		})
	}
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRouteAbsentWithoutSecret(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, "")
	defer closeManager(t, handler.Manager)

	mux := http.NewServeMux()
	handler.RegisterWebhookRun(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, WebhookRunPath,
		bytes.NewReader([]byte(`{"prompt":"unauthenticated"}`))))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	assertNoWebhookRun(t, handler)
}

func TestWebhookRunRejectsNonPostMethod(t *testing.T) {
	handler, _ := newWebhookTestHandler(t, webhookTestSecret)
	defer closeManager(t, handler.Manager)

	rec := httptest.NewRecorder()
	handler.ServeWebhookRun(rec, httptest.NewRequest(http.MethodGet, WebhookRunPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
	assertNoWebhookRun(t, handler)
}

func newWebhookTestHandler(t *testing.T, secret string) (*Handler, *managerTestAgent) {
	t.Helper()
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{
		TerminalRetention: -1,
		NewRunID:          func() string { return webhookTestRunID },
	})
	return &Handler{
		Manager: manager,
		Webhook: WebhookOptions{Secret: secret, Now: func() time.Time { return webhookTestNow }},
	}, agent
}

func signedWebhookRequest(t *testing.T, secret string, at time.Time, body []byte) *http.Request {
	t.Helper()
	timestamp := webhookTimestamp(at)
	req := httptest.NewRequest(http.MethodPost, WebhookRunPath, bytes.NewReader(body))
	req.Header.Set(WebhookTimestampHeader, timestamp)
	req.Header.Set(WebhookSignatureHeader, "sha256="+webhookSignature(secret, timestamp, body))
	return req
}

func webhookTimestamp(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10)
}

func webhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func assertWebhookUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func assertNoWebhookRun(t *testing.T, handler *Handler) {
	t.Helper()
	if _, err := handler.Manager.Get(webhookTestRunID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("webhook started a run: Get = %v", err)
	}
}

package modelretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/model"
)

func statusError(code int, body string) *model.HTTPStatusError {
	return model.NewHTTPStatusError("openai", "chat completion", "https://api.example/v1/chat", code, http.StatusText(code), body)
}

func TestClassifyHTTPStatusCodes(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		wantCode  Code
		retryable bool
	}{
		{429, `{"error":"rate limited"}`, CodeRateLimit, true},
		{408, ``, CodeTimeout, true},
		{500, `internal`, CodeServer, true},
		{502, `bad gateway`, CodeServer, true},
		{503, `unavailable`, CodeServer, true},
		{504, `gateway timeout`, CodeServer, true},
		{401, `invalid key`, CodeAuth, false},
		{403, `forbidden`, CodeAuth, false},
		{402, `payment required`, CodeQuota, false},
		{400, `{"error":"bad request"}`, CodeRequestInvalid, false},
		{404, `not found`, CodeRequestInvalid, false},
		{422, `unprocessable`, CodeRequestInvalid, false},
		{413, `too large`, CodeContextWindow, false},
		{400, `{"error":{"code":"context_length_exceeded"}}`, CodeContextWindow, false},
		{400, `This model's maximum context length is 8192 tokens`, CodeContextWindow, false},
	}
	for _, tc := range cases {
		failure := Classify(statusError(tc.status, tc.body))
		if failure.Code != tc.wantCode || failure.Retryable != tc.retryable {
			t.Errorf("Classify(status %d, %q) = %+v, want code %s retryable %v", tc.status, tc.body, failure, tc.wantCode, tc.retryable)
		}
	}
}

func TestClassifyPreservesRetryAfter(t *testing.T) {
	err := statusError(429, `slow down`)
	err.RetryAfter = 30 * time.Second
	failure := Classify(err)
	if failure.Code != CodeRateLimit || !failure.Retryable {
		t.Fatalf("Classify(429) = %+v", failure)
	}
	if failure.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", failure.RetryAfter)
	}
}

type fakeNetError struct{ timeout bool }

func (e fakeNetError) Error() string   { return "fake network failure" }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return true }

func TestClassifyTransportErrors(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  Code
		retryable bool
	}{
		{"canceled", context.Canceled, CodeCanceled, false},
		{"wrapped canceled", fmt.Errorf("stream aborted: %w", context.Canceled), CodeCanceled, false},
		{"deadline", context.DeadlineExceeded, CodeTimeout, true},
		{"net timeout", fakeNetError{timeout: true}, CodeTimeout, true},
		{"net error", fakeNetError{}, CodeTransport, true},
		{"url error", &url.Error{Op: "Post", URL: "https://api.example", Err: errors.New("dial failed")}, CodeTransport, true},
		{"unexpected eof", io.ErrUnexpectedEOF, CodeTransport, true},
		{"connection reset text", errors.New("read tcp: connection reset by peer"), CodeTransport, true},
		{"wrapped status", fmt.Errorf("model call: %w", statusError(503, "down")), CodeServer, true},
		{"idle stream", &model.StreamIdleError{Idle: 5 * time.Second}, CodeTimeout, true},
		{"unknown", errors.New("something odd"), CodeUnknown, false},
		{"nil", nil, CodeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := Classify(tc.err)
			if failure.Code != tc.wantCode || failure.Retryable != tc.retryable {
				t.Fatalf("Classify(%v) = %+v, want code %s retryable %v", tc.err, failure, tc.wantCode, tc.retryable)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"30", 30 * time.Second, true},
		{" 45 ", 45 * time.Second, true},
		{"0", 0, true},
		{"-5", 0, false},
		{"", 0, false},
		{"garbage", 0, false},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
	}
	for _, tc := range cases {
		got, ok := model.ParseRetryAfter(tc.value, now)
		if ok != tc.wantOK {
			t.Fatalf("ParseRetryAfter(%q) ok = %v, want %v", tc.value, ok, tc.wantOK)
		}
		if ok && (got < tc.want-time.Second || got > tc.want+time.Second) {
			t.Fatalf("ParseRetryAfter(%q) = %v, want about %v", tc.value, got, tc.want)
		}
	}
}

func TestConfigWithDefaults(t *testing.T) {
	resolved, err := Config{}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	if resolved.MaxRetries != DefaultMaxRetries || resolved.InitialDelay != DefaultInitialDelay ||
		resolved.MaxDelay != DefaultMaxDelay || resolved.Jitter != DefaultJitter ||
		resolved.MaxRetryAfterDelay != DefaultMaxRetryAfterDelay {
		t.Fatalf("defaults not applied: %+v", resolved)
	}
	invalid := []Config{
		{MaxRetries: -1},
		{InitialDelay: -time.Second},
		{InitialDelay: time.Second, MaxDelay: 500 * time.Millisecond},
		{Jitter: 1.5},
		{Jitter: -0.1},
		{MaxRetryAfterDelay: -time.Second},
	}
	for _, config := range invalid {
		if _, err := config.WithDefaults(); err == nil {
			t.Fatalf("WithDefaults(%+v) accepted invalid config", config)
		}
	}
}

func TestDelaySchedule(t *testing.T) {
	config, err := Config{InitialDelay: 500 * time.Millisecond, MaxDelay: 10 * time.Second}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	config.Jitter = 0
	failure := Failure{Code: CodeServer, Retryable: true}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second}
	for retry, expected := range want {
		if got := config.Delay(retry, failure); got != expected {
			t.Fatalf("Delay(%d) = %v, want %v", retry, got, expected)
		}
	}
}

func TestDelayHonorsRetryAfterWithinCap(t *testing.T) {
	config, err := Config{InitialDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond, MaxRetryAfterDelay: 100 * time.Millisecond}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	config.Jitter = 0
	advised := Failure{Code: CodeRateLimit, Retryable: true, RetryAfter: 40 * time.Millisecond}
	if got := config.Delay(0, advised); got != 40*time.Millisecond {
		t.Fatalf("Delay with Retry-After 40ms = %v, want 40ms", got)
	}
	capped := Failure{Code: CodeRateLimit, Retryable: true, RetryAfter: time.Hour}
	if got := config.Delay(0, capped); got != 100*time.Millisecond {
		t.Fatalf("Delay with huge Retry-After = %v, want cap 100ms", got)
	}
	short := Failure{Code: CodeRateLimit, Retryable: true, RetryAfter: time.Millisecond}
	if got := config.Delay(0, short); got != 10*time.Millisecond {
		t.Fatalf("Delay with tiny Retry-After = %v, want backoff 10ms", got)
	}
}

func TestDelayJitterStaysInBounds(t *testing.T) {
	config, err := Config{InitialDelay: time.Second, MaxDelay: time.Second}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	failure := Failure{Code: CodeServer, Retryable: true}
	for i := 0; i < 100; i++ {
		got := config.Delay(0, failure)
		if got < 900*time.Millisecond || got > 1100*time.Millisecond {
			t.Fatalf("jittered delay %v outside +/-10%% of 1s", got)
		}
	}
}

func TestShouldRetry(t *testing.T) {
	config, err := Config{MaxRetries: 2}.WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults returned error: %v", err)
	}
	retryable := Failure{Code: CodeServer, Retryable: true}
	if !config.ShouldRetry(0, retryable) || !config.ShouldRetry(1, retryable) {
		t.Fatalf("retryable failure refused within budget")
	}
	if config.ShouldRetry(2, retryable) {
		t.Fatalf("retry allowed past MaxRetries")
	}
	if config.ShouldRetry(0, Failure{Code: CodeAuth}) {
		t.Fatalf("non-retryable failure allowed")
	}
}

var _ net.Error = fakeNetError{}

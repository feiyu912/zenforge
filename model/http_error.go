package model

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPStatusError reports a non-successful response from a model endpoint.
// It keeps configuration diagnostics useful without retaining credentials.
type HTTPStatusError struct {
	Provider   string
	Operation  string
	Endpoint   string
	StatusCode int
	Status     string
	Response   string
	// RetryAfter carries provider-advised delay parsed from a Retry-After
	// response header, when present. Zero means the provider gave no
	// advice. Retry middleware honors it within its own caps.
	RetryAfter time.Duration
}

func NewHTTPStatusError(provider, operation, endpoint string, statusCode int, status, response string) *HTTPStatusError {
	return &HTTPStatusError{
		Provider:   provider,
		Operation:  operation,
		Endpoint:   safeEndpoint(endpoint),
		StatusCode: statusCode,
		Status:     status,
		Response:   strings.TrimSpace(response),
	}
}

func (e *HTTPStatusError) Error() string {
	status := strings.TrimSpace(e.Status)
	if status == "" {
		status = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	message := fmt.Sprintf("%s %s failed: %s", e.Provider, e.Operation, status)
	if e.Endpoint != "" {
		message += fmt.Sprintf(" (endpoint %s; %s)", e.Endpoint, e.guidance())
	}
	if e.Response != "" {
		message += ": " + e.Response
	}
	return message
}

func (e *HTTPStatusError) guidance() string {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication failed; verify the API key belongs to this BaseURL and that the selected provider protocol matches the endpoint"
	case http.StatusNotFound:
		return "endpoint was not found; verify BaseURL is the API root for the selected provider protocol"
	case http.StatusTooManyRequests:
		return "rate limited or out of quota; retry later or check the provider account"
	default:
		return "check the provider response and request configuration"
	}
}

func safeEndpoint(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// RedactSecret removes a configured credential from text that may be surfaced
// in a provider response. Empty values are left unchanged.
func RedactSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}

// StreamIdleError reports a model stream that produced no events within a
// configured idle window. It is a timeout classification: retry middleware
// treats a stalled connection as retryable instead of hanging the run.
type StreamIdleError struct {
	Idle time.Duration
}

func (e *StreamIdleError) Error() string {
	return fmt.Sprintf("model stream idle timeout: no events received for %s", e.Idle)
}

// Timeout marks the error as a timeout for net.Error-style classification.
func (e *StreamIdleError) Timeout() bool { return true }

// ParseRetryAfter interprets a Retry-After header value, which is either
// delta-seconds or an HTTP-date. It returns the advised delay relative to
// now and reports whether the value was usable. Negative or unparseable
// values are rejected so retry middleware never waits on garbage.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := date.Sub(now)
		if delay < 0 {
			return 0, true
		}
		return delay, true
	}
	return 0, false
}

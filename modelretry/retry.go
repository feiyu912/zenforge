// Package modelretry classifies model-call failures and computes retry
// schedules for the durable agent loop.
//
// The design follows the retry layers of the reference harnesses:
//
//   - deepseek-harness dsh-llm-retry: a stable failure-code taxonomy
//     (rate limit, server, timeout, transport, empty response) with
//     exponential backoff, bounded jitter, provider Retry-After advice,
//     and a durable retry event emitted BEFORE each wait so a resumed
//     session can see why the loop paused. Context-window exhaustion is
//     deliberately NOT retryable here: the compaction subsystem owns it.
//   - openai/codex util.rs/responses_retry.rs: exponential backoff with
//     multiplicative jitter, separate request and stream budgets, and a
//     stream idle timeout that classifies a stalled connection as a
//     retryable timeout instead of hanging the run.
//
// Classification fails closed: an error that matches no known retryable
// shape is reported as unknown and is not retried, so a genuine
// configuration or request bug surfaces immediately instead of being
// masked by five identical failures.
package modelretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/model"
)

// Code is a stable classification of one model-call failure. The string
// values appear verbatim in model.retry event payloads, so they are part
// of the observable event vocabulary.
type Code string

const (
	// CodeRateLimit is a provider rate-limit rejection (HTTP 429).
	CodeRateLimit Code = "rate_limit"
	// CodeServer is a provider-side failure (HTTP 5xx).
	CodeServer Code = "server_error"
	// CodeTimeout is a request or stream timeout, including the stream
	// idle watchdog.
	CodeTimeout Code = "timeout"
	// CodeTransport is a connection-level failure: DNS, TCP reset, TLS
	// handshake, or a truncated response body.
	CodeTransport Code = "transport"
	// CodeEmptyResponse is a stream that completed without any assistant
	// text or tool calls.
	CodeEmptyResponse Code = "empty_response"
	// CodeContextWindow is a context-length rejection. It is never
	// retryable by this package: compaction owns the recovery.
	CodeContextWindow Code = "context_window_exceeded"
	// CodeAuth is an authentication or authorization failure (401/403).
	CodeAuth Code = "auth"
	// CodeQuota is a billing or quota exhaustion (402).
	CodeQuota Code = "quota"
	// CodeRequestInvalid is a request the provider rejected as malformed
	// or misconfigured (other 4xx). Retrying cannot fix it.
	CodeRequestInvalid Code = "request_invalid"
	// CodeCanceled is caller cancellation, which is a decision, not a
	// failure.
	CodeCanceled Code = "canceled"
	// CodeUnknown is anything unclassified. Unknown failures are not
	// retried.
	CodeUnknown Code = "unknown"
)

// Failure is the retry classification of one model-call error.
type Failure struct {
	// Code is the stable failure classification.
	Code Code
	// Retryable reports whether a retry can plausibly succeed.
	Retryable bool
	// RetryAfter carries provider-advised delay from a Retry-After
	// header when one was present. Zero means no advice.
	RetryAfter time.Duration
}

// Defaults mirror the reference harnesses: five retries, 500ms initial
// delay doubling to a 10s cap, +/-10% jitter.
const (
	DefaultMaxRetries         = 5
	DefaultInitialDelay       = 500 * time.Millisecond
	DefaultMaxDelay           = 10 * time.Second
	DefaultJitter             = 0.1
	DefaultMaxRetryAfterDelay = 120 * time.Second
)

// Config tunes the retry schedule. The zero value is invalid; call
// WithDefaults to fill in reference defaults.
type Config struct {
	// MaxRetries is the number of retries after the first attempt.
	MaxRetries int
	// InitialDelay is the backoff before the first retry.
	InitialDelay time.Duration
	// MaxDelay caps the computed backoff.
	MaxDelay time.Duration
	// Jitter is the symmetric fraction applied to each delay, in [0, 1).
	Jitter float64
	// MaxRetryAfterDelay caps honored provider Retry-After advice so a
	// hostile or confused header cannot stall the run for hours.
	MaxRetryAfterDelay time.Duration
}

// WithDefaults fills unset fields with the reference defaults and
// validates the result.
func (c Config) WithDefaults() (Config, error) {
	out := c
	if out.MaxRetries == 0 {
		out.MaxRetries = DefaultMaxRetries
	}
	if out.InitialDelay == 0 {
		out.InitialDelay = DefaultInitialDelay
	}
	if out.MaxDelay == 0 {
		out.MaxDelay = DefaultMaxDelay
	}
	if out.Jitter == 0 {
		out.Jitter = DefaultJitter
	}
	if out.MaxRetryAfterDelay == 0 {
		out.MaxRetryAfterDelay = DefaultMaxRetryAfterDelay
	}
	if out.MaxRetries < 0 {
		return Config{}, errors.New("model retry maxRetries must be non-negative")
	}
	if out.InitialDelay < 0 {
		return Config{}, errors.New("model retry initialDelay must be non-negative")
	}
	if out.MaxDelay < out.InitialDelay {
		return Config{}, errors.New("model retry maxDelay must be at least initialDelay")
	}
	if out.Jitter < 0 || out.Jitter >= 1 {
		return Config{}, errors.New("model retry jitter must be in [0, 1)")
	}
	if out.MaxRetryAfterDelay <= 0 {
		return Config{}, errors.New("model retry maxRetryAfterDelay must be positive")
	}
	return out, nil
}

// ShouldRetry reports whether another attempt is allowed. retriesDone is
// the number of retries already performed for the current model call.
func (c Config) ShouldRetry(retriesDone int, failure Failure) bool {
	return failure.Retryable && retriesDone < c.MaxRetries
}

// Delay computes the wait before the next retry. retriesDone is the
// number of retries already performed (0 for the first retry): the base
// schedule is InitialDelay * 2^retriesDone capped at MaxDelay, then
// jittered by +/-Jitter. Provider Retry-After advice raises the delay up
// to MaxRetryAfterDelay but never lowers the computed backoff.
func (c Config) Delay(retriesDone int, failure Failure) time.Duration {
	backoff := c.InitialDelay
	for i := 0; i < retriesDone; i++ {
		backoff *= 2
		if backoff <= 0 || backoff > c.MaxDelay {
			backoff = c.MaxDelay
			break
		}
	}
	if backoff > c.MaxDelay {
		backoff = c.MaxDelay
	}
	delay := backoff
	if c.Jitter > 0 {
		factor := 1 + (rand.Float64()*2-1)*c.Jitter
		delay = time.Duration(float64(backoff) * factor)
	}
	if failure.RetryAfter > 0 {
		advised := failure.RetryAfter
		if advised > c.MaxRetryAfterDelay {
			advised = c.MaxRetryAfterDelay
		}
		if advised > delay {
			delay = advised
		}
	}
	return delay
}

// Classify maps an error from a model call onto the failure taxonomy.
// Context cancellation is never retryable; unknown shapes fail closed.
func Classify(err error) Failure {
	if err == nil {
		return Failure{Code: CodeUnknown}
	}
	if errors.Is(err, context.Canceled) {
		return Failure{Code: CodeCanceled}
	}

	var statusErr *model.HTTPStatusError
	if errors.As(err, &statusErr) {
		return classifyStatus(statusErr)
	}

	var idleErr *model.StreamIdleError
	if errors.As(err, &idleErr) {
		return Failure{Code: CodeTimeout, Retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Failure{Code: CodeTimeout, Retryable: true}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return Failure{Code: CodeTimeout, Retryable: true}
		}
		return Failure{Code: CodeTransport, Retryable: true}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return Failure{Code: CodeTransport, Retryable: true}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return Failure{Code: CodeTransport, Retryable: true}
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"connection reset", "broken pipe", "tls handshake", "eof",
		"connection refused", "no such host", "server misbehaving",
	} {
		if strings.Contains(message, fragment) {
			return Failure{Code: CodeTransport, Retryable: true}
		}
	}
	return Failure{Code: CodeUnknown}
}

func classifyStatus(err *model.HTTPStatusError) Failure {
	failure := Failure{RetryAfter: err.RetryAfter}
	switch {
	case err.StatusCode == http.StatusTooManyRequests:
		failure.Code, failure.Retryable = CodeRateLimit, true
	case err.StatusCode == http.StatusRequestTimeout:
		failure.Code, failure.Retryable = CodeTimeout, true
	case err.StatusCode >= 500:
		failure.Code, failure.Retryable = CodeServer, true
	case err.StatusCode == http.StatusUnauthorized, err.StatusCode == http.StatusForbidden:
		failure.Code = CodeAuth
	case err.StatusCode == http.StatusPaymentRequired:
		failure.Code = CodeQuota
	case err.StatusCode == http.StatusRequestEntityTooLarge:
		failure.Code = CodeContextWindow
	case strings.Contains(strings.ToLower(err.Response), "context_length_exceeded") ||
		strings.Contains(strings.ToLower(err.Response), "maximum context length"):
		failure.Code = CodeContextWindow
	case err.StatusCode >= 400:
		failure.Code = CodeRequestInvalid
	default:
		failure.Code = CodeUnknown
	}
	return failure
}

// EmptyResponseFailure is the classification used when a stream completes
// without assistant text or tool calls.
func EmptyResponseFailure() Failure {
	return Failure{Code: CodeEmptyResponse, Retryable: true}
}

// Describe renders a failure for event payloads and wrapped errors.
func (f Failure) Describe(err error) string {
	if err == nil {
		return string(f.Code)
	}
	return fmt.Sprintf("%s: %v", f.Code, err)
}

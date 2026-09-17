package model

import (
	"strconv"
	"strings"
	"time"
)

// ParseRateLimits normalizes provider rate-limit response headers into a
// snapshot. The get function is typically http.Header.Get. Both the
// OpenAI-style x-ratelimit-* headers (Go duration reset values) and the
// Anthropic-style anthropic-ratelimit-* headers (RFC 3339 reset
// timestamps) are understood; the result is nil when neither family is
// present. Unparsable values are skipped, never fatal — a rate-limit
// snapshot is advisory telemetry.
func ParseRateLimits(get func(string) string, now time.Time) *RateLimit {
	if get == nil {
		return nil
	}
	limits := &RateLimit{}
	found := false
	found = parseIntHeader(get, "x-ratelimit-limit-requests", &limits.RequestsLimit) || found
	found = parseIntHeader(get, "x-ratelimit-remaining-requests", &limits.RequestsRemaining) || found
	found = parseResetHeader(get, "x-ratelimit-reset-requests", now, &limits.RequestsReset) || found
	found = parseIntHeader(get, "x-ratelimit-limit-tokens", &limits.TokensLimit) || found
	found = parseIntHeader(get, "x-ratelimit-remaining-tokens", &limits.TokensRemaining) || found
	found = parseResetHeader(get, "x-ratelimit-reset-tokens", now, &limits.TokensReset) || found

	found = parseIntHeader(get, "anthropic-ratelimit-requests-limit", &limits.RequestsLimit) || found
	found = parseIntHeader(get, "anthropic-ratelimit-requests-remaining", &limits.RequestsRemaining) || found
	found = parseResetHeader(get, "anthropic-ratelimit-requests-reset", now, &limits.RequestsReset) || found
	found = parseIntHeader(get, "anthropic-ratelimit-tokens-limit", &limits.TokensLimit) || found
	found = parseIntHeader(get, "anthropic-ratelimit-tokens-remaining", &limits.TokensRemaining) || found
	found = parseResetHeader(get, "anthropic-ratelimit-tokens-reset", now, &limits.TokensReset) || found
	if !found {
		return nil
	}
	return limits
}

func parseIntHeader(get func(string) string, name string, out *int) bool {
	value := strings.TrimSpace(get(name))
	if value == "" {
		return false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	*out = parsed
	return true
}

// parseResetHeader accepts a Go duration ("1.5s", "2m"), a plain
// non-negative number of seconds, or an RFC 3339 timestamp, and stores
// the time remaining clamped to zero.
func parseResetHeader(get func(string) string, name string, now time.Time, out *time.Duration) bool {
	value := strings.TrimSpace(get(name))
	if value == "" {
		return false
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration < 0 {
			duration = 0
		}
		*out = duration
		return true
	}
	if stamp, err := time.Parse(time.RFC3339, value); err == nil {
		until := stamp.Sub(now)
		if until < 0 {
			until = 0
		}
		*out = until
		return true
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		*out = time.Duration(seconds * float64(time.Second))
		return true
	}
	return false
}

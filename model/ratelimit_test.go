package model

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimitsOpenAIHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("x-ratelimit-limit-requests", "500")
	header.Set("x-ratelimit-remaining-requests", "499")
	header.Set("x-ratelimit-reset-requests", "1.5s")
	header.Set("x-ratelimit-limit-tokens", "150000")
	header.Set("x-ratelimit-remaining-tokens", "149000")
	header.Set("x-ratelimit-reset-tokens", "2m")

	limits := ParseRateLimits(header.Get, time.Now())
	if limits == nil {
		t.Fatal("ParseRateLimits returned nil for OpenAI headers")
	}
	if limits.RequestsLimit != 500 || limits.RequestsRemaining != 499 || limits.RequestsReset != 1500*time.Millisecond {
		t.Fatalf("request dimensions = %+v", limits)
	}
	if limits.TokensLimit != 150000 || limits.TokensRemaining != 149000 || limits.TokensReset != 2*time.Minute {
		t.Fatalf("token dimensions = %+v", limits)
	}
}

func TestParseRateLimitsAnthropicHeaders(t *testing.T) {
	now := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	header := http.Header{}
	header.Set("anthropic-ratelimit-requests-limit", "2000")
	header.Set("anthropic-ratelimit-requests-remaining", "1999")
	header.Set("anthropic-ratelimit-requests-reset", now.Add(90*time.Second).Format(time.RFC3339))
	header.Set("anthropic-ratelimit-tokens-limit", "80000")
	header.Set("anthropic-ratelimit-tokens-remaining", "79000")
	header.Set("anthropic-ratelimit-tokens-reset", now.Add(-5*time.Second).Format(time.RFC3339))

	limits := ParseRateLimits(header.Get, now)
	if limits == nil {
		t.Fatal("ParseRateLimits returned nil for Anthropic headers")
	}
	if limits.RequestsLimit != 2000 || limits.RequestsRemaining != 1999 || limits.RequestsReset != 90*time.Second {
		t.Fatalf("request dimensions = %+v", limits)
	}
	// A past reset timestamp clamps to zero instead of going negative.
	if limits.TokensReset != 0 || limits.TokensRemaining != 79000 {
		t.Fatalf("token dimensions = %+v", limits)
	}
}

func TestParseRateLimitsAbsentOrMalformed(t *testing.T) {
	if limits := ParseRateLimits(http.Header{}.Get, time.Now()); limits != nil {
		t.Fatalf("empty headers produced %+v", limits)
	}
	if limits := ParseRateLimits(nil, time.Now()); limits != nil {
		t.Fatalf("nil getter produced %+v", limits)
	}
	malformed := http.Header{}
	malformed.Set("x-ratelimit-limit-requests", "not-a-number")
	malformed.Set("x-ratelimit-reset-requests", "soon")
	if limits := ParseRateLimits(malformed.Get, time.Now()); limits != nil {
		t.Fatalf("malformed headers produced %+v", limits)
	}

	// Plain non-negative seconds are accepted as a reset fallback.
	seconds := http.Header{}
	seconds.Set("x-ratelimit-reset-tokens", "45")
	limits := ParseRateLimits(seconds.Get, time.Now())
	if limits == nil || limits.TokensReset != 45*time.Second {
		t.Fatalf("seconds fallback = %+v", limits)
	}
}

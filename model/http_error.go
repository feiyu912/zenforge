package model

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/anthropic"
	"github.com/feiyu912/zenforge/model/openai"
	"github.com/feiyu912/zenforge/model/provider"
)

func TestNewSupportedProtocols(t *testing.T) {
	tests := []struct {
		protocol string
		wantType any
	}{
		{protocol: "openai", wantType: (*openai.Client)(nil)},
		{protocol: "ANTHROPIC", wantType: (*anthropic.Client)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			got, err := provider.New(provider.Config{
				Protocol: tt.protocol,
				APIKey:   "secret",
				Model:    "test-model",
			})
			if err != nil {
				t.Fatalf("New returned error: %v", err)
			}
			switch tt.wantType.(type) {
			case *openai.Client:
				if _, ok := got.(*openai.Client); !ok {
					t.Fatalf("New returned %T, want *openai.Client", got)
				}
			case *anthropic.Client:
				if _, ok := got.(*anthropic.Client); !ok {
					t.Fatalf("New returned %T, want *anthropic.Client", got)
				}
			}
		})
	}
}

func TestNewValidationDoesNotExposeAPIKey(t *testing.T) {
	secret := "do-not-print-this"
	tests := []struct {
		name   string
		config provider.Config
		want   string
	}{
		{name: "unknown", config: provider.Config{Protocol: "minimax", APIKey: secret, Model: "m"}, want: "unknown model provider: minimax"},
		{name: "missing key", config: provider.Config{Protocol: "openai", Model: "m"}, want: "openai API key is required"},
		{name: "missing model", config: provider.Config{Protocol: "anthropic", APIKey: secret}, want: "anthropic model is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.New(tt.config)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("New error = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error exposes API key: %v", err)
			}
		})
	}
}

func TestFromEnvDefaultsAndPrefix(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("OPENAI_MODEL", "openai-model")
	got, err := provider.FromEnv(provider.Config{Protocol: "openai"})
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}
	if _, ok := got.(*openai.Client); !ok {
		t.Fatalf("FromEnv returned %T, want *openai.Client", got)
	}

	t.Setenv("LOCAL_API_KEY", "local-key")
	t.Setenv("LOCAL_MODEL", "local-model")
	got, err = provider.FromEnv(provider.Config{Protocol: "anthropic", EnvPrefix: "LOCAL_"})
	if err != nil {
		t.Fatalf("FromEnv with prefix returned error: %v", err)
	}
	if _, ok := got.(*anthropic.Client); !ok {
		t.Fatalf("FromEnv returned %T, want *anthropic.Client", got)
	}
}

func TestFromEnvZenforgeVariables(t *testing.T) {
	t.Setenv("ZENFORGE_PROVIDER", "anthropic")
	t.Setenv("ZENFORGE_API_KEY", "zenforge-key")
	t.Setenv("ZENFORGE_MODEL", "claude-test")
	got, err := provider.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}
	if _, ok := got.(*anthropic.Client); !ok {
		t.Fatalf("FromEnv returned %T, want *anthropic.Client", got)
	}
}

func TestFromEnvMissingValues(t *testing.T) {
	t.Setenv("ZENFORGE_PROVIDER", " ")
	if _, err := provider.FromEnv(); err == nil || err.Error() != "ZENFORGE_PROVIDER is not set" {
		t.Fatalf("missing provider error = %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	if _, err := provider.FromEnv(provider.Config{Protocol: "anthropic"}); err == nil || err.Error() != "ANTHROPIC_API_KEY is not set" {
		t.Fatalf("missing key error = %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "key")
	if _, err := provider.FromEnv(provider.Config{Protocol: "anthropic"}); err == nil || err.Error() != "ANTHROPIC_MODEL is not set" {
		t.Fatalf("missing model error = %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "   ")
	if _, err := provider.FromEnv(provider.Config{Protocol: "anthropic", Model: "model"}); err == nil || err.Error() != "ANTHROPIC_API_KEY is not set" {
		t.Fatalf("whitespace key error = %v", err)
	}
}

func TestFromEnvAnthropicBaseURLIsAPIRoot(t *testing.T) {
	var requestPath string
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestPath = r.URL.Path
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")),
		}, nil
	})}

	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ANTHROPIC_MODEL", "claude-test")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic.example/v1/")
	client, err := provider.FromEnv(provider.Config{Protocol: "anthropic", HTTPClient: httpClient})
	if err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}
	events, err := client.Stream(context.Background(), model.Request{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	if requestPath != "/v1/messages" {
		t.Fatalf("request path = %q, want /v1/messages", requestPath)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

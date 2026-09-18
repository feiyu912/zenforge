package cli

import (
	"strings"
	"testing"
)

func TestCLIModelResolverKeepsTheHostConfigurationForItsOwnProvider(t *testing.T) {
	// No environment key: resolution succeeds only if the host's explicit key
	// is the one used, which is how a config-file key keeps working.
	t.Setenv("OPENAI_API_KEY", "")
	resolver := cliModelResolver{opts: options{
		provider: "openai", model: "gpt-host", apiKey: "host-key", baseURL: "https://host.example",
	}}
	if _, err := resolver.Resolve("", "gpt-other"); err != nil {
		t.Fatalf("Resolve with an empty provider returned error: %v", err)
	}
	if _, err := resolver.Resolve("openai", "gpt-other"); err != nil {
		t.Fatalf("Resolve for the host's own provider returned error: %v", err)
	}
	if _, err := resolver.Resolve("OpenAI", "gpt-other"); err != nil {
		t.Fatalf("provider comparison should be case-insensitive: %v", err)
	}
}

func TestCLIModelResolverFallsBackToTheHostModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	withoutModel := cliModelResolver{opts: options{provider: "openai", apiKey: "host-key"}}
	if _, err := withoutModel.Resolve("", ""); err == nil || !strings.Contains(err.Error(), "MODEL") {
		t.Fatalf("an unnamed model without a host model should not resolve: %v", err)
	}
	withModel := cliModelResolver{opts: options{provider: "openai", model: "gpt-host", apiKey: "host-key"}}
	if _, err := withModel.Resolve("", ""); err != nil {
		t.Fatalf("the host model should have been used: %v", err)
	}
}

func TestCLIModelResolverReadsAnotherProvidersEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	resolver := cliModelResolver{opts: options{provider: "openai", model: "gpt-host", apiKey: "host-key"}}
	if _, err := resolver.Resolve("anthropic", "claude-test"); err == nil {
		t.Fatal("a second provider without credentials resolved")
	} else if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("error should name the missing environment variable: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "second-key")
	if _, err := resolver.Resolve("anthropic", "claude-test"); err != nil {
		t.Fatalf("a second provider with credentials returned error: %v", err)
	}
	// A provider this binary does not know about stays an error, not a
	// fallback to the host's protocol.
	if _, err := resolver.Resolve("nonsense", "model"); err == nil {
		t.Fatal("an unknown provider resolved")
	}
}

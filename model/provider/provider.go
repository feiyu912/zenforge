// Package provider constructs supported model adapters from explicit or
// environment-based configuration.
package provider

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/anthropic"
	"github.com/feiyu912/zenforge/model/openai"
)

const (
	OpenAI    = "openai"
	Anthropic = "anthropic"
)

// Config configures a model provider. The Env fields are used only by FromEnv.
// Explicit APIKey, BaseURL, and Model values take precedence over environment
// variables.
type Config struct {
	Protocol string
	APIKey   string
	BaseURL  string
	Model    string

	EnvPrefix  string
	APIKeyEnv  string
	BaseURLEnv string
	ModelEnv   string

	HTTPClient *http.Client
}

// New constructs a model adapter from explicit configuration.
func New(config Config) (model.Model, error) {
	protocol, err := normalizeProtocol(config.Protocol)
	if err != nil {
		return nil, err
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.Model = strings.TrimSpace(config.Model)
	if config.APIKey == "" {
		return nil, fmt.Errorf("%s API key is required", protocol)
	}
	if config.Model == "" {
		return nil, fmt.Errorf("%s model is required", protocol)
	}

	switch protocol {
	case OpenAI:
		return openai.New(openai.Config{
			APIKey:     config.APIKey,
			BaseURL:    config.BaseURL,
			Model:      config.Model,
			HTTPClient: config.HTTPClient,
		}), nil
	case Anthropic:
		// BaseURL is the API root. The adapter appends /messages itself.
		return anthropic.New(anthropic.Config{
			APIKey:     config.APIKey,
			BaseURL:    config.BaseURL,
			Model:      config.Model,
			HTTPClient: config.HTTPClient,
		}), nil
	default:
		panic("unreachable")
	}
}

// FromEnv constructs a model adapter from environment variables. With no
// config it reads ZENFORGE_PROVIDER and the other ZENFORGE_* variables. A
// Config with Protocol set uses that protocol's native prefix by default.
func FromEnv(configs ...Config) (model.Model, error) {
	if len(configs) > 1 {
		return nil, fmt.Errorf("provider.FromEnv accepts at most one config")
	}

	var config Config
	if len(configs) == 1 {
		config = configs[0]
	}

	prefix := strings.TrimSuffix(config.EnvPrefix, "_")
	protocol := config.Protocol
	if protocol == "" {
		if prefix == "" {
			prefix = "ZENFORGE"
		}
		protocolEnv := prefix + "_PROVIDER"
		protocol = strings.TrimSpace(os.Getenv(protocolEnv))
		if protocol == "" {
			return nil, fmt.Errorf("%s is not set", protocolEnv)
		}
	}
	normalized, err := normalizeProtocol(protocol)
	if err != nil {
		return nil, err
	}
	config.Protocol = normalized
	if prefix == "" {
		prefix = strings.ToUpper(normalized)
	}
	apiKeyEnv := envName(config.APIKeyEnv, prefix+"_API_KEY")
	baseURLEnv := envName(config.BaseURLEnv, prefix+"_BASE_URL")
	modelEnv := envName(config.ModelEnv, prefix+"_MODEL")

	if config.APIKey == "" {
		config.APIKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
	}
	if config.BaseURL == "" {
		config.BaseURL = strings.TrimSpace(os.Getenv(baseURLEnv))
	}
	if config.Model == "" {
		config.Model = strings.TrimSpace(os.Getenv(modelEnv))
	}
	if config.APIKey == "" {
		return nil, fmt.Errorf("%s is not set", apiKeyEnv)
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("%s is not set", modelEnv)
	}
	return New(config)
}

func normalizeProtocol(protocol string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(protocol)); normalized {
	case OpenAI, Anthropic:
		return normalized, nil
	default:
		return "", fmt.Errorf("unknown model provider: %s", protocol)
	}
}

func envName(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

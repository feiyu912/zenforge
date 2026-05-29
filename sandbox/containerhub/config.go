package containerhub

import (
	"net/http"
	"time"
)

type Config struct {
	BaseURL      string
	AuthToken    string
	Timeout      time.Duration
	DefaultEnvID string
	HTTPClient   *http.Client
}

func (c Config) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

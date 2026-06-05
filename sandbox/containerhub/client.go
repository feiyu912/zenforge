package containerhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/feiyu912/zenforge/sandbox"
)

type Client struct {
	baseURL   string
	authToken string
	http      *http.Client
}

func NewClient(config Config) (*Client, error) {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("containerhub base url is required")
	}
	return &Client{
		baseURL:   baseURL,
		authToken: config.AuthToken,
		http:      config.client(),
	}, nil
}

func (c *Client) CreateSession(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	var session sandbox.Session
	if err := c.doJSON(ctx, http.MethodPost, "/api/sessions/create", req, &session); err != nil {
		return nil, err
	}
	if session.ID == "" {
		session.ID = sandbox.SessionKey(req.RunID, req.SubtaskID)
	}
	if session.RunID == "" {
		session.RunID = req.RunID
	}
	if session.SubtaskID == "" {
		session.SubtaskID = req.SubtaskID
	}
	if session.EnvironmentID == "" {
		session.EnvironmentID = req.EnvironmentID
	}
	if session.WorkingDir == "" {
		session.WorkingDir = req.WorkingDir
	}
	return &session, nil
}

func (c *Client) ExecuteSession(ctx context.Context, sessionID string, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	var result sandbox.ExecuteResult
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/execute"
	if err := c.doJSON(ctx, http.MethodPost, path, req, &result); err != nil {
		return sandbox.ExecuteResult{}, err
	}
	return result, nil
}

func (c *Client) StopSession(ctx context.Context, sessionID string) error {
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/stop"
	return c.doJSON(ctx, http.MethodPost, path, nil, nil)
}

func (c *Client) EnvironmentPrompt(ctx context.Context, environmentID string) (sandbox.Prompt, error) {
	path := "/api/environments/" + url.PathEscape(environmentID) + "/agent-prompt"
	res, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return sandbox.Prompt{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return sandbox.Prompt{}, err
	}
	contentType := res.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") || json.Valid(body) {
		var prompt sandbox.Prompt
		if err := json.Unmarshal(body, &prompt); err != nil {
			return sandbox.Prompt{}, err
		}
		if prompt.EnvironmentID == "" {
			prompt.EnvironmentID = environmentID
		}
		return prompt, nil
	}
	return sandbox.Prompt{EnvironmentID: environmentID, Content: string(body)}, nil
}

func (c *Client) RuntimeInfo(ctx context.Context) (map[string]any, error) {
	var info map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/runtime-info", nil, &info); err != nil {
		return nil, err
	}
	return info, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	res, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode containerhub response: %w", err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/plain")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSandboxUnavailable, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return nil, fmt.Errorf("%w: containerhub %s %s failed: status=%d body=%s", containerHubErrorCode(path, res.StatusCode), method, path, res.StatusCode, strings.TrimSpace(string(data)))
}

func containerHubErrorCode(path string, status int) sandbox.ErrorCode {
	switch {
	case strings.Contains(path, "/agent-prompt") && status == http.StatusNotFound:
		return sandbox.ErrEnvironmentNotFound
	case path == "/api/sessions/create":
		return sandbox.ErrSessionOpenFailed
	case strings.Contains(path, "/execute"):
		return sandbox.ErrExecuteFailed
	case strings.Contains(path, "/stop") && status == http.StatusNotFound:
		return sandbox.ErrClosed
	default:
		return sandbox.ErrSandboxUnavailable
	}
}

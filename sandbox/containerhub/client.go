package containerhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

const maxResponseBytes int64 = 8 << 20

type Client struct {
	baseURL   string
	authToken string
	http      *http.Client
}

func NewClient(config Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("containerhub base url is required")
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid containerhub base url: %q", config.BaseURL)
	}
	return &Client{
		baseURL:   baseURL,
		authToken: config.AuthToken,
		http:      config.client(),
	}, nil
}

func (c *Client) CreateSession(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	sessionID := sandbox.SessionKey(req.RunID, req.SubtaskID)
	if sessionID == "" {
		return nil, fmt.Errorf("%w: run id is required", sandbox.ErrSessionOpenFailed)
	}
	payload := map[string]any{
		"session_id":       sessionID,
		"environment_name": req.EnvironmentID,
		"cwd":              req.WorkingDir,
		"labels":           sessionLabels(req),
		"mounts":           sessionMounts(req.Mounts),
	}
	if len(req.Env) > 0 {
		payload["env"] = req.Env
	}
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/sessions/create", payload, &response); err != nil {
		return nil, err
	}
	if returnedID := firstString(response, "session_id", "id"); returnedID != "" {
		sessionID = returnedID
	}
	environmentID := firstString(response, "environment_name", "environmentId")
	if environmentID == "" {
		environmentID = req.EnvironmentID
	}
	workingDir := firstString(response, "cwd", "workingDirectory", "workingDir")
	if workingDir == "" {
		workingDir = req.WorkingDir
	}
	return &sandbox.Session{
		ID:            sessionID,
		RunID:         strings.TrimSpace(req.RunID),
		SubtaskID:     strings.TrimSpace(req.SubtaskID),
		EnvironmentID: environmentID,
		WorkingDir:    workingDir,
		Metadata:      cloneMap(req.Metadata),
	}, nil
}

func (c *Client) ExecuteSession(ctx context.Context, sessionID string, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sandbox.ExecuteResult{}, sandbox.ErrClosed
	}
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/execute"
	payload := map[string]any{
		"command": "/bin/sh",
		"args":    []string{"-lc", req.Command},
		"cwd":     req.CWD,
	}
	if req.Timeout > 0 {
		payload["timeout_ms"] = durationMilliseconds(req.Timeout)
	}
	if len(req.Env) > 0 {
		payload["env"] = req.Env
	}
	res, err := c.do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return sandbox.ExecuteResult{}, err
	}
	defer res.Body.Close()
	data, err := readResponseBody(res.Body)
	if err != nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: read execute response: %v", responseReadErrorCode(err, sandbox.ErrExecuteFailed), err)
	}
	if !strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "application/json") {
		return sandbox.ExecuteResult{ExitCode: 0, Stdout: string(data), WorkingDirectory: req.CWD}, nil
	}
	var response map[string]any
	if err := json.Unmarshal(data, &response); err != nil {
		return sandbox.ExecuteResult{}, fmt.Errorf("%w: decode response: %v", sandbox.ErrExecuteFailed, err)
	}
	result := sandbox.ExecuteResult{
		ExitCode:         firstInt(response, 0, "exit_code", "exitCode"),
		Stdout:           firstString(response, "stdout"),
		Stderr:           firstString(response, "stderr"),
		WorkingDirectory: firstString(response, "working_directory", "workingDirectory"),
		Metadata:         cloneMap(req.Metadata),
	}
	if result.WorkingDirectory == "" {
		result.WorkingDirectory = req.CWD
	}
	if firstBool(response, "timed_out", "timedOut") {
		return result, sandbox.ErrTimeout
	}
	return result, nil
}

func (c *Client) StopSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sandbox.ErrClosed
	}
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/stop"
	return c.doJSON(ctx, http.MethodPost, path, map[string]any{}, nil)
}

func (c *Client) EnvironmentPrompt(ctx context.Context, environmentID string) (sandbox.Prompt, error) {
	path := "/api/environments/" + url.PathEscape(environmentID) + "/agent-prompt"
	res, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return sandbox.Prompt{}, err
	}
	defer res.Body.Close()
	body, err := readResponseBody(res.Body)
	if err != nil {
		return sandbox.Prompt{}, fmt.Errorf("%w: read environment prompt: %v", responseReadErrorCode(err, sandbox.ErrSandboxUnavailable), err)
	}
	contentType := res.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var response map[string]any
		if err := json.Unmarshal(body, &response); err != nil {
			return sandbox.Prompt{}, fmt.Errorf("%w: decode environment prompt: %v", sandbox.ErrSandboxUnavailable, err)
		}
		prompt := sandbox.Prompt{
			EnvironmentID: firstString(response, "environmentId", "environment_name", "environmentName"),
			Content:       firstString(response, "content", "prompt"),
		}
		if metadata, ok := response["metadata"].(map[string]any); ok {
			prompt.Metadata = cloneMap(metadata)
		}
		if prompt.EnvironmentID == "" {
			prompt.EnvironmentID = strings.TrimSpace(environmentID)
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
		_, err := readResponseBody(res.Body)
		if err != nil {
			return fmt.Errorf("%w: read containerhub response: %v", responseReadErrorCode(err, containerHubErrorCode(path, res.StatusCode)), err)
		}
		return nil
	}
	data, err := readResponseBody(res.Body)
	if err != nil {
		return fmt.Errorf("%w: read containerhub response: %v", responseReadErrorCode(err, containerHubErrorCode(path, res.StatusCode)), err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode containerhub response: %v", containerHubErrorCode(path, res.StatusCode), err)
	}
	return nil
}

func readResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d byte limit", sandbox.ErrResponseTooLarge, maxResponseBytes)
	}
	return data, nil
}

func responseReadErrorCode(err error, fallback sandbox.ErrorCode) sandbox.ErrorCode {
	if errors.Is(err, sandbox.ErrResponseTooLarge) {
		return sandbox.ErrResponseTooLarge
	}
	return fallback
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
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %v", sandbox.ErrTimeout, err)
		}
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		return nil, fmt.Errorf("%w: %v", sandbox.ErrSandboxUnavailable, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	data, err := readResponseBody(res.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read containerhub error response: %v", responseReadErrorCode(err, containerHubErrorCode(path, res.StatusCode)), err)
	}
	if len(data) > 4096 {
		data = data[:4096]
	}
	return nil, fmt.Errorf("%w: containerhub %s %s failed: status=%d detail=%s", containerHubErrorCode(path, res.StatusCode), method, path, res.StatusCode, errorDetail(data))
}

func containerHubErrorCode(path string, status int) sandbox.ErrorCode {
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return sandbox.ErrTimeout
	case strings.Contains(path, "/agent-prompt") && status == http.StatusNotFound:
		return sandbox.ErrEnvironmentNotFound
	case path == "/api/sessions/create" && status == http.StatusNotFound:
		return sandbox.ErrEnvironmentNotFound
	case path == "/api/sessions/create":
		return sandbox.ErrSessionOpenFailed
	case strings.Contains(path, "/execute") && (status == http.StatusNotFound || status == http.StatusConflict):
		return sandbox.ErrClosed
	case strings.Contains(path, "/execute"):
		return sandbox.ErrExecuteFailed
	case strings.Contains(path, "/stop") && status == http.StatusNotFound:
		return sandbox.ErrClosed
	default:
		return sandbox.ErrSandboxUnavailable
	}
}

func sessionLabels(req sandbox.OpenRequest) map[string]string {
	labels := make(map[string]string, len(req.Metadata)+2)
	for key, value := range req.Metadata {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			labels[key] = text
		}
	}
	labels["runId"] = strings.TrimSpace(req.RunID)
	if subtaskID := strings.TrimSpace(req.SubtaskID); subtaskID != "" {
		labels["subtaskId"] = subtaskID
	}
	return labels
}

func sessionMounts(mounts []sandbox.Mount) []map[string]any {
	out := make([]map[string]any, 0, len(mounts))
	for _, mount := range mounts {
		mode := strings.ToLower(strings.TrimSpace(mount.Mode))
		out = append(out, map[string]any{
			"source":      mount.Source,
			"destination": mount.Destination,
			"read_only":   mode == "ro" || mode == "readonly" || mode == "read_only" || mode == "read-only",
		})
	}
	return out
}

func durationMilliseconds(timeout time.Duration) int64 {
	milliseconds := timeout.Milliseconds()
	if milliseconds == 0 {
		return 1
	}
	return milliseconds
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstInt(values map[string]any, fallback int, keys ...string) int {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			parsed, err := value.Int64()
			if err == nil {
				return int(parsed)
			}
		}
	}
	return fallback
}

func firstBool(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok {
			return value
		}
	}
	return false
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func errorDetail(data []byte) string {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "empty response"
	}
	var response map[string]any
	if json.Unmarshal(data, &response) == nil {
		if detail := firstString(response, "error", "message", "msg", "detail"); detail != "" {
			return detail
		}
	}
	return trimmed
}

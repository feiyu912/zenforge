package containerhub

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

func TestClientShapesRequestsAndHeaders(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: `{"id":"session_1","workingDir":"/workspace"}`, contentType: "application/json"},
		{status: 200, body: `{"exitCode":0,"stdout":"ok","workingDirectory":"/workspace"}`, contentType: "application/json"},
		{status: 204},
	}}
	client, err := NewClient(Config{
		BaseURL:    "https://hub.example",
		AuthToken:  "token",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	session, err := client.CreateSession(context.Background(), sandbox.OpenRequest{RunID: "run_1", EnvironmentID: "go", WorkingDir: "/workspace"})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	result, err := client.ExecuteSession(context.Background(), session.ID, sandbox.ExecuteRequest{Command: "go test ./...", Timeout: time.Second})
	if err != nil {
		t.Fatalf("ExecuteSession returned error: %v", err)
	}
	if err := client.StopSession(context.Background(), session.ID); err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	if session.ID != "session_1" || result.Stdout != "ok" {
		t.Fatalf("unexpected session/result: %#v %#v", session, result)
	}
	assertRequest(t, transport.requests[0], http.MethodPost, "/api/sessions/create", "Bearer token", `"runId":"run_1"`)
	assertRequest(t, transport.requests[1], http.MethodPost, "/api/sessions/session_1/execute", "Bearer token", `"command":"go test ./..."`)
	assertRequest(t, transport.requests[2], http.MethodPost, "/api/sessions/session_1/stop", "Bearer token", "")
}

func TestClientPromptTextAndJSON(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: "plain prompt", contentType: "text/plain"},
		{status: 200, body: `{"content":"json prompt","metadata":{"k":"v"}}`, contentType: "application/json"},
		{status: 200, body: `{"version":"dev"}`, contentType: "application/json"},
	}}
	client, err := NewClient(Config{BaseURL: "https://hub.example", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	textPrompt, err := client.EnvironmentPrompt(context.Background(), "go")
	if err != nil {
		t.Fatalf("EnvironmentPrompt text returned error: %v", err)
	}
	jsonPrompt, err := client.EnvironmentPrompt(context.Background(), "python")
	if err != nil {
		t.Fatalf("EnvironmentPrompt json returned error: %v", err)
	}
	info, err := client.RuntimeInfo(context.Background())
	if err != nil {
		t.Fatalf("RuntimeInfo returned error: %v", err)
	}
	if textPrompt.Content != "plain prompt" || jsonPrompt.Content != "json prompt" || info["version"] != "dev" {
		t.Fatalf("unexpected prompt/info: %#v %#v %#v", textPrompt, jsonPrompt, info)
	}
	assertRequest(t, transport.requests[0], http.MethodGet, "/api/environments/go/agent-prompt", "", "")
	assertRequest(t, transport.requests[1], http.MethodGet, "/api/environments/python/agent-prompt", "", "")
	assertRequest(t, transport.requests[2], http.MethodGet, "/api/runtime-info", "", "")
}

func TestAdapterImplementsSandbox(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: `{"id":"session_1","environmentId":"go"}`, contentType: "application/json"},
		{status: 200, body: `{"exitCode":0,"stdout":"ok"}`, contentType: "application/json"},
		{status: 204},
		{status: 200, body: "adapter prompt", contentType: "text/plain"},
	}}
	adapter, err := New(Config{
		BaseURL:      "https://hub.example",
		DefaultEnvID: "go",
		HTTPClient:   &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "printf ok"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if err := adapter.Close(context.Background(), session); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	prompt, err := adapter.Prompt(context.Background(), "")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if session.EnvironmentID != "go" || result.Stdout != "ok" || prompt.EnvironmentID != "go" {
		t.Fatalf("unexpected adapter values: %#v %#v %#v", session, result, prompt)
	}
}

type httpResponse struct {
	status      int
	body        string
	contentType string
}

type recordedRequest struct {
	method string
	path   string
	auth   string
	body   string
}

type recordingTransport struct {
	requests  []recordedRequest
	responses []httpResponse
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	t.requests = append(t.requests, recordedRequest{
		method: req.Method,
		path:   req.URL.Path,
		auth:   req.Header.Get("Authorization"),
		body:   string(body),
	})
	response := httpResponse{status: 200}
	if len(t.responses) > 0 {
		response = t.responses[0]
		t.responses = t.responses[1:]
	}
	if response.status == 0 {
		response.status = 200
	}
	header := make(http.Header)
	if response.contentType != "" {
		header.Set("Content-Type", response.contentType)
	}
	return &http.Response{
		StatusCode: response.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Request:    req,
	}, nil
}

func assertRequest(t *testing.T, req recordedRequest, method, path, auth, bodyContains string) {
	t.Helper()
	if req.method != method || req.path != path {
		t.Fatalf("request = %#v, want method=%s path=%s", req, method, path)
	}
	if auth != "" && req.auth != auth {
		t.Fatalf("auth = %q, want %q", req.auth, auth)
	}
	if bodyContains != "" && !strings.Contains(req.body, bodyContains) {
		t.Fatalf("body = %q, want to contain %q", req.body, bodyContains)
	}
}

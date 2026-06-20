package containerhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

func TestClientShapesRequestsAndHeaders(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: `{"session_id":"session_1","environment_name":"go","cwd":"/workspace"}`, contentType: "application/json"},
		{status: 200, body: `{"exit_code":0,"stdout":"ok","working_directory":"/workspace"}`, contentType: "application/json"},
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
	session, err := client.CreateSession(context.Background(), sandbox.OpenRequest{
		RunID:         "run_1",
		SubtaskID:     "sub_1",
		EnvironmentID: "go",
		WorkingDir:    "/workspace",
		Env:           map[string]string{"GOFLAGS": "-mod=readonly"},
		Mounts:        []sandbox.Mount{{Source: "/host", Destination: "/workspace", Mode: "ro"}},
		Metadata:      map[string]any{"toolCallId": "call_1", "ignored": 42},
	})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	result, err := client.ExecuteSession(context.Background(), session.ID, sandbox.ExecuteRequest{Command: "go test ./...", CWD: "/workspace", Timeout: time.Second})
	if err != nil {
		t.Fatalf("ExecuteSession returned error: %v", err)
	}
	if err := client.StopSession(context.Background(), session.ID); err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	if session.ID != "session_1" || session.RunID != "run_1" || session.SubtaskID != "sub_1" || result.Stdout != "ok" {
		t.Fatalf("unexpected session/result: %#v %#v", session, result)
	}
	assertRequest(t, transport.requests[0], http.MethodPost, "/api/sessions/create", "Bearer token", "")
	createBody := decodeBody(t, transport.requests[0])
	if createBody["session_id"] != "run-run_1-sub_1" || createBody["environment_name"] != "go" || createBody["cwd"] != "/workspace" {
		t.Fatalf("create body = %#v", createBody)
	}
	labels := createBody["labels"].(map[string]any)
	if labels["runId"] != "run_1" || labels["subtaskId"] != "sub_1" || labels["toolCallId"] != "call_1" {
		t.Fatalf("create labels = %#v", labels)
	}
	mount := createBody["mounts"].([]any)[0].(map[string]any)
	if mount["read_only"] != true || mount["source"] != "/host" {
		t.Fatalf("create mount = %#v", mount)
	}
	assertRequest(t, transport.requests[1], http.MethodPost, "/api/sessions/session_1/execute", "Bearer token", "")
	executeBody := decodeBody(t, transport.requests[1])
	if executeBody["command"] != "/bin/sh" || executeBody["cwd"] != "/workspace" || executeBody["timeout_ms"] != float64(1000) {
		t.Fatalf("execute body = %#v", executeBody)
	}
	args := executeBody["args"].([]any)
	if len(args) != 2 || args[0] != "-lc" || args[1] != "go test ./..." {
		t.Fatalf("execute args = %#v", args)
	}
	assertRequest(t, transport.requests[2], http.MethodPost, "/api/sessions/session_1/stop", "Bearer token", "")
}

func TestClientHandlesPlainTextAndTimedOutExecution(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: "plain output", contentType: "text/plain; charset=utf-8"},
		{status: 200, body: `{"exit_code":124,"stderr":"timed out","timed_out":true}`, contentType: "application/json"},
	}}
	client, err := NewClient(Config{BaseURL: "https://hub.example", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	plain, err := client.ExecuteSession(context.Background(), "session_1", sandbox.ExecuteRequest{Command: "printf ok", CWD: "/workspace"})
	if err != nil || plain.Stdout != "plain output" || plain.WorkingDirectory != "/workspace" {
		t.Fatalf("plain execution = %#v err=%v", plain, err)
	}
	timedOut, err := client.ExecuteSession(context.Background(), "session_1", sandbox.ExecuteRequest{Command: "sleep 2"})
	if sandbox.Code(err) != sandbox.ErrTimeout || timedOut.ExitCode != 124 || timedOut.Stderr != "timed out" {
		t.Fatalf("timed out execution = %#v err=%v", timedOut, err)
	}
}

func TestClientPromptTextAndJSON(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: "plain prompt", contentType: "text/plain"},
		{status: 200, body: `{"environment_name":"python","has_prompt":true,"prompt":"json prompt"}`, contentType: "application/json"},
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

func TestClientRejectsInvalidBaseURLAndEmptySessionIDs(t *testing.T) {
	if _, err := NewClient(Config{BaseURL: "://bad"}); err == nil {
		t.Fatalf("expected invalid base URL error")
	}
	client, err := NewClient(Config{BaseURL: "https://hub.example", HTTPClient: &http.Client{Transport: &recordingTransport{}}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.CreateSession(context.Background(), sandbox.OpenRequest{}); sandbox.Code(err) != sandbox.ErrSessionOpenFailed {
		t.Fatalf("empty run error = %v", err)
	}
	if _, err := client.ExecuteSession(context.Background(), " ", sandbox.ExecuteRequest{}); sandbox.Code(err) != sandbox.ErrClosed {
		t.Fatalf("empty execute session error = %v", err)
	}
	if err := client.StopSession(context.Background(), " "); sandbox.Code(err) != sandbox.ErrClosed {
		t.Fatalf("empty stop session error = %v", err)
	}
}

func TestClientMapsHTTPFailuresToSandboxCodes(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 500, body: "create failed"},
		{status: 500, body: "execute failed"},
		{status: 404, body: "missing prompt"},
		{status: 409, body: `{"message":"session is stopped"}`},
		{status: 504, body: "gateway timeout"},
	}}
	client, err := NewClient(Config{BaseURL: "https://hub.example", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.CreateSession(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); sandbox.Code(err) != sandbox.ErrSessionOpenFailed {
		t.Fatalf("CreateSession error code = %q err=%v", sandbox.Code(err), err)
	}
	if _, err := client.ExecuteSession(context.Background(), "session_1", sandbox.ExecuteRequest{Command: "printf ok"}); sandbox.Code(err) != sandbox.ErrExecuteFailed {
		t.Fatalf("ExecuteSession error code = %q err=%v", sandbox.Code(err), err)
	}
	if _, err := client.EnvironmentPrompt(context.Background(), "missing"); sandbox.Code(err) != sandbox.ErrEnvironmentNotFound {
		t.Fatalf("EnvironmentPrompt error code = %q err=%v", sandbox.Code(err), err)
	}
	if _, err := client.ExecuteSession(context.Background(), "stopped", sandbox.ExecuteRequest{Command: "printf ok"}); sandbox.Code(err) != sandbox.ErrClosed {
		t.Fatalf("stopped session error code = %q err=%v", sandbox.Code(err), err)
	}
	if _, err := client.ExecuteSession(context.Background(), "slow", sandbox.ExecuteRequest{Command: "sleep 10"}); sandbox.Code(err) != sandbox.ErrTimeout {
		t.Fatalf("gateway timeout error code = %q err=%v", sandbox.Code(err), err)
	}
}

func TestClientMapsMalformedSuccessResponsesToSandboxCodes(t *testing.T) {
	transport := &recordingTransport{responses: []httpResponse{
		{status: 200, body: `{`, contentType: "application/json"},
		{status: 200, body: `{`, contentType: "application/json"},
		{status: 200, body: `{`, contentType: "application/json"},
	}}
	client, err := NewClient(Config{BaseURL: "https://hub.example", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.CreateSession(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); sandbox.Code(err) != sandbox.ErrSessionOpenFailed {
		t.Fatalf("malformed create error code = %q err=%v", sandbox.Code(err), err)
	}
	if _, err := client.ExecuteSession(context.Background(), "session_1", sandbox.ExecuteRequest{}); sandbox.Code(err) != sandbox.ErrExecuteFailed {
		t.Fatalf("malformed execute error code = %q err=%v", sandbox.Code(err), err)
	}
	if _, err := client.EnvironmentPrompt(context.Background(), "go"); sandbox.Code(err) != sandbox.ErrSandboxUnavailable {
		t.Fatalf("malformed prompt error code = %q err=%v", sandbox.Code(err), err)
	}
}

func TestClientMapsTransportCancellationAndTimeout(t *testing.T) {
	timeoutClient, err := NewClient(Config{
		BaseURL:    "https://hub.example",
		HTTPClient: &http.Client{Transport: errorTransport{err: context.DeadlineExceeded}},
	})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := timeoutClient.RuntimeInfo(context.Background()); sandbox.Code(err) != sandbox.ErrTimeout {
		t.Fatalf("timeout error code = %q err=%v", sandbox.Code(err), err)
	}

	cancelClient, err := NewClient(Config{
		BaseURL:    "https://hub.example",
		HTTPClient: &http.Client{Transport: errorTransport{err: context.Canceled}},
	})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := cancelClient.RuntimeInfo(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
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

type errorTransport struct {
	err error
}

func (t errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
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

func decodeBody(t *testing.T, req recordedRequest) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatalf("decode request body %q: %v", req.body, err)
	}
	return body
}

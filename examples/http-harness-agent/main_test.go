package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPHarnessServesDetachedRunWithCompatibleProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"smoke complete\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()

	binary := filepath.Join(t.TempDir(), "http-harness-agent")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build HTTP harness: %v\n%s", err, output)
	}
	address := freeLoopbackAddress(t)
	dataDir := t.TempDir()
	args := []string{
		"-addr", address,
		"-data-dir", dataDir,
		"-workspace", "..",
		"-skill-root", "../harness-agent/skills",
	}
	env := append(os.Environ(),
		"ZENFORGE_PROVIDER=openai",
		"ZENFORGE_MODEL=smoke-model",
		"ZENFORGE_API_KEY=smoke-key",
		"ZENFORGE_BASE_URL="+provider.URL+"/v1",
	)
	var output bytes.Buffer
	newCommand := func() *exec.Cmd {
		command := exec.Command(binary, args...)
		command.Env = env
		command.Stdout = &output
		command.Stderr = &output
		return command
	}
	command := newCommand()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process == nil || command.ProcessState != nil && command.ProcessState.Exited() {
			return
		}
		stopHarness(t, command, &output)
	})

	baseURL := "http://" + address
	client := &http.Client{Timeout: 2 * time.Second}
	waitForHTTP(t, client, baseURL+"/runs")
	response, err := client.Post(baseURL+"/runs/start", "application/json", strings.NewReader(
		`{"runId":"smoke_run","input":"Return the smoke response."}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %s; output=%s", response.Status, output.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		status, err := client.Get(baseURL + "/runs/status?runId=smoke_run")
		if err == nil {
			var body struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(status.Body).Decode(&body)
			_ = status.Body.Close()
			if decodeErr == nil && body.Status == "completed" {
				completed = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("detached run did not complete; output=%s", output.String())
	}
	attach, err := client.Get(baseURL + "/runs/attach?runId=smoke_run")
	if err != nil {
		t.Fatal(err)
	}
	defer attach.Body.Close()
	data, err := io.ReadAll(attach.Body)
	if err != nil {
		t.Fatal(err)
	}
	if attach.StatusCode != http.StatusOK || !strings.Contains(string(data), "smoke complete") {
		t.Fatalf("attach status=%s body=%q output=%s", attach.Status, data, output.String())
	}
	stopHarness(t, command, &output)

	command = newCommand()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForHTTP(t, client, baseURL+"/runs")
	status, err := client.Get(baseURL + "/runs/status?runId=smoke_run")
	if err != nil {
		t.Fatal(err)
	}
	defer status.Body.Close()
	var persisted struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(status.Body).Decode(&persisted); err != nil {
		t.Fatal(err)
	}
	if status.StatusCode != http.StatusOK || persisted.Status != "completed" {
		t.Fatalf("restarted status=%s body=%+v output=%s", status.Status, persisted, output.String())
	}
	replayed, err := client.Get(baseURL + "/runs/attach?runId=smoke_run")
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Body.Close()
	data, err = io.ReadAll(replayed.Body)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.StatusCode != http.StatusOK || !strings.Contains(string(data), "smoke complete") {
		t.Fatalf("restarted attach status=%s body=%q output=%s", replayed.Status, data, output.String())
	}
}

func TestHTTPHarnessRunsApprovedDockerShellWhenEnabled(t *testing.T) {
	if os.Getenv("ZENFORGE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ZENFORGE_DOCKER_INTEGRATION=1 to run HTTP Docker integration")
	}
	var mu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		turn := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"shell-1\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"uname -s\\\",\\\"description\\\":\\\"verify the Docker sandbox\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"approved Docker sandbox complete\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()

	binary := filepath.Join(t.TempDir(), "http-harness-agent")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build HTTP harness: %v\n%s", err, output)
	}
	address := freeLoopbackAddress(t)
	command := exec.Command(binary,
		"-addr", address,
		"-data-dir", t.TempDir(),
		"-workspace", "..",
		"-skill-root", "../harness-agent/skills",
	)
	command.Env = append(os.Environ(),
		"ZENFORGE_PROVIDER=openai",
		"ZENFORGE_MODEL=docker-smoke-model",
		"ZENFORGE_API_KEY=docker-smoke-key",
		"ZENFORGE_BASE_URL="+provider.URL+"/v1",
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil && (command.ProcessState == nil || !command.ProcessState.Exited()) {
			stopHarness(t, command, &output)
		}
	})

	baseURL := "http://" + address
	client := &http.Client{Timeout: 5 * time.Second}
	waitForHTTP(t, client, baseURL+"/runs")
	response, err := client.Post(baseURL+"/runs/start", "application/json", strings.NewReader(
		`{"runId":"docker_smoke","input":"Use the shell to verify the sandbox."}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %s; output=%s", response.Status, output.String())
	}

	requestID := waitForApprovalID(t, client, baseURL, "docker_smoke", &output)
	approvalBody := fmt.Sprintf(`{"requestId":%q,"action":"approve","scope":"once"}`, requestID)
	approved, err := client.Post(baseURL+"/approval", "application/json", strings.NewReader(approvalBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = approved.Body.Close()
	if approved.StatusCode != http.StatusOK {
		t.Fatalf("approval status = %s; output=%s", approved.Status, output.String())
	}
	waitForRunCompletion(t, client, baseURL, "docker_smoke", &output)

	attach, err := client.Get(baseURL + "/runs/attach?runId=docker_smoke")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(attach.Body)
	_ = attach.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if attach.StatusCode != http.StatusOK || !strings.Contains(string(data), "approved Docker sandbox complete") {
		t.Fatalf("attach status=%s body=%q output=%s", attach.Status, data, output.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d; output=%s", len(requests), output.String())
	}
	second, _ := json.Marshal(requests[1]["messages"])
	if !strings.Contains(string(second), `"tool_call_id":"shell-1"`) || !strings.Contains(string(second), "Linux") {
		t.Fatalf("second provider request does not include Docker tool result: %s", second)
	}
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForHTTP(t *testing.T, client *http.Client, url string) {
	t.Helper()
	// This example builds and starts a separate process. Allow it enough time
	// to acquire CPU while the full repository test suite is building packages.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not start at %s", url)
}

func waitForApprovalID(t *testing.T, client *http.Client, baseURL, runID string, output *bytes.Buffer) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/approvals?runId=" + runID)
		if err == nil {
			var body struct {
				Approvals []struct {
					ID string `json:"id"`
				} `json:"approvals"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if decodeErr == nil && len(body.Approvals) == 1 && body.Approvals[0].ID != "" {
				return body.Approvals[0].ID
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("approval did not become pending; output=%s", output.String())
	return ""
}

func waitForRunCompletion(t *testing.T, client *http.Client, baseURL, runID string, output *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/runs/status?runId=" + runID)
		if err == nil {
			var body struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if decodeErr == nil && body.Status == "completed" {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("detached run did not complete; output=%s", output.String())
}

func waitForExit(t *testing.T, command *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HTTP harness stopped with error: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("HTTP harness did not stop\n%s", output.String())
	}
}

func stopHarness(t *testing.T, command *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt HTTP harness: %v\n%s", err, output.String())
	}
	waitForExit(t, command, output)
}

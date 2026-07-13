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
	command := exec.Command(binary,
		"-addr", address,
		"-data-dir", dataDir,
		"-workspace", "..",
		"-skill-root", "../harness-agent/skills",
	)
	command.Env = append(os.Environ(),
		"ZENFORGE_PROVIDER=openai",
		"ZENFORGE_MODEL=smoke-model",
		"ZENFORGE_API_KEY=smoke-key",
		"ZENFORGE_BASE_URL="+provider.URL+"/v1",
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process == nil || command.ProcessState != nil && command.ProcessState.Exited() {
			return
		}
		_ = command.Process.Signal(os.Interrupt)
		waitForExit(t, command, &output)
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
	deadline := time.Now().Add(5 * time.Second)
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
